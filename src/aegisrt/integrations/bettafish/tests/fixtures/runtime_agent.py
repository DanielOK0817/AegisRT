"""Deterministic process Agent; never imports BettaFish or calls a model."""

import argparse
import json
import os
from pathlib import Path
import re
import sys
import time


def save_trace(directory, task_id, attempt, phase, extra=None):
    document = {
        "task_id": task_id,
        "attempt_number": attempt,
        "phase": phase,
        "pid": os.getpid(),
        "monotonic_ns": time.monotonic_ns(),
        "wall_time_ns": time.time_ns(),
    }
    document.update(extra or {})
    destination = directory / f"{task_id}.{attempt}.{phase}.json"
    temporary = destination.with_suffix(".tmp")
    with temporary.open("w", encoding="utf-8") as handle:
        json.dump(document, handle)
        handle.flush()
        os.fsync(handle.fileno())
    temporary.replace(destination)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--trace-dir", type=Path, required=True)
    options = parser.parse_args()
    request = json.load(sys.stdin)
    task_id = request["task_id"]
    if not re.fullmatch(r"[a-z0-9-]+", task_id):
        raise ValueError("fixture task ID is invalid")
    attempt = request["attempt_number"]
    goal = json.loads(request["goal"])
    mode = goal["mode"]
    options.trace_dir.mkdir(parents=True, exist_ok=True)
    save_trace(options.trace_dir, task_id, attempt, "started", {"mode": mode})
    print(f"fixture {task_id} attempt {attempt}", file=sys.stderr)
    if mode == "fail":
        save_trace(options.trace_dir, task_id, attempt, "exited", {"exit_code": 17})
        return 17
    if mode == "context":
        if goal["expected"] not in request.get("context", ""):
            save_trace(options.trace_dir, task_id, attempt, "exited", {"exit_code": 18})
            return 18
    delay = goal.get("delay_seconds", 0)
    if mode == "retry" and attempt > 1:
        delay = 0
    deadline = time.monotonic() + delay
    while time.monotonic() < deadline:
        if (options.trace_dir / "harness-cleanup").exists():
            save_trace(options.trace_dir, task_id, attempt, "exited", {"exit_code": 19})
            return 19
        time.sleep(min(0.025, max(0, deadline - time.monotonic())))
    content = goal.get("content", f"fixture {task_id} attempt {attempt} complete")
    save_trace(options.trace_dir, task_id, attempt, "exited", {"exit_code": 0})
    json.dump(
        {"version": "aegisrt.agent-output/v1", "artifacts": [
            {"name": "fixture-result", "content": content, "visibility": "workflow"}
        ]},
        sys.stdout,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
