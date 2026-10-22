"""Run isolated BettaFish research, discussion feedback and reporting tasks."""
from __future__ import annotations

import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import time
from datetime import datetime, timezone
from urllib.error import HTTPError, URLError
from urllib.request import Request, build_opener, ProxyHandler

ROLES = ("query", "media", "insight", "forum", "report")
LIVE_REQUEST_LIMITS = (40, 40, 64, 1, 5)
REFINEMENT_REQUEST_LIMITS = (20, 20, 32)
TERMINAL = {"SUCCEEDED", "FAILED", "CANCELED"}


class WorkflowCanceled(Exception):
    pass


def check_cancellation(args):
    if getattr(args, "cancel_file", None) and Path(args.cancel_file).is_file():
        raise WorkflowCanceled("Workflow cancellation requested")


def utc_now():
    return datetime.now(timezone.utc).isoformat()


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def save_json(path, value):
    from durable_io import atomic_text
    atomic_text(path, json.dumps(value, ensure_ascii=False, indent=2) + "\n")


@contextlib.contextmanager
def defer_cleanup_interrupts(cleanup):
    """Record repeated Ctrl+C while the owned daemon is being stopped."""
    previous_handler = signal.getsignal(signal.SIGINT)

    def interrupted(_signum, _frame):
        cleanup["deferred_interrupts"] += 1
        cleanup["error_types"].append("KeyboardInterrupt")

    installed = False
    try:
        signal.signal(signal.SIGINT, interrupted)
        installed = True
    except ValueError:
        pass  # Signal handlers can only be installed from the main thread.
    cleanup["sigint_deferred"] = installed
    try:
        yield
    finally:
        if installed:
            signal.signal(signal.SIGINT, previous_handler)


def stop_owned_daemon(proc, cleanup):
    """Stop only the subprocess created by this workflow, then reap it."""
    if proc is None:
        return
    if proc.poll() is None:
        kill_required = False
        try:
            proc.terminate()
        except (Exception, KeyboardInterrupt) as exc:
            cleanup["error_types"].append(type(exc).__name__)
            kill_required = True
        if not kill_required:
            try:
                proc.wait(timeout=15)
            except (subprocess.TimeoutExpired, KeyboardInterrupt) as exc:
                if isinstance(exc, KeyboardInterrupt):
                    cleanup["error_types"].append(type(exc).__name__)
                kill_required = True
        if kill_required and proc.poll() is None:
            cleanup["daemon_kill_fallback"] = True
            proc.kill()
            proc.wait(timeout=5)
    cleanup["daemon_stopped"] = proc.poll() is not None


def freeze_inputs(repro_dir, output_dir):
    """Copy the explicitly selected previous run, never glob for a latest report."""
    repro = Path(repro_dir).resolve()
    original = repro / "runtime/logs/resume_inputs/0d6c717a-21f7-407d-a96c-bf284d90fc56"
    manifest = json.loads((original / "manifest.json").read_text(encoding="utf-8"))
    target = Path(output_dir).resolve()
    target.mkdir(parents=True, exist_ok=True)
    frozen = {"run_id": manifest["run_id"], "topic": manifest["topic"], "inputs": {}}
    for role in ROLES[:3]:
        item = manifest["inputs"][role]
        report = repro / item["report"].replace("\\", "/")
        state = repro / item["state"].replace("\\", "/")
        if digest(report) != item["report_sha256"]:
            raise ValueError(f"Frozen {role} report hash mismatch")
        data = json.loads(state.read_text(encoding="utf-8"))
        if data.get("query") != manifest["topic"] or not data.get("is_completed"):
            raise ValueError(f"Frozen {role} state is incomplete or has a different topic")
        shutil.copyfile(report, target / f"{role}.md")
        shutil.copyfile(state, target / f"{role}.json")
        frozen["inputs"][role] = {
            "report": f"{role}.md", "state": f"{role}.json",
            "report_sha256": digest(target / f"{role}.md"),
            "state_sha256": digest(target / f"{role}.json"),
        }
    for name in ("comments.json", "provenance.json", "forum.log"):
        shutil.copyfile(original / name, target / name)
    comments = json.loads((target / "comments.json").read_text(encoding="utf-8"))
    if len(comments) != len({row["comment_id"] for row in comments}):
        raise ValueError("Frozen comments have repeated identities")
    frozen["comments_sha256"] = digest(target / "comments.json")
    frozen["sample_count"] = len(comments)
    frozen["frozen_at"] = utc_now()
    save_json(target / "manifest.json", frozen)
    return {"input_dir": str(target), "sample_count": len(comments),
            "manifest_sha256": digest(target / "manifest.json")}


