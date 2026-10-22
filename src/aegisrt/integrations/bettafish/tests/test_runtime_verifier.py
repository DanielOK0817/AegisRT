"""Protocol and failure-evidence tests using real fixture subprocesses."""

import argparse
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
FIXTURE = ROOT / "tests" / "fixtures" / "runtime_agent.py"
spec = importlib.util.spec_from_file_location("bettafish_runtime_verifier", ROOT / "verify_runtime.py")
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


class RuntimeFixtureTests(unittest.TestCase):
    def execute(self, directory, goal, context="", attempt=1):
        request = {
            "task_id": "fixture-test", "workflow_id": "tests", "attempt_id": f"test-{attempt}",
            "attempt_number": attempt, "idempotency_key": "fixture-test",
            "goal": json.dumps(goal), "context": context,
        }
        return subprocess.run(
            [sys.executable, str(FIXTURE), "--trace-dir", str(directory)],
            input=json.dumps(request), capture_output=True, text=True, timeout=5,
        )

    def test_success_protocol_has_json_only_stdout(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.execute(directory, {"mode": "success", "content": "known evidence"})
            self.assertEqual(result.returncode, 0, result.stderr)
            document = json.loads(result.stdout)
            self.assertEqual(document["version"], "aegisrt.agent-output/v1")
            self.assertEqual(document["artifacts"][0]["content"], "known evidence")
            self.assertIn("fixture-test", result.stderr)

    def test_dependency_fixture_rejects_missing_content(self):
        with tempfile.TemporaryDirectory() as directory:
            goal = {"mode": "context", "expected": "saved output"}
            missing = self.execute(directory, goal)
            self.assertEqual(missing.returncode, 18)
            self.assertEqual(missing.stdout, "")
            present = self.execute(directory, goal, context="prefix\nsaved output\nsuffix")
            self.assertEqual(present.returncode, 0, present.stderr)

    def test_retry_attempt_skips_interrupted_delay(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.execute(directory, {"mode": "retry", "delay_seconds": 20}, attempt=2)
            self.assertEqual(result.returncode, 0, result.stderr)
            trace = json.loads((Path(directory) / "fixture-test.2.started.json").read_text())
            self.assertEqual(trace["attempt_number"], 2)

    def test_missing_runtime_still_produces_failed_evidence_without_token(self):
        with tempfile.TemporaryDirectory() as directory:
            options = argparse.Namespace(output_dir=Path(directory), aegisrt=Path(directory) / "missing",
                                         python=Path(sys.executable), cgroup_root=None)
            report, path = verifier.verify(options)
            self.assertEqual(report["status"], "failed")
            self.assertTrue(path.is_file())
            self.assertTrue(report["token_file_removed"])
            self.assertFalse((path.parent / "api.token").exists())
            self.assertFalse(report["real_bettafish_workflow"])
            self.assertEqual(report["model_calls"], 0)


if __name__ == "__main__":
    unittest.main()
