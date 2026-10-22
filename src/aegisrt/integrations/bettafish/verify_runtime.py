"""Verify real isolated AegisRT process behavior without paid model calls.

This fixture exercise is not a real BettaFish business-workflow validation.
Active-attempt crash recovery requires Linux cgroup isolation in AegisRT.
Without it, the script verifies the fail-closed guard and reports that limit.
"""

import argparse
import ctypes
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import signal
import socket
import subprocess
import sys
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.request import build_opener, ProxyHandler, Request


FIXTURE = Path(__file__).parent / "tests" / "fixtures" / "runtime_agent.py"


def write_json(path, document):
    path.write_text(json.dumps(document, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def wait_until(probe, description, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = probe()
        if result:
            return result
        time.sleep(0.025)
    raise TimeoutError(f"Timed out waiting for {description}")


def pid_alive(pid):
    if os.name == "nt":
        # os.kill(pid, 0) must not be used on Windows: use a read-only handle.
        from ctypes import wintypes
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        kernel.OpenProcess.restype = wintypes.HANDLE
        kernel.GetExitCodeProcess.argtypes = [wintypes.HANDLE, ctypes.POINTER(wintypes.DWORD)]
        kernel.CloseHandle.argtypes = [wintypes.HANDLE]
        handle = kernel.OpenProcess(0x1000, False, pid)
        if not handle:
            error = ctypes.get_last_error()
            if error == 5:
                raise OSError("Cannot inspect owned fixture process")
            return False
        try:
            exit_code = wintypes.DWORD()
            if not kernel.GetExitCodeProcess(handle, ctypes.byref(exit_code)):
                raise OSError("Cannot read owned fixture process status")
            return exit_code.value == 259
        finally:
            kernel.CloseHandle(handle)
    # A reparented Linux fixture may temporarily be a zombie: it has exited.
    stat_file = Path("/proc") / str(pid) / "stat"
    if stat_file.exists():
        try:
            if stat_file.read_text().rsplit(")", 1)[1].split()[0] == "Z":
                return False
        except FileNotFoundError:
            return False
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


class Daemon:
    def __init__(self, binary, directory, token, cgroup_root=None, start_cgroup=None):
        self.directory = directory
        self.token = token
        self.process = None
        self.epoch = 0
        self.start_cgroup = start_cgroup
        self.opener = build_opener(ProxyHandler({}))
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.address = f"127.0.0.1:{listener.getsockname()[1]}"
        self.base_url = "http://" + self.address
        self.command = [
            str(binary), "-listen", self.address,
            "-agents-file", str(directory / "agents.json"),
            "-auth-token-file", str(directory / "api.token"),
            "-state-file", str(directory / "runtime.state"),
            "-cpu-milli", "1000", "-memory-mib", "1024",
            "-token-budget", "20000", "-llm-slots", "0",
            "-max-workers", "3", "-poll-interval", "10ms",
            "-event-capacity", "128", "-shutdown-timeout", "3s",
        ]
        if cgroup_root:
            self.command += ["-cgroup-root", cgroup_root]
        self.logs = []

    def request(self, method, endpoint, body=None, expected=200, authenticated=True):
        headers = {}
        if authenticated:
            headers["Authorization"] = "Bearer " + self.token
        data = None
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = Request(self.base_url + endpoint, data=data, headers=headers, method=method)
        try:
            response = self.opener.open(request, timeout=2)
        except HTTPError as error:
            response = error
        with response:
            document = json.loads(response.read())
            statuses = expected if isinstance(expected, tuple) else (expected,)
            require(response.status in statuses,
                    f"{method} {endpoint} returned {response.status}, expected {expected}: {document}")
            return document

    def start(self, expect_blocked=False):
        self.epoch += 1
        log_path = self.directory / f"runtime-{self.epoch}.log"
        self.logs.append(log_path)
        command = list(self.command)
        if self.start_cgroup and self.epoch > 1:
            # The original root now distributes controllers to child cgroups.
            # Restart from its manager leaf and reuse that exact prepared root;
            # joining the non-leaf root violates cgroup v2's process rule.
            command[command.index("-cgroup-root") + 1] = str(self.start_cgroup)
        with log_path.open("wb") as log:
            self.process = subprocess.Popen(
                command, stdout=log, stderr=log,
                creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0,
                **({"preexec_fn": self.join_start_cgroup} if self.start_cgroup else {}),
            )
        if expect_blocked:
            self.process.wait(timeout=10)
            diagnostic = log_path.read_text(encoding="utf-8", errors="replace")
            require(self.process.returncode != 0, "Active recovery without isolation was unexpectedly accepted")
            require("active-attempt recovery requires cgroup isolation" in diagnostic,
                    "Restart failed for a different reason than the documented isolation guard")
            return diagnostic

        def ready():
            if self.process.poll() is not None:
                diagnostic = log_path.read_text(encoding="utf-8", errors="replace")
                raise RuntimeError(f"Runtime exited before ready: {diagnostic}")
            try:
                value = self.request("GET", "/readyz", expected=(200, 503), authenticated=False)
                return value if value.get("status") == "ready" else False
            except (URLError, TimeoutError, ConnectionError):
                return False

        wait_until(ready, "Runtime readiness")

    def join_start_cgroup(self):
        # This verifier is single-threaded. Only its freshly forked child moves.
        destination = Path(self.start_cgroup)
        if self.epoch > 1:
            destination /= "manager"
        (destination / "cgroup.procs").write_text(str(os.getpid()), encoding="ascii")

    def kill(self):
        require(self.process is not None and self.process.poll() is None, "Runtime was not alive before forced stop")
        pid = self.process.pid
        self.process.kill()
        self.process.wait(timeout=5)
        return {"pid": pid, "exit_code": self.process.returncode, "method": "Popen.kill"}

    def stop(self):
        if self.process is None or self.process.poll() is not None:
            return
        if os.name == "nt":
            self.process.terminate()
        else:
            self.process.send_signal(signal.SIGTERM)
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)

    def tasks(self):
        return {item["task"]["id"]: item for item in self.request("GET", "/v1/tasks")["tasks"]}

    def wait_tasks(self, wanted):
        def complete():
            tasks = self.tasks()
            return tasks if all(tasks.get(task_id, {}).get("status") == status
                                for task_id, status in wanted.items()) else False
        return wait_until(complete, f"task states {wanted}")

    def submit(self, tasks):
        return self.request("POST", "/v1/task-graphs/patches", {"tasks": tasks}, expected=201)


def task(task_id, mode="success", attempts=1, timeout_ms=10000, dependencies=None, **goal):
    return {
        "id": task_id, "workflow_id": "runtime-fixture", "agent_id": "fixture-agent",
        "goal": json.dumps({"mode": mode, **goal}), "priority": 1,
        "resources": {"cpu_milli": 250, "memory_mib": 128, "token_budget": 4096},
        "max_attempts": attempts, "timeout_ms": timeout_ms,
        "idempotency_key": "fixture-" + task_id,
        **({"dependencies": dependencies} if dependencies else {}),
    }


def read_trace(directory, task_id, attempt, phase):
    path = directory / "trace" / f"{task_id}.{attempt}.{phase}.json"
    return json.loads(path.read_text(encoding="utf-8")) if path.exists() else None


def idle(daemon):
    def released():
        view = daemon.request("GET", "/v1/runtime", authenticated=False)
        allocated = view["stats"]["allocated"]
        return view if all(value == 0 for value in allocated.values() if isinstance(value, (int, float))) else False
    return wait_until(released, "released resource allocations")


def verify(options):
    options.output_dir.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="runtime-fixture-", dir=options.output_dir)).resolve()
    trace_dir = directory / "trace"
    trace_dir.mkdir()
    token = secrets.token_hex(32)
    token_path = directory / "api.token"
    token_path.write_text(token + "\n", encoding="ascii")
    token_path.chmod(0o600)
    binary = options.aegisrt.resolve()
    python = options.python.resolve()
    registry = {"agents": [{
        "id": "fixture-agent", "name": "Controlled Runtime Fixture", "runtime": "process",
        "entrypoint": str(python), "args": [str(FIXTURE.resolve()), "--trace-dir", str(trace_dir)],
        "resources": {"cpu_milli": 250, "memory_mib": 128, "token_budget": 4096},
        "max_concurrency": 3, "execution_timeout": "30s",
    }]}
    write_json(directory / "agents.json", registry)
    daemon = Daemon(binary, directory, token, options.cgroup_root, getattr(options, "start_cgroup", None))
    report = {
        "version": "aegisrt.bettafish.runtime-verification/v1",
        "started_at": datetime.now(timezone.utc).isoformat(),
        "kind": "controlled_process_fixture", "real_bettafish_workflow": False,
        "model_calls": 0, "platform": platform.platform(), "python": str(python),
        "runtime_binary": str(binary), "runtime_sha256": None,
        "isolated_directory": str(directory), "cgroup_root": options.cgroup_root,
        "start_cgroup": getattr(options, "start_cgroup", None),
        "checks": [], "limitations": [
            "Controlled fixture evidence does not validate real BettaFish analysis, forum, or reporting.",
            "No original-application baseline or performance comparison is produced.",
            "Runtime events are process-local and are expected to change stream after restart.",
        ],
    }

    def passed(name, **evidence):
        report["checks"].append({"name": name, "status": "passed", **evidence})
        print(f"PASS {name}", flush=True)

    try:
        require(binary.is_file(), f"Runtime binary does not exist: {binary}")
        require(python.is_file(), f"Python interpreter does not exist: {python}")
        report["runtime_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
        daemon.start()
        daemon.request("GET", "/v1/tasks", expected=401, authenticated=False)
        passed("isolated_authenticated_runtime", runtime_pid=daemon.process.pid, address=daemon.address)

        evidence_content = "fixture-completed-evidence-" + secrets.token_hex(12)
        producer = task("completed-producer", content=evidence_content)
        concurrent = [task(f"parallel-{i}", delay_seconds=1.0) for i in range(3)]
        daemon.submit([producer, *concurrent])
        daemon.wait_tasks({item["id"]: "SUCCEEDED" for item in [producer, *concurrent]})
        intervals = []
        for item in concurrent:
            started = read_trace(directory, item["id"], 1, "started")
            exited = read_trace(directory, item["id"], 1, "exited")
            require(started and exited, "Missing real process concurrency trace")
            intervals.append({"task_id": item["id"], "pid": started["pid"],
                              "start_ns": started["monotonic_ns"], "end_ns": exited["monotonic_ns"]})
        points = [(item["start_ns"], 1) for item in intervals] + [(item["end_ns"], -1) for item in intervals]
        active = maximum = 0
        for _, change in sorted(points):
            active += change
            maximum = max(maximum, active)
        require(maximum >= 2, f"Real fixture execution was serial: peak={maximum}")
        require(len({item["pid"] for item in intervals}) == len(intervals), "Fixture executions reused a process")
        passed("real_process_concurrency", peak_concurrent=maximum, intervals=intervals)

        daemon.submit([task("timeout-task", mode="sleep", delay_seconds=20, timeout_ms=600),
                       task("exit-task", mode="fail")])
        daemon.wait_tasks({"timeout-task": "FAILED", "exit-task": "FAILED"})
        timeout_trace = read_trace(directory, "timeout-task", 1, "started")
        require(timeout_trace, "Timeout task never started a real process")
        wait_until(lambda: not pid_alive(timeout_trace["pid"]), "timed-out process exit", timeout=5)
        events_before = wait_until(
            lambda: (value if any(event["task_id"] == "timeout-task" for event in value["events"])
                     and any(event["task_id"] == "exit-task" for event in value["events"]) else False)
            if (value := daemon.request("GET", "/v1/events?after_id=0&limit=128")) else False,
            "failure event publication",
        )
        write_json(directory / "events-before-restart.json", events_before)
        timeout_event = next(event for event in events_before["events"] if event["task_id"] == "timeout-task")
        exit_event = next(event for event in events_before["events"] if event["task_id"] == "exit-task")
        require(timeout_event["type"] == "run.timed_out" and timeout_event["payload"]["timed_out"], "Timeout event was misclassified")
        require(exit_event["type"] == "run.failed" and exit_event["payload"]["exit_code"] == 17, "Abnormal exit was misclassified")
        passed("task_timeout", task_status="FAILED", event=timeout_event, process_alive=False)
        passed("abnormal_process_exit", task_status="FAILED", event=exit_event)
        before = idle(daemon)
        summary_before = daemon.request("GET", "/v1/tasks/completed-producer/output-summary")
        snapshot_before = daemon.tasks()
        write_json(directory / "tasks-before-restart.json", snapshot_before)
        forced_stop = daemon.kill()
        daemon.start()
        snapshot_after = daemon.tasks()
        require(snapshot_after == snapshot_before, "Terminal task snapshot changed after forced Runtime stop")
        summary_after = daemon.request("GET", "/v1/tasks/completed-producer/output-summary")
        require(summary_before == summary_after == {"record_count": 1, "artifact_count": 1}, "Completed output summary was lost")
        stream_after = daemon.request("GET", "/v1/events?after_id=0&limit=128")["stream_id"]
        require(stream_after != events_before["stream_id"], "Event stream identity was unexpectedly reused")
        daemon.submit([producer])
        require(len(list(trace_dir.glob("completed-producer.*.started.json"))) == 1, "Completed producer was replayed")
        daemon.submit([task("read-restored-artifact", mode="context", expected=evidence_content,
                            dependencies=["completed-producer"])])
        daemon.wait_tasks({"read-restored-artifact": "SUCCEEDED"})
        passed("terminal_tasks_and_artifact_survive_forced_restart", forced_stop=forced_stop,
               summary=summary_after, original_producer_executions=1,
               restored_content_verified_by_real_dependency_agent=True,
               old_event_stream=events_before["stream_id"], new_event_stream=stream_after)
        passed("failed_tasks_release_resources", allocated=before["stats"]["allocated"])

        crash_tasks = [task("crash-retry", mode="retry", attempts=2, delay_seconds=20, timeout_ms=25000),
                       task("crash-no-retry", mode="sleep", delay_seconds=20, timeout_ms=25000),
                       task("crash-dependent", dependencies=["crash-retry"])]
        daemon.submit(crash_tasks)
        daemon.wait_tasks({"crash-retry": "RUNNING", "crash-no-retry": "RUNNING"})
        old_traces = [wait_until(lambda task_id=item["id"]: read_trace(directory, task_id, 1, "started"),
                                "crash fixture start") for item in crash_tasks[:2]]
        write_json(directory / "tasks-at-active-crash.json", daemon.tasks())
        forced_active_stop = daemon.kill()
        if options.cgroup_root:
            daemon.start()
            daemon.wait_tasks({"crash-retry": "SUCCEEDED", "crash-no-retry": "FAILED", "crash-dependent": "SUCCEEDED"})
            for trace in old_traces:
                wait_until(lambda pid=trace["pid"]: not pid_alive(pid), "cgroup cleanup of prior attempt", timeout=5)
            retry_trace = read_trace(directory, "crash-retry", 2, "started")
            require(retry_trace, "Interrupted retry did not create attempt 2")
            require(len(list(trace_dir.glob("crash-no-retry.*.started.json"))) == 1, "Exhausted task was re-executed")
            passed("active_tasks_recover_after_forced_restart", forced_stop=forced_active_stop,
                   retry_attempt=retry_trace["attempt_number"], prior_pids_gone=True,
                   exhausted_task_status="FAILED", dependency_status="SUCCEEDED")
            idle(daemon)
            write_json(directory / "tasks-final.json", daemon.tasks())
            write_json(directory / "events-after-restart.json", daemon.request("GET", "/v1/events?after_id=0&limit=128"))
        else:
            # Cleanup is the fixture harness's responsibility here, not proof
            # of Runtime cgroup cleanup or active recovery on this platform.
            (trace_dir / "harness-cleanup").touch()
            for trace in old_traces:
                wait_until(lambda pid=trace["pid"]: not pid_alive(pid), "owned fixture cleanup", timeout=5)
            daemon.start(expect_blocked=True)
            passed("active_restart_fails_closed_without_cgroup", forced_stop=forced_active_stop,
                   harness_cleaned_owned_fixture_processes=True)
            report["checks"].append({"name": "active_tasks_recover_after_forced_restart", "status": "unsupported",
                                      "reason": "AegisRT requires Linux cgroup isolation for active-attempt recovery"})
            report["limitations"].append("Active-attempt recovery is unverified; rerun on openEuler with --cgroup-root.")
        report["status"] = "passed" if options.cgroup_root else "passed_with_limitations"
    except Exception as error:
        report["status"] = "failed"
        report["error"] = str(error).replace(token, "[REDACTED]")
        print("FAIL " + report["error"], file=sys.stderr, flush=True)
    finally:
        (trace_dir / "harness-cleanup").touch()
        daemon.stop()
        for trace_file in trace_dir.glob("*.started.json"):
            trace = json.loads(trace_file.read_text(encoding="utf-8"))
            if pid_alive(trace["pid"]):
                try:
                    wait_until(lambda pid=trace["pid"]: not pid_alive(pid), "owned fixture final cleanup", timeout=5)
                except TimeoutError:
                    report["status"] = "failed"
                    report["cleanup_error"] = "An owned fixture process did not exit"
        for log_path in daemon.logs:
            log = log_path.read_text(encoding="utf-8", errors="replace")
            if token in log:
                log_path.write_text(log.replace(token, "[REDACTED]"), encoding="utf-8")
                report["status"] = "failed"
                report["credential_leak_detected"] = True
        token_path.unlink(missing_ok=True)
        report["token_file_removed"] = not token_path.exists()
        report["finished_at"] = datetime.now(timezone.utc).isoformat()
        report_path = directory / "verification.json"
        write_json(report_path, report)
        print(f"REPORT {report_path}", flush=True)
    return report, report_path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    repo_root = Path(__file__).resolve().parents[2]
    parser.add_argument("--aegisrt", type=Path, default=repo_root / "dist" / "bettafish" / ("aegisrt.exe" if os.name == "nt" else "aegisrt"))
    parser.add_argument("--python", type=Path, default=Path(sys.executable))
    parser.add_argument("--output-dir", type=Path, default=repo_root / "evidence" / "bettafish-runtime")
    parser.add_argument("--cgroup-root", help="Linux delegated cgroup root, or auto inside a delegated systemd service")
    parser.add_argument("--start-cgroup", help="Linux delegated empty cgroup for each new Runtime process")
    options = parser.parse_args()
    if options.start_cgroup and (sys.platform != "linux" or options.cgroup_root != "auto"):
        parser.error("--start-cgroup requires Linux and --cgroup-root auto")
    report, _ = verify(options)
    return 1 if report["status"] == "failed" else 0


if __name__ == "__main__":
    raise SystemExit(main())