class Client:
    def __init__(self, port, token):
        self.url = f"http://127.0.0.1:{port}"
        self.token = token
        self.opener = build_opener(ProxyHandler({}))

    def call(self, path, body=None):
        headers = {"Authorization": f"Bearer {self.token}"}
        data = None
        if body is not None:
            data = json.dumps(body, ensure_ascii=False).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = Request(self.url + path, data=data, headers=headers)
        try:
            with self.opener.open(request, timeout=10) as response:
                return json.load(response)
        except HTTPError as exc:
            # API errors are safe projections; never echo Authorization headers.
            payload = json.loads(exc.read())
            raise RuntimeError(f"AegisRT API {exc.code}: {payload.get('error', payload)}") from None


def unused_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def resources(role):
    # Admission requests, not measurements of actual CPU/memory/LLM usage.
    return {"cpu_milli": 500, "memory_mib": 2048 if role == "insight" else 512,
            "llm_slots": 1, "token_budget": 16000}


def make_registry(args):
    adapter = Path(__file__).with_name("adapter.py").resolve()
    agents = []
    for role in ROLES:
        command_args = [str(adapter), "--role", role, "--source-dir", str(Path(args.source_dir).resolve()),
                        "--run-dir", str(Path(args.run_dir).resolve()), "--mode", args.mode,
                        "--input-dir", str(Path(args.input_dir).resolve())]
        if args.env_file:
            command_args += ["--env-file", str(Path(args.env_file).resolve())]
        if args.resume_dir:
            command_args += ["--resume-dir", str(Path(args.resume_dir).resolve())]
        agents.append({"id": f"bettafish-{role}", "name": f"BettaFish {role}",
                       "runtime": "process", "entrypoint": str(Path(args.python).resolve()),
                       "args": command_args, "resources": resources(role),
                       "max_concurrency": 1, "execution_timeout": f"{args.task_timeout}s"})
    return {"agents": agents}


def task_stages(args):
    """Keep feedback in explicit task dependencies instead of global forum logs."""
    stages = [(role, role, "initial") for role in ROLES[:3]]
    stages.append(("forum", "forum", "initial"))
    if args.mode == "live":
        stages.extend((role + "-feedback", role, "refinement") for role in ROLES[:3])
        stages.append(("forum-final", "forum", "final"))
    stages.append(("report", "report", "final"))
    return stages


def model_request_plan(args):
    """Count required research steps and recovery capacity without raising caps.

    Search decisions and summaries each need one request; the final research
    report is rendered locally. Database Insight also calls its keyword
    optimizer, while frozen comments and explicit fixed-topic searches do not.
    """
    plan = {}
    manifest_path = Path(args.input_dir) / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8-sig")) if manifest_path.is_file() else {}
    frozen_comments = (manifest.get("schema") == "bettafish.sample-input/v1"
                       or manifest.get("sample_source") == "file")
    fixed_topic = getattr(args, "fixed_topic_search", False)
    for suffix, role, phase in task_stages(args):
        limit = args.model_request_limits[ROLES.index(role)]
        if phase == "refinement":
            limit = getattr(args, "refinement_request_limits", REFINEMENT_REQUEST_LIMITS)[ROLES.index(role)]
        if args.mode != "live":
            required, recovery, limit = 0, 0, 0
        elif role in ROLES[:3]:
            optimizer = role == "insight" and not (frozen_comments or fixed_topic)
            if phase == "refinement":
                required = args.max_paragraphs * (2 + int(optimizer))
            else:
                reflections = (getattr(args, "insight_max_reflections", 3) if role == "insight"
                               else min(args.max_reflections, 2))
                required = 1 + args.max_paragraphs * (
                    1 + int(not fixed_topic) + 2 * reflections + int(optimizer) * (1 + reflections))
            recovery = 1
        else:
            required, recovery = (2, 0) if role == "report" else (1, 0)
        plan[suffix] = {"limit": limit, "required_calls": required,
                        "basis": "declared_fresh_research_depth",
                        "minimum_recovery_calls": recovery, "recovery_capacity": limit - required}
    return plan


