"""Local Flask entry for fresh, bounded BettaFish workflows managed by AegisRT."""
from __future__ import annotations

import atexit
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import threading
import uuid
from types import SimpleNamespace
from urllib.parse import urlsplit

from flask import Blueprint, Response, jsonify, request
from sample_inputs import SampleInputError, freeze_sample_values, load_sample_inputs, validate_sample
from runner import LIVE_REQUEST_LIMITS, REFINEMENT_REQUEST_LIMITS, task_stages

ROLES = ("query", "media", "insight", "forum", "report")
NAMES = dict(zip(ROLES, ("信息检索", "媒体分析", "评论分析", "汇总讨论", "生成报告")))
STAGE_IDS = [suffix for suffix, _role, _phase in task_stages(SimpleNamespace(mode="live"))]
NAMES.update({role + "-feedback": NAMES[role] + "补充研究" for role in ROLES[:3]})
NAMES["forum-final"] = "最终讨论"
LIVE_REQUEST_LIMIT = sum(LIVE_REQUEST_LIMITS) + LIVE_REQUEST_LIMITS[3] + sum(REFINEMENT_REQUEST_LIMITS)
ACTIVE = {"QUEUED", "RUNNING", "CANCELING"}
IDENTIFIER = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,119}\Z")
MAX_BODY = 1_000_000


