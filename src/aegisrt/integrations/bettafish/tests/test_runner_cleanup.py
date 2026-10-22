"""Exercise coordinator cleanup without launching Runtime or business work."""

import argparse
import copy
import importlib.util
import json
from pathlib import Path
import signal
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("bettafish_runner_cleanup", ROOT / "runner.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class ProcessDouble:
    def __init__(self, *args, **kwargs):
        self.returncode = None
        self.terminate_calls = 0
        self.kill_calls = 0
        self.wait_timeouts = []
        self.interrupt_first_wait = False
        self.expire_first_wait = False

    def poll(self):
        return self.returncode

    def terminate(self):
        self.terminate_calls += 1

    def kill(self):
        self.kill_calls += 1
        self.returncode = -9

    def wait(self, timeout=None):
        self.wait_timeouts.append(timeout)
        if len(self.wait_timeouts) == 1:
            if self.interrupt_first_wait:
                signal.getsignal(signal.SIGINT)(signal.SIGINT, None)
            if self.expire_first_wait:
                raise runner.subprocess.TimeoutExpired("owned-demo-daemon", timeout)
        if self.returncode is None:
            self.returncode = 0
        return self.returncode


class ControlDouble:
    def __init__(self, workflow):
        self.workflow = workflow
        states = ("FAILED", "RUNNING", "RUNNING", "WAITING_DEPENDENCY", "WAITING_DEPENDENCY")
        self.rows = [{"task": {"id": f"{workflow}-{role}", "agent_id": f"bettafish-{role}"}, "status": status}
                     for role, status in zip(runner.ROLES, states)]
        self.canceled = []
        self.events = []

    def event(self, identifier, role, event_type):
        return {"id": identifier, "task_id": f"{self.workflow}-{role}", "type": event_type, "payload": {}}

    def call(self, path, body=None):
        if path == "/readyz":
            return {"status": "ready"}
        if path == "/v1/task-graphs/patches":
            return {"tasks": copy.deepcopy(self.rows)}
        if path == "/v1/tasks":
            return {"tasks": copy.deepcopy(self.rows)}
        if path.endswith("/cancel"):
            task_id = path.split("/")[-2]
            for row in self.rows:
                if row["task"]["id"] == task_id:
                    row["status"] = "CANCELED"
            self.canceled.append(task_id)
            return {"status": "CANCELED"}
        if path.startswith("/v1/events?"):
            cursor = path.split("after_id=")[1].split("&")[0]
            if cursor == "0":
                page = {"events": [self.event("1", "query", "run.failed")], "next_after_id": "1", "has_more": False}
            elif cursor == "1":
                page = {"events": [self.event("2", "media", "run.canceled")], "next_after_id": "2", "has_more": True}
            elif cursor == "2":
                page = {"events": [self.event("3", "insight", "run.canceled")], "next_after_id": "3", "has_more": False}
            else:
                page = {"events": [], "next_after_id": "3", "has_more": False}
            page["stream_id"] = "test-event-stream"
            self.events.append(copy.deepcopy(page))
            return page
        raise AssertionError(f"Unexpected control call: {path}")


class RunnerCleanupTests(unittest.TestCase):
    def exercise_cleanup(self, cancel_response_error=False, initial_interrupt=False,
                         interrupt_stage=None, interrupt_wait=False, direct_interrupt=False, marker_cancel=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            inputs = root / "inputs"
            inputs.mkdir()
            runner.save_json(inputs / "manifest.json", {"topic": "测试案例"})
            workflow = "cleanup-regression"
            task_root = root / "runs" / workflow
            original_adapter_state = {"status": "running", "role": "media"}
            runner.save_json(task_root / f"{workflow}-media" / "state.json", original_adapter_state)
            args = argparse.Namespace(workflow_id=workflow, run_dir=root / "runs", input_dir=inputs,
                                      source_dir=root, python=Path(__file__), aegisrt=root / "unused-runtime",
                                      env_file=None, mode="replay", topic=None, task_timeout=30, wall_timeout=60,
                                      max_paragraphs=1, max_reflections=0, model_request_limits=[6, 6, 6, 2, 2],
                                      port=12345, cgroup_root=None, resume_dir=None, fixed_topic_search=False, max_output_tokens=2000)
            control = ControlDouble(workflow)
            if marker_cancel:
                args.cancel_file = root / "cancel.requested"
                control.rows[0]["status"] = "RUNNING"
            process = ProcessDouble()
            process.interrupt_first_wait = interrupt_wait
            process.expire_first_wait = interrupt_wait
            original_call = control.call
            interrupted = {"initial": False, "cleanup": False}

            def response_error(path, body=None):
                if initial_interrupt and path == "/v1/tasks" and not interrupted["initial"]:
                    interrupted["initial"] = True
                    raise KeyboardInterrupt("initial run interruption")
                result = original_call(path, body)
                if marker_cancel and path == "/v1/tasks":
                    args.cancel_file.write_text("cancel")
                if path.endswith("-media/cancel"):
                    if cancel_response_error:
                        raise RuntimeError("Cancellation response was interrupted")
                repeat_here = (interrupt_stage == "cancel" and path.endswith("-media/cancel") or
                               interrupt_stage == "events" and path.startswith("/v1/events?"))
                if repeat_here and not interrupted["cleanup"]:
                    interrupted["cleanup"] = True
                    if direct_interrupt:
                        raise KeyboardInterrupt("direct cleanup interruption")
                    signal.getsignal(signal.SIGINT)(signal.SIGINT, None)
                return result

            control.call = response_error
            previous_handler = signal.getsignal(signal.SIGINT)
            with patch.object(runner, "Client", return_value=control), patch.object(runner.subprocess, "Popen", return_value=process):
                expected_error = runner.WorkflowCanceled if marker_cancel else KeyboardInterrupt if initial_interrupt else RuntimeError
                with self.assertRaises(expected_error):
                    runner.run_workflow(args)
            self.assertGreaterEqual(process.terminate_calls, 1, "owned daemon cleanup was skipped")
            self.assertIsNotNone(process.returncode, "owned daemon was not waited for")
            self.assertIs(signal.getsignal(signal.SIGINT), previous_handler)
            if interrupt_wait:
                self.assertEqual(process.kill_calls, 1)
                self.assertEqual(process.wait_timeouts, [15, 5])
            snapshot = json.loads((task_root / "task-snapshot.json").read_text(encoding="utf-8"))
            statuses = {row["task"]["agent_id"]: row["status"] for row in snapshot["tasks"]}
            self.assertEqual(statuses["bettafish-query"], "CANCELED" if marker_cancel else "FAILED")
            self.assertTrue(all(status == "CANCELED" for role, status in statuses.items() if role != "bettafish-query"))
            self.assertEqual(len(control.canceled), 5 if marker_cancel else 4)
            events = json.loads((task_root / "events.json").read_text(encoding="utf-8"))
            self.assertEqual([event["type"] for page in events["pages"] for event in page["events"]],
                             ["run.failed", "run.canceled", "run.canceled"])
            summary = json.loads((task_root / "summary.json").read_text(encoding="utf-8"))
            self.assertEqual(summary["status"], "error")
            self.assertEqual(summary["error_type"], "WorkflowCanceled" if marker_cancel else "KeyboardInterrupt" if initial_interrupt else "RuntimeError")
            self.assertTrue(summary["cleanup"]["tasks_terminal"])
            self.assertTrue(summary["cleanup"]["snapshot_refreshed"])
            self.assertTrue(summary["cleanup"]["events_refreshed"])
            self.assertTrue(summary["cleanup"]["daemon_stopped"])
            self.assertEqual(summary["cleanup"]["daemon_kill_fallback"], interrupt_wait)
            self.assertEqual(summary["cleanup"]["deferred_interrupts"],
                             int(bool((interrupt_stage and not direct_interrupt) or interrupt_wait)))
            expected_cleanup_errors = ["RuntimeError"] if cancel_response_error else []
            if interrupt_stage or interrupt_wait:
                expected_cleanup_errors.append("KeyboardInterrupt")
            self.assertEqual(summary["cleanup"]["error_types"], expected_cleanup_errors)
            self.assertEqual(json.loads((task_root / f"{workflow}-media" / "state.json").read_text()), original_adapter_state)

    def test_failed_workflow_captures_final_cancellations_and_events(self):
        self.exercise_cleanup()

    def test_entry_cancellation_marker_stops_all_owned_tasks(self):
        self.exercise_cleanup(marker_cancel=True)

    def test_one_cancel_response_error_does_not_skip_other_tasks(self):
        self.exercise_cleanup(cancel_response_error=True)

    def test_initial_interrupt_captures_final_cancellations_and_events(self):
        self.exercise_cleanup(initial_interrupt=True)

    def test_repeated_interrupt_during_cancellation_or_events_is_deferred(self):
        for stage in ("cancel", "events"):
            with self.subTest(stage=stage):
                self.exercise_cleanup(initial_interrupt=True, interrupt_stage=stage)

    def test_repeated_interrupt_while_waiting_keeps_kill_fallback(self):
        self.exercise_cleanup(initial_interrupt=True, interrupt_wait=True)

    def test_direct_cleanup_keyboard_interrupt_still_stops_owned_daemon(self):
        self.exercise_cleanup(initial_interrupt=True, interrupt_stage="cancel", direct_interrupt=True)


if __name__ == "__main__":
    unittest.main()