def validate_model_request_plan(plan):
    for stage, row in plan.items():
        if row["limit"] < row["required_calls"] + row["minimum_recovery_calls"]:
            raise ValueError(f"{stage} model request limit {row['limit']} cannot cover "
                             f"{row['required_calls']} required calls and "
                             f"{row['minimum_recovery_calls']} recovery calls; "
                             "redistribute the existing workflow budget before starting")


def make_tasks(args, topic):
    tasks = []
    comprehensive = args.mode == "live"
    for suffix, role, phase in task_stages(args):
        dependency_tasks = {}
        if phase == "refinement":
            dependency_tasks = {role: f"{args.workflow_id}-{role}", "forum": f"{args.workflow_id}-forum"}
        if role == "forum":
            tail = "-feedback" if phase == "final" else ""
            dependency_tasks = {name: f"{args.workflow_id}-{name}{tail}" for name in ROLES[:3]}
        elif role == "report":
            tail = "-feedback" if comprehensive else ""
            dependency_tasks = {name: f"{args.workflow_id}-{name}{tail}" for name in ROLES[:3]}
            dependency_tasks["forum"] = f"{args.workflow_id}-forum" + ("-final" if comprehensive else "")
        limit = args.model_request_limits[ROLES.index(role)]
        if phase == "refinement":
            limit = getattr(args, "refinement_request_limits", REFINEMENT_REQUEST_LIMITS)[ROLES.index(role)]
        goal = {"topic": topic, "max_paragraphs": args.max_paragraphs,
                "max_reflections": args.max_reflections,
                "insight_max_reflections": getattr(args, "insight_max_reflections", 3),
                "model_timeout_seconds": 300 if comprehensive and role == "report" else 120,
                "max_model_requests": limit if comprehensive else 0,
                "max_output_tokens": (getattr(args, "report_max_output_tokens", 32000)
                                      if comprehensive and role == "report" else args.max_output_tokens),
                "max_content_length": getattr(args, "max_content_length", 60000),
                "flow_variant": "comprehensive" if comprehensive else "replay-five-role",
                "research_phase": phase if role in ROLES[:3] else "initial",
                "discussion_round": 2 if phase == "final" else 1,
                "dependency_tasks": dependency_tasks}
        goal["fixed_topic_search"] = args.fixed_topic_search
        tasks.append({"id": f"{args.workflow_id}-{suffix}", "workflow_id": args.workflow_id,
                      "agent_id": f"bettafish-{role}", "goal": json.dumps(goal, ensure_ascii=False),
                      "dependencies": list(dependency_tasks.values()), "priority": 1,
                      "resources": resources(role), "max_attempts": 1,
                      "timeout_ms": args.task_timeout * 1000,
                      "idempotency_key": f"{args.workflow_id}-{suffix}"})
    return {"tasks": tasks}


def collect_usage(root):
    rows = [json.loads(p.read_text(encoding="utf-8")) for p in root.glob("*/model-usage/*.json")]
    budgets = [json.loads(p.read_text(encoding="utf-8")) for p in root.glob("*/model-budget.json")]
    provided = [row for row in rows if row.get("usage_present")]
    return {"provider_attempts": sum(row.get("attempts", 0) for row in budgets),
            "logged_calls": len(rows), "calls_with_reported_usage": len(provided),
            "calls_missing_usage": len(rows) - len(provided),
            "reported_total_tokens": sum(row.get("total_tokens") or 0 for row in provided) if provided else None,
            "failed_calls": sum(row.get("status") == "error" for row in rows),
            "failed_or_truncated_attempts": sum(row.get("failed_attempts", 0) for row in budgets),
            "usage_is_provider_reported": True, "cost_calculated": False}


def concurrency_evidence(pages):
    changes, intervals = [], []
    for page in pages:
        for event in page.get("events", []):
            lifecycle = event.get("payload", {}).get("lifecycle", {})
            begin, end = lifecycle.get("process_started_at"), lifecycle.get("wait_completed_at")
            if begin and end:
                start = datetime.fromisoformat(begin.replace("Z", "+00:00")).timestamp()
                finish = datetime.fromisoformat(end.replace("Z", "+00:00")).timestamp()
                if finish > start:
                    changes += [(start, 1), (finish, -1)]
                    intervals.append({"task_id": event["task_id"], "started_at": begin, "finished_at": end})
    count, peak = 0, 0
    for _, delta in sorted(changes):
        count += delta
        peak = max(peak, count)
    return {"peak_overlapping_process_intervals": peak, "intervals": intervals,
            "source": "Runtime terminal-event lifecycle timestamps", "performance_comparison": False}