def read_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def save_json(path, value):
    path = Path(path)
    temporary = path.with_name(path.name + "." + uuid.uuid4().hex + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def utc_now():
    return datetime.now(timezone.utc).isoformat()


class EntryError(Exception):
    def __init__(self, message, status=400):
        super().__init__(message)
        self.status = status


class WorkflowService:
    def __init__(self, *, source_dir, integration_dir, binary, env_file, run_root, sample_root, launch=True):
        self.source_dir = Path(source_dir).resolve()
        self.integration_dir = Path(integration_dir).resolve()
        self.binary, self.env_file = Path(binary).resolve(), Path(env_file).resolve()
        self.run_root, self.sample_root = Path(run_root).resolve(), Path(sample_root).resolve()
        self.run_root.mkdir(parents=True, exist_ok=True)
        self.sample_root.mkdir(parents=True, exist_ok=True)
        self.lock = threading.RLock()
        self.threads = {}
        self.processes = {}
        self.launch = launch
        from observed_workflows import load_registry
        self.observed, self.default_workflow_id = load_registry(
            self.run_root.parent / 'observed-runs.json', env_file=self.env_file, binary=self.binary)

    def reader(self, workflow_id):
        return self.observed.get(workflow_id, self)

    def ready(self):
        return all(path.is_file() for path in (self.binary, self.env_file,
                   self.integration_dir / "runner.py", self.integration_dir / "adapter.py",
                   self.source_dir / "app.py"))

    def job_dir(self, workflow_id):
        if not isinstance(workflow_id, str) or not IDENTIFIER.fullmatch(workflow_id):
            raise EntryError("工作流不存在", 404)
        if workflow_id in self.observed:
            return self.observed[workflow_id].job_dir(workflow_id)
        directory = (self.run_root / workflow_id).resolve()
        if directory.parent != self.run_root or not (directory / "entry.json").is_file():
            raise EntryError("工作流不存在", 404)
        return directory

    def catalog(self):
        rows = []
        for folder in sorted(self.sample_root.iterdir()):
            if not folder.is_dir() or not IDENTIFIER.fullmatch(folder.name):
                continue
            if folder.resolve().parent != self.sample_root:
                continue
            try:
                manifest = read_json(folder / "manifest.json")
                sample = load_sample_inputs(folder, manifest["topic"])
                rows.append({"id": folder.name, "topic": sample["topic"],
                             "comment_count": sample["sample_count"], "label": sample["topic"]})
            except (OSError, ValueError, KeyError, TypeError):
                continue
        return rows

    def active_workflow(self):
        for metadata in sorted(self.run_root.glob("*/entry.json"), reverse=True):
            status = self.status(metadata.parent.name)
            if status["status"] in ACTIVE:
                return metadata.parent.name
        return None

    def configuration(self):
        with self.lock:
            active = self.active_workflow()
            return {"success": True, "enabled": True, "ready": self.ready(),
                    "samples": self.catalog(), "max_model_requests": LIVE_REQUEST_LIMIT,
                    "report_max_model_requests": LIVE_REQUEST_LIMITS[-1],
                    "flow_variant": "comprehensive-discussion-feedback", "task_count": len(STAGE_IDS),
                    "active_workflow_id": active, "default_workflow_id": self.default_workflow_id}

    def submit(self, data):
        if not self.ready():
            raise EntryError("分析入口配置尚未就绪", 503)
        if not isinstance(data, dict) or not isinstance(data.get("query"), str):
            raise EntryError("请输入话题并选择对应评论样本")
        topic = data["query"].strip()
        if bool(data.get("sample_id")) == bool(data.get("sample")):
            raise EntryError("请选择一个样本，或上传一份样本文件")
        try:
            if data.get("sample_id"):
                sample_id = data["sample_id"]
                if not isinstance(sample_id, str) or not IDENTIFIER.fullmatch(sample_id):
                    raise EntryError("所选样本不存在")
                folder = (self.sample_root / sample_id).resolve()
                if folder.parent != self.sample_root:
                    raise EntryError("所选样本不存在")
                sample = load_sample_inputs(folder, topic)
            else:
                source = data["sample"]
                if not isinstance(source, dict) or source.get("topic") != topic:
                    raise EntryError("话题与样本不一致，请选择对应样本或重新上传")
                sample = validate_sample(topic, source.get("comments"), source.get("provenance"))
        except (SampleInputError, OSError, KeyError, TypeError, ValueError):
            raise EntryError("样本校验失败，请检查话题、评论标识、正文和来源链接") from None
        with self.lock:
            active = self.active_workflow()
            if active:
                raise EntryError("已有分析正在运行，请等待完成或取消当前分析", 409)
            workflow_id = "bf-entry-" + datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S") + "-" + uuid.uuid4().hex[:10]
            folder = self.run_root / workflow_id
            folder.mkdir(mode=0o700)
            frozen = freeze_sample_values(topic, sample["comments"], sample["provenance"], folder / "inputs")
            metadata = {"workflow_id": workflow_id, "query": topic, "status": "QUEUED",
                        "created_at": utc_now(), "sample_count": sample["sample_count"],
                        "input_manifest_sha256": frozen["manifest_sha256"],
                        "model_request_limit": LIVE_REQUEST_LIMIT, "execution_backend": "aegisrt",
                        "flow_variant": "comprehensive-discussion-feedback", "stage_ids": STAGE_IDS,
                        "research_results_reused": False, "resume_used": False}
            save_json(folder / "entry.json", metadata)
            if self.launch:
                worker = threading.Thread(target=self._run, args=(workflow_id,), daemon=True,
                                          name="bettafish-" + workflow_id)
                self.threads[workflow_id] = worker
                worker.start()
            return {"success": True, "execution_backend": "aegisrt", "workflow_id": workflow_id,
                    "status": "QUEUED", "status_url": "/api/aegisrt/workflows/" + workflow_id}

    def command(self, workflow_id):
        folder = self.job_dir(workflow_id)
        return [sys.executable, str(self.integration_dir / "runner.py"), "--aegisrt", str(self.binary),
                "--python", sys.executable, "--source-dir", str(self.source_dir),
                "--env-file", str(self.env_file), "--input-dir", str(folder / "inputs"),
                "--run-dir", str(self.run_root), "--workflow-id", workflow_id,
                "--mode", "live", "--max-paragraphs", "5", "--max-reflections", "2",
                "--insight-max-reflections", "3", "--max-output-tokens", "16000",
                "--model-request-limits", ",".join(map(str, LIVE_REQUEST_LIMITS)),
                "--refinement-request-limits", ",".join(map(str, REFINEMENT_REQUEST_LIMITS)),
                "--max-content-length", "60000", "--task-timeout", "3600", "--wall-timeout", "7200",
                "--cancel-file", str(folder / "cancel.requested")]

    def _run(self, workflow_id):
        folder = self.job_dir(workflow_id)
        try:
            with self.lock:
                metadata = read_json(folder / "entry.json")
                if (folder / "cancel.requested").exists():
                    metadata.update(status="CANCELED", ended_at=utc_now())
                    save_json(folder / "entry.json", metadata)
                    return
                with (folder / "coordinator.log").open("wb") as log:
                    process = subprocess.Popen(self.command(workflow_id), stdin=subprocess.DEVNULL,
                                               stdout=log, stderr=subprocess.STDOUT,
                                               creationflags=0x08000000 if os.name == "nt" else 0)
                self.processes[workflow_id] = process
                metadata.update(status="RUNNING", coordinator_pid=process.pid, started_at=utc_now())
                save_json(folder / "entry.json", metadata)
            returncode = process.wait()
            with self.lock:
                metadata = read_json(folder / "entry.json")
                summary = read_json(folder / "summary.json") if (folder / "summary.json").exists() else {}
                status = "SUCCEEDED" if summary.get("status") == "completed" and returncode == 0 else (
                    "CANCELED" if (folder / "cancel.requested").exists() else "FAILED")
                metadata.update(status=status, ended_at=utc_now(), coordinator_returncode=returncode)
                save_json(folder / "entry.json", metadata)
        except Exception as exc:
            with self.lock:
                metadata = read_json(folder / "entry.json")
                metadata.update(status="FAILED", ended_at=utc_now(), error_type=type(exc).__name__)
                save_json(folder / "entry.json", metadata)
        finally:
            with self.lock:
                self.processes.pop(workflow_id, None)
                self.threads.pop(workflow_id, None)

    def _coordinator_running(self, workflow_id, metadata):
        if workflow_id in self.threads:
            return True
        pid = metadata.get("coordinator_pid")
        if os.name != "nt" and isinstance(pid, int):
            try:
                arguments = Path(f"/proc/{pid}/cmdline").read_bytes().split(b"\0")
                return (str(self.integration_dir / "runner.py").encode() in arguments and
                        workflow_id.encode() in arguments and str(self.run_root).encode() in arguments)
            except OSError:
                pass
        return not self.launch  # Only offline route tests use launch=False.

    def report_path(self, workflow_id):
        if workflow_id in self.observed:
            try:
                return self.observed[workflow_id].report_path(workflow_id)
            except (OSError, KeyError, ValueError, TypeError):
                raise EntryError("本轮报告尚未通过完整验收", 409) from None
        folder = self.job_dir(workflow_id)
        metadata = read_json(folder / "entry.json")
        summary = read_json(folder / "summary.json") if (folder / "summary.json").exists() else {}
        if metadata["status"] != "SUCCEEDED" and summary.get("status") != "completed":
            raise EntryError("报告尚未生成", 409)
        role_dir = (folder / (workflow_id + "-report")).resolve()
        try:
            if role_dir.parent != folder:
                raise ValueError("report directory outside workflow")
            manifest = read_json(role_dir / "result.json")
            if (manifest.get("schema"), manifest.get("workflow_id"), manifest.get("task_id"), manifest.get("role"), manifest.get("mode")) != (
                    "bettafish.task-result/v1", workflow_id, workflow_id + "-report", "report", "live"):
                raise ValueError("report identity mismatch")
            item = manifest["files"]["report"]
            path = Path(item["path"]).resolve()
            if not path.is_relative_to(role_dir) or path.name != "report.html":
                raise ValueError("report path outside workflow")
            if hashlib.sha256(path.read_bytes()).hexdigest() != item["sha256"]:
                raise ValueError("report digest mismatch")
            return path
        except (OSError, KeyError, TypeError, ValueError):
            raise EntryError("报告完整性检查失败，已停止提供该报告", 409) from None

    def status(self, workflow_id):
        if workflow_id in self.observed:
            return {**self.observed[workflow_id].status(workflow_id), "readonly": True}
        folder = self.job_dir(workflow_id)
        with self.lock:
            metadata = read_json(folder / "entry.json")
            state = metadata["status"]
            summary = read_json(folder / "summary.json") if (folder / "summary.json").is_file() else {}
            if summary.get("workflow_id") == workflow_id and summary.get("status") == "completed":
                state = "SUCCEEDED"
            elif state in ACTIVE and not self._coordinator_running(workflow_id, metadata):
                state = "CANCELED" if (folder / "cancel.requested").exists() else "INTERRUPTED"
            stages = metadata.get("stage_ids", list(ROLES))
            tasks = {stage: "WAITING" for stage in stages}
            snapshot_path = folder / "task-snapshot.json"
            if snapshot_path.is_file():
                snapshot = read_json(snapshot_path)
                for row in snapshot.get("tasks", []):
                    identifier = row.get("task", {}).get("id", "")
                    stage = identifier.removeprefix(workflow_id + "-")
                    if stage in tasks and identifier == workflow_id + "-" + stage:
                        tasks[stage] = row["status"]
            attempts = 0
            for stage in stages:
                path = folder / (workflow_id + "-" + stage) / "model-budget.json"
                if path.is_file():
                    attempts += read_json(path).get("attempts", 0)
            # A completed coordinator also counts preserved rejected report attempts.
            total = summary.get("model_usage", {}).get("provider_attempts")
            if summary.get("workflow_id") == workflow_id and type(total) is int and total >= attempts:
                attempts = total
            report_url, message = None, None
            if state == "SUCCEEDED":
                try:
                    self.report_path(workflow_id)
                    report_url = "/api/aegisrt/workflows/" + workflow_id + "/report"
                except EntryError as exc:
                    state, message = "FAILED", str(exc)
            elif state == "FAILED":
                message = "本轮分析未完成，请查看任务状态后重新提交。已发生的模型请求已计入。"
            elif state == "INTERRUPTED":
                message = "协调进程已中断，本轮未自动恢复。请确认状态后重新提交。"
            return {"success": True, "workflow_id": workflow_id, "query": metadata["query"], "status": state,
                    "tasks": [{"role": stage, "name": NAMES[stage], "status": tasks[stage]} for stage in stages],
                    "model_requests": {"attempts": attempts, "limit": metadata.get("model_request_limit", LIVE_REQUEST_LIMIT)}, "report_url": report_url,
                    "sample_count": metadata["sample_count"], "message": message}

    def cancel(self, workflow_id):
        if workflow_id in self.observed:
            raise EntryError("已登记的真实验收记录只读", 409)
        with self.lock:
            state = self.status(workflow_id)
            if state["status"] not in ACTIVE:
                raise EntryError("本轮分析已经结束", 409)
            folder = self.job_dir(workflow_id)
            (folder / "cancel.requested").write_text(utc_now(), encoding="ascii")
            metadata = read_json(folder / "entry.json")
            metadata["status"] = "CANCELING"
            save_json(folder / "entry.json", metadata)
            return self.status(workflow_id)

    def shutdown(self):
        with self.lock:
            workers = list(self.threads.items())
            for workflow_id, _worker in workers:
                (self.job_dir(workflow_id) / "cancel.requested").write_text(utc_now(), encoding="ascii")
        for _workflow_id, worker in workers:
            worker.join(timeout=20)


def _request_body():
    origin = request.headers.get("Origin")
    if origin and urlsplit(origin).netloc != request.host:
        raise EntryError("请从当前微舆页面提交分析", 403)
    if not request.is_json:
        raise EntryError("请提交JSON格式的分析请求", 415)
    raw = request.stream.read(MAX_BODY + 1)
    if len(raw) > MAX_BODY:
        raise EntryError("样本文件过大", 413)
    try:
        return json.loads(raw.decode("utf-8"))
    except (ValueError, UnicodeError):
        raise EntryError("请求不是有效的JSON") from None


def install_aegisrt_entry(app, source_dir):
    service = WorkflowService(source_dir=source_dir,
        integration_dir=Path(__file__).resolve().parent,
        binary=os.environ["BETTAFISH_AEGISRT_BINARY"], env_file=os.environ["BETTAFISH_AEGISRT_ENV_FILE"],
        run_root=os.environ["BETTAFISH_AEGISRT_RUN_ROOT"], sample_root=os.environ["BETTAFISH_AEGISRT_SAMPLE_ROOT"])
    blueprint = Blueprint("aegisrt_entry", __name__)

    @blueprint.errorhandler(EntryError)
    def entry_error(exc):
        return jsonify(success=False, message=str(exc)), exc.status

    @blueprint.get("/workflows/<workflow_id>")
    def workflow_status(workflow_id):
        return jsonify(service.status(workflow_id))

    @blueprint.post("/workflows/<workflow_id>/cancel")
    def workflow_cancel(workflow_id):
        _request_body()
        return jsonify(service.cancel(workflow_id))

    @blueprint.get("/workflows/<workflow_id>/report")
    def workflow_report(workflow_id):
        response = Response(service.report_path(workflow_id).read_bytes(), content_type="text/html; charset=utf-8")
        response.headers["Content-Security-Policy"] = "default-src 'none'; style-src 'unsafe-inline'; img-src data: https:; script-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'"
        response.headers["X-Content-Type-Options"] = "nosniff"
        response.headers["Cache-Control"] = "no-store"
        return response

    from presentation_api import install_presentation
    install_presentation(blueprint, service)
    app.register_blueprint(blueprint, url_prefix="/api/aegisrt")
    atexit.register(service.shutdown)
    return service


def submit_http_request(service):
    try:
        return jsonify(service.submit(_request_body())), 202
    except EntryError as exc:
        return jsonify(success=False, message=str(exc)), exc.status
