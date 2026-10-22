"""Report generation needs a bounded allowance for a complete JSON response."""
import json
import unittest
from test_full_workflow import adapter, runner, options


class ReportTimeoutPolicyTests(unittest.TestCase):
    def test_nine_task_report_has_longer_deadline_without_more_requests(self):
        args = options(".")
        args.task_timeout = 1800
        args.model_request_limits = [11, 11, 11, 1, 5]
        args.refinement_request_limits = [6, 6, 6]
        tasks = runner.make_tasks(args, args.topic)["tasks"]
        self.assertEqual(len(tasks), 9)
        self.assertEqual(sum(json.loads(t["goal"])["max_model_requests"] for t in tasks), 58)
        for task in tasks:
            role = task["agent_id"].removeprefix("bettafish-")
            expected = 300 if role == "report" else 120
            self.assertEqual(json.loads(task["goal"])["model_timeout_seconds"], expected)
        for task in tasks:
            role = task["agent_id"].removeprefix("bettafish-")
            self.assertEqual(adapter.validate_request({**task, "task_id": task["id"]}, role=role)["model_timeout_seconds"],
                             json.loads(task["goal"])["model_timeout_seconds"])
        self.assertLess(300 * 5, args.task_timeout)

    def test_report_timeout_bound_does_not_expand_other_roles(self):
        request = {"workflow_id": "case", "task_id": "case-report", "goal": ""}
        for role in ("query", "media", "insight", "forum", "report"):
            cap = 300 if role == "report" else 120
            for value in (0, cap + 1, True, 120.5):
                request["goal"] = json.dumps({"topic": "test", "model_timeout_seconds": value})
                with self.subTest(role=role, value=value), self.assertRaises(adapter.AdapterError):
                    adapter.validate_request(request, role=role)

    def test_report_output_capacity_covers_reasoning_and_complete_review(self):
        args = options(".")
        args.max_output_tokens = 16000
        for task in runner.make_tasks(args, args.topic)["tasks"]:
            role = task["agent_id"].removeprefix("bettafish-")
            payload = adapter.validate_request({**task, "task_id": task["id"]}, role=role)
            self.assertEqual(payload["max_output_tokens"], 32000 if role == "report" else 16000)
        args.report_max_output_tokens = 24000
        report = runner.make_tasks(args, args.topic)["tasks"][-1]
        self.assertEqual(json.loads(report["goal"])["max_output_tokens"], 24000)


if __name__ == "__main__":
    unittest.main()