def run_workflow(args):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,119}", args.workflow_id):
        raise ValueError("Workflow id must be an AegisRT identifier")
    check_cancellation(args)
    request_plan = model_request_plan(args)
    validate_model_request_plan(request_plan)
    root = Path(args.run_dir).resolve() / args.workflow_id
    control = root / ".control"
    control.mkdir(parents=True, exist_ok=True)
    manifest = json.loads((Path(args.input_dir) / "manifest.json").read_text(encoding="utf-8"))
    topic = args.topic or manifest["topic"]
    if topic != manifest["topic"]:
        raise ValueError("Topic differs from frozen sample")
    registry = make_registry(args)
    registry_path = control / "agents.json"
    if registry_path.exists() and json.loads(registry_path.read_text(encoding="utf-8")) != registry:
        raise ValueError("Registry changed for a persisted run; choose a new workflow id")
    save_json(registry_path, registry)
    token_path = control / "auth.token"
    if not token_path.exists():
        token_path.write_text(secrets.token_urlsafe(32), encoding="ascii")
    if os.name != "nt":
        token_path.chmod(0o600)
    token = token_path.read_text(encoding="ascii").strip()
    port = args.port or unused_port()
    command = [str(Path(args.aegisrt).resolve()), "-listen", f"127.0.0.1:{port}",
               "-agents-file", str(registry_path), "-auth-token-file", str(token_path),
               "-state-file", str(control / "runtime.state"), "-max-workers", "3",
               "-poll-interval", "50ms", "-cpu-milli", "4000", "-memory-mib", "4096",
               "-llm-slots", "3", "-token-budget", "64000",
               "-context-counter", "unicode-segment-v1"]
    if args.cgroup_root:
        command += ["-cgroup-root", args.cgroup_root]
    client = Client(port, token)
    tasks = make_tasks(args, topic)
    save_json(root / "submitted-tasks.json", tasks)
    started = time.monotonic()
    summary = {"workflow_id": args.workflow_id, "mode": args.mode,
               "flow_variant": "comprehensive-discussion-feedback" if args.mode == "live" else "replay-five-role", "started_at": utc_now(),
               "input_manifest_sha256": digest(Path(args.input_dir) / "manifest.json"),
               "status": "starting", "max_concurrent_research_tasks": 3,
               "model_request_budget": sum(json.loads(task["goal"])["max_model_requests"] for task in tasks["tasks"]),
               "model_request_plan": request_plan,
               "task_count": len(tasks["tasks"]), "discussion_feedback_rounds": 1 if args.mode == "live" else 0,
               "full_original_workflow": False, "performance_comparison": False,
               "actual_resource_enforcement": bool(args.cgroup_root)}
    if args.resume_dir:
        summary["resume_manifest_sha256"] = digest(Path(args.resume_dir) / "manifest.json")
    proc = None
    event_pages = []
    last_statuses = None
    after_id, stream_id = "0", None
    with (control / "daemon.log").open("ab") as daemon_log:
        try:
            proc = subprocess.Popen(command, stdout=daemon_log, stderr=daemon_log,
                                    cwd=root, creationflags=0x08000000 if os.name == "nt" else 0)
            for _ in range(150):
                check_cancellation(args)
                if proc.poll() is not None:
                    raise RuntimeError("Runtime exited before readiness; inspect private daemon.log")
                try:
                    if client.call("/readyz").get("status") == "ready":
                        break
                except (URLError, RuntimeError, TimeoutError):
                    pass
                time.sleep(0.2)
            else:
                raise RuntimeError("Runtime readiness timeout")
            client.call("/v1/task-graphs/patches", tasks)
            summary["status"] = "running"
            while time.monotonic() - started < args.wall_timeout:
                snapshot = client.call("/v1/tasks")
                statuses = {x["task"]["id"]: x["status"] for x in snapshot["tasks"]}
                save_json(root / "task-snapshot.json", snapshot)
                if statuses != last_statuses:
                    print(json.dumps({"workflow_id": args.workflow_id, "tasks": statuses}, ensure_ascii=False), flush=True)
                    last_statuses = statuses
                query = f"/v1/events?after_id={after_id}&limit=256"
                if stream_id:
                    query += f"&stream_id={stream_id}"
                page = client.call(query)
                if page.get("events"):
                    event_pages.append(page)
                after_id = page.get("next_after_id", after_id)
                stream_id = page.get("stream_id", stream_id)
                if any(value in ("FAILED", "CANCELED") for value in statuses.values()):
                    raise RuntimeError("A BettaFish task failed; stopping dependent work")
                if len(statuses) == len(tasks["tasks"]) and all(value == "SUCCEEDED" for value in statuses.values()):
                    break
                check_cancellation(args)
                time.sleep(1)
            else:
                raise TimeoutError("Workflow exceeded the wall-clock budget")
            summary["runtime"] = client.call("/v1/runtime")
            summary["output_summaries"] = {
                suffix: client.call(f"/v1/tasks/{args.workflow_id}-{suffix}/output-summary")
                for suffix, _role, _phase in task_stages(args)}
            # Check the current report's files and every dependency again on the coordinator side.
            result_path = root / f"{args.workflow_id}-report" / "result.json"
            report_manifest = json.loads(result_path.read_text(encoding="utf-8"))
            for item in report_manifest["files"].values():
                if digest(item["path"]) != item["sha256"]:
                    raise ValueError("Final artifact changed after publication")
            summary["report"] = report_manifest
            summary["status"] = "completed"
            return summary
        except BaseException as exc:
            summary.update(status="error", error_type=type(exc).__name__, error=str(exc))
            raise
        finally:
            cleanup = {"cancellation_requested": [], "snapshot_refreshed": False,
                       "tasks_terminal": False, "events_refreshed": False, "error_types": [],
                       "deferred_interrupts": 0, "daemon_stopped": None, "daemon_kill_fallback": False}
            summary["cleanup"] = cleanup
            with defer_cleanup_interrupts(cleanup):
                try:
                    if proc is not None and proc.poll() is None:
                        try:
                            snapshot = client.call("/v1/tasks")
                            for row in snapshot.get("tasks", []):
                                if row["status"] not in TERMINAL:
                                    try:
                                        client.call(f"/v1/tasks/{row['task']['id']}/cancel", {})
                                        cleanup["cancellation_requested"].append(row["task"]["id"])
                                    except (Exception, KeyboardInterrupt) as exc:
                                        cleanup["error_types"].append(type(exc).__name__)
                            deadline = time.monotonic() + 15
                            while time.monotonic() < deadline:
                                snapshot = client.call("/v1/tasks")
                                rows = snapshot.get("tasks", [])
                                save_json(root / "task-snapshot.json", snapshot)
                                cleanup["snapshot_refreshed"] = True
                                cleanup["final_task_statuses"] = {row["task"]["id"]: row["status"] for row in rows}
                                if all(x["status"] in TERMINAL for x in rows):
                                    cleanup["tasks_terminal"] = True
                                    break
                                time.sleep(0.2)
                        except (Exception, KeyboardInterrupt) as exc:
                            cleanup["error_types"].append(type(exc).__name__)
                        # Capture available events before process-local history is lost.
                        try:
                            empty_reads = 0
                            for _ in range(20):
                                query = f"/v1/events?after_id={after_id}&limit=256"
                                if stream_id:
                                    query += f"&stream_id={stream_id}"
                                page = client.call(query)
                                cleanup["events_refreshed"] = True
                                if page.get("events"):
                                    event_pages.append(page)
                                    empty_reads = 0
                                else:
                                    empty_reads += 1
                                after_id = page.get("next_after_id", after_id)
                                stream_id = page.get("stream_id", stream_id)
                                if not page.get("has_more"):
                                    if empty_reads >= 2:
                                        break
                                    time.sleep(0.05)
                            cleanup["event_history_drained"] = not page.get("has_more", False)
                        except (Exception, KeyboardInterrupt) as exc:
                            cleanup["error_types"].append(type(exc).__name__)
                finally:
                    try:
                        stop_owned_daemon(proc, cleanup)
                    finally:
                        summary.update(ended_at=utc_now(), elapsed_seconds=round(time.monotonic() - started, 3))
                        summary["model_usage"] = collect_usage(root)
                        summary["concurrency"] = concurrency_evidence(event_pages)
                        save_json(root / "events.json", {"pages": event_pages, "retention": "captured process_memory events"})
                        save_json(root / "summary.json", summary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--freeze-from", help="Prepare a relocatable, verified snapshot from the existing reproduction")
    parser.add_argument("--input-dir", required=True)
    parser.add_argument("--aegisrt")
    parser.add_argument("--python", default=sys.executable)
    parser.add_argument("--source-dir")
    parser.add_argument("--env-file")
    parser.add_argument("--run-dir")
    parser.add_argument("--workflow-id", default="bettafish-" + datetime.now().strftime("%Y%m%d-%H%M%S"))
    parser.add_argument("--mode", choices=("replay", "live"), default="replay")
    parser.add_argument("--topic")
    parser.add_argument("--max-paragraphs", type=int, default=5)
    parser.add_argument("--max-reflections", type=int, default=2)
    parser.add_argument("--insight-max-reflections", type=int, default=3)
    parser.add_argument("--model-request-limits", default=",".join(map(str, LIVE_REQUEST_LIMITS)), help="Initial query,media,insight; each forum; final report")
    parser.add_argument("--refinement-request-limits", default="20,20,32", help="Feedback research limits in query,media,insight order")
    parser.add_argument("--max-output-tokens", type=int, default=16000)
    parser.add_argument("--max-content-length", type=int, default=60000)
    parser.add_argument("--task-timeout", type=int, default=3600)
    parser.add_argument("--wall-timeout", type=int, default=7200)
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--cgroup-root")
    parser.add_argument("--resume-dir", help="An explicitly frozen checkpoint snapshot from an interrupted live run")
    parser.add_argument("--report-max-output-tokens", type=int, default=32000,
                        help="Report-only output allowance, including provider reasoning and complete review JSON")
    parser.add_argument("--cancel-file", help="Cancellation marker owned by the local application entry")
    parser.add_argument("--fixed-topic-search", action="store_true", help="Use the supplied topic and frozen comment identities instead of generating search keywords")
    args = parser.parse_args()
    try:
        args.model_request_limits = [int(value) for value in args.model_request_limits.split(",")]
        if len(args.model_request_limits) != len(ROLES) or any(not 0 <= limit <= (64 if role in ROLES[:3] else 5 if role == "report" else 1) for role, limit in zip(ROLES, args.model_request_limits)):
            raise ValueError
        args.refinement_request_limits = [int(value) for value in args.refinement_request_limits.split(",")]
        if len(args.refinement_request_limits) != 3 or any(not 0 <= limit <= 64 for limit in args.refinement_request_limits):
            raise ValueError
    except ValueError:
        parser.error("Request limits require five values within 64,64,64,1,5 and three refinement values within 64")
    if not 128 <= args.max_output_tokens <= 32000:
        parser.error("Output token limit must be between 128 and 32000, including provider reasoning tokens")
    if not 128 <= args.report_max_output_tokens <= 32000:
        parser.error("Report output token limit must be between 128 and 32000")
    if args.freeze_from:
        print(json.dumps(freeze_inputs(args.freeze_from, args.input_dir), ensure_ascii=False))
        return
    for name in ("aegisrt", "source_dir", "run_dir"):
        if not getattr(args, name):
            parser.error(f"--{name.replace('_', '-')} is required for execution")
    if not 1 <= args.max_paragraphs <= 5 or not 0 <= args.max_reflections <= 2 or not 0 <= args.insight_max_reflections <= 3:
        parser.error("Research supports one to five paragraphs, up to two reflections and up to three Insight reflections")
    if not 1000 <= args.max_content_length <= 180000:
        parser.error("Content budget must be between 1000 and 180000 characters")
    if not 1 <= args.task_timeout <= args.wall_timeout <= 14400:
        parser.error("Timeout budgets must be positive and at most 14400 seconds")
    try:
        result = run_workflow(args)
        print(json.dumps({"workflow_id": result["workflow_id"], "status": result["status"],
                          "mode": result["mode"]}, ensure_ascii=False))
    except Exception as exc:
        print(f"BettaFish integration failed: {type(exc).__name__}: {exc}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
