"""Counted offline reproduction of a real two-paragraph refinement failure."""
import argparse
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import types
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "budget_recovery_depth_fixture", Path(__file__).with_name("test_research_depth.py"))
depth = importlib.util.module_from_spec(spec)
spec.loader.exec_module(depth)
adapter = depth.adapter


class ResearchBudgetRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.fixture = depth.ResearchDepthTests(methodName="runTest")
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        usage_path = (Path(__file__).resolve().parents[4] / "reproductions" /
                      "bettafish-20261001" / "source" / "utils" / "llm_usage.py")
        usage_spec = importlib.util.spec_from_file_location("budget_recovery_usage_fixture", usage_path)
        self.usage = importlib.util.module_from_spec(usage_spec)
        usage_spec.loader.exec_module(self.usage)

    def assert_usage_counts(self, expected):
        """Bind production budget attempts to real recorder rows with mock usage."""
        rows = [adapter.read_json(path) for path in self.usage_dir.glob("*.json")]
        self.assertEqual(self.budget.data["attempts"], expected)
        self.assertEqual(self.budget.data["agents"], {"query": expected})
        self.assertEqual(len(self.calls), expected)
        self.assertEqual(len(rows), expected)
        self.assertEqual(len({row["call_id"] for row in rows}), expected)
        self.assertEqual({row["response_model"] for row in rows},
                         {"offline-" + str(index) for index in range(1, expected + 1)})
        for row in rows:
            self.assertEqual((row["run_id"], row["agent"], row["status"]),
                             ("depth-case-query-feedback", "query", "completed"))
            self.assertTrue(row["usage_present"] and row["token_sum_matches"])
            self.assertEqual((row["input_tokens"], row["output_tokens"], row["total_tokens"]),
                             (100, 20, 120))  # Explicitly simulated tokens; no provider was contacted.

    def run_two_paragraph_refinement(self, limit, *, repair_both=False, repair_decisions=False,
                                     format_repair=True, connection_failures=(), retry_transient=False, role="query", unchanged=False, always_unchanged=False):
        """Use production hooks and ModelBudget; only the provider is a mock."""
        fixture = self.fixture
        request, payload = fixture.payload(max_paragraphs=2, research_phase="refinement",
                                           max_model_requests=limit)
        imports = fixture.modules(role)
        agent_class = imports[role.capitalize() + "Engine"].DeepSearchAgent
        original_initialize = agent_class.__init__
        self.calls = []
        self.raw_summaries = []
        self.usage_dir = fixture.root / "model-usage"
        self.budget = adapter.ModelBudget(fixture.root / "model-budget.json", "same-input", limit, 120, 16000,
                                         retry_transient=retry_transient)

        def provider(_client, _role, **kwargs):
            kind, data = kwargs["node_kind"], kwargs["node_input"]
            self.calls.append({"kind": kind, "data": copy.deepcopy(data)})
            if len(self.calls) in connection_failures:
                raise type("APIConnectionError", (Exception,), {})("offline connection failed")
            if kind == "decision":
                value = {"search_query": "补搜" + data["title"], "search_tool": "basic_search_news",
                         "reasoning": "按主持疑点核查给定材料"}
                if repair_decisions and "format_correction" not in data:
                    value.pop("reasoning")
            else:
                value = {"paragraph_latest_state": "补搜材料仍需核查原始出处。" + data["title"] +
                         "来源：" + data["search_results"][0]["url"]}
                # This is valid JSON with the exact extra-field shape found in
                # the real run. The strict summary contract must reject it.
                if format_repair and "format_correction" not in data and (repair_both or data["title"] == "角度0"):
                    value["search_scope"] = copy.deepcopy(data["search_scope"])
                if unchanged and (always_unchanged or "feedback_correction" not in data):
                    value = {"paragraph_latest_state": data["paragraph_latest_state"]}
            raw = json.dumps(value, ensure_ascii=False)
            if kind == "summary":
                self.raw_summaries.append(raw)
            return types.SimpleNamespace(choices=[types.SimpleNamespace(
                finish_reason="stop", message=types.SimpleNamespace(content=raw))],
                model="offline-" + str(len(self.calls)),
                usage=types.SimpleNamespace(prompt_tokens=100, completion_tokens=20, total_tokens=120))

        client = types.SimpleNamespace(max_retries=0, base_url="https://offline.invalid/v1")
        client.chat = types.SimpleNamespace(completions=types.SimpleNamespace(
            create=lambda **kwargs: provider(client, role, **kwargs)))
        counted = self.budget.wrap(self.usage.recorded_completion)

        def initialize(agent, configuration):
            original_initialize(agent, configuration)
            for node, kind in ((agent.reflection_node, "decision"), (agent.reflection_summary_node, "summary")):
                node.process_output = lambda raw: raw

                def run(data, _node=node, _kind=kind, **kwargs):
                    response = counted(client, role,
                                       node_kind=_kind, node_input=data)
                    return _node.process_output(response.choices[0].message.content)

                node.run = run

        initial = fixture.initial_state()
        self.initial_before = copy.deepcopy(initial)
        arguments = argparse.Namespace(role=role, resume_seed=None, initial_research_state=initial,
                                       initial_research_sha256="a" * 64,
                                       forum_feedback="仅基于补搜原文核查初轮仍未解释的分歧。")
        self.output = fixture.root / (role + "-feedback")
        with patch("socket.socket", side_effect=AssertionError("offline regression attempted network access")), \
                patch.dict(adapter.os.environ, {"LLM_USAGE_DIR": str(self.usage_dir),
                                               "BETTAFISH_RUN_ID": "depth-case-query-feedback"}), \
                patch.object(agent_class, "__init__", initialize), \
                patch.object(adapter.importlib, "import_module", side_effect=lambda name: imports[name]):
            return adapter.research_live(arguments, request, payload, self.output)

    def test_unchanged_feedback_gets_one_counted_correction_per_paragraph(self):
        state, _ = self.run_two_paragraph_refinement(6, format_repair=False, retry_transient=True, unchanged=True)
        self.assert_usage_counts(6)
        self.assertTrue(all(p["feedback_consumed"] for p in state["research_audit"]["paragraphs"]))
        corrections = [r for r in self.calls if "feedback_correction" in r["data"]]
        self.assertEqual(len(corrections), 2)
        self.assertEqual(len(self.fixture.trace["queries"]), 2)

    def test_repeated_unchanged_feedback_still_fails(self):
        with self.assertRaisesRegex(adapter.AdapterError, "did not update"):
            self.run_two_paragraph_refinement(6, format_repair=False, retry_transient=True, unchanged=True, always_unchanged=True)
        self.assert_usage_counts(3)

    def test_unchanged_correction_preserves_next_paragraph_budget(self):
        with self.assertRaisesRegex(adapter.ModelCallStopped, "budget"):
            self.run_two_paragraph_refinement(4, format_repair=False, retry_transient=True, unchanged=True)
        self.assert_usage_counts(2)

    def test_four_request_budget_reproduces_failure_before_the_fifth_provider_call(self):
        with self.assertRaisesRegex(adapter.ModelCallStopped, "budget exhausted"):
            self.run_two_paragraph_refinement(4)
        self.assert_usage_counts(4)
        self.assertEqual([row["kind"] for row in self.calls], ["decision", "summary", "summary", "decision"])
        state = adapter.read_json(self.output / "checkpoint.json")["state"]
        self.assertFalse(state["is_completed"])
        self.assertTrue(state["paragraphs"][0]["research"]["is_completed"])
        self.assertFalse(state["paragraphs"][1]["research"]["is_completed"])
        self.assertEqual([row["validation"] for row in state["research_audit"]["summary_responses"]],
                         ["rejected", "accepted"])
        self.assertEqual(adapter.read_json(self.budget.path)["attempts"], 4)

    def test_eight_request_budget_finishes_both_paragraphs_after_same_evidence_correction(self):
        state, rendered = self.run_two_paragraph_refinement(8)
        self.assertTrue(state["is_completed"])
        self.assertTrue(all(row["research"]["is_completed"] for row in state["paragraphs"]))
        self.assert_usage_counts(5)
        self.assertEqual(self.budget.data["failed_attempts"], 0)  # Both provider responses completed normally.
        rejected, repaired = self.calls[1]["data"], self.calls[2]["data"]
        self.assertNotIn("format_correction", rejected)
        self.assertEqual(repaired["format_correction"]["validation_error"], "invalid research summary fields")
        for field in ("search_results", "search_scope", "forum_feedback", "paragraph_latest_state"):
            self.assertEqual(rejected[field], repaired[field])
        records = state["research_audit"]["summary_responses"]
        self.assertEqual([row["validation"] for row in records], ["rejected", "accepted", "accepted"])
        for record, raw in zip(records, self.raw_summaries):
            self.assertEqual(record["raw_response_sha256"], hashlib.sha256(raw.encode("utf-8")).hexdigest())
            self.assertTrue(Path(record["path"]).is_file())
        self.assertEqual(len(self.fixture.trace["queries"]), 2, "format correction repeated a search")
        self.assertEqual(len(state["research_audit"]["paragraphs"]), 2)
        self.assertTrue(all(row["feedback_consumed"] and row["reflections_executed"] == 1
                            and row["source_records_after"] > row["source_records_before"]
                            for row in state["research_audit"]["paragraphs"]))
        self.assertIn("角度0", rendered)
        self.assertIn("角度1", rendered)

    def test_eight_requests_cover_one_decision_and_summary_correction_per_paragraph(self):
        state, _ = self.run_two_paragraph_refinement(8, repair_both=True, repair_decisions=True)
        self.assertTrue(state["is_completed"])
        self.assert_usage_counts(8)
        for collection in ("decision_responses", "summary_responses"):
            self.assertEqual([row["validation"] for row in state["research_audit"][collection]],
                             ["rejected", "accepted", "rejected", "accepted"])
        self.assertEqual(len(self.fixture.trace["queries"]), 2)

    def test_connection_retry_preserves_the_last_required_summary_slot(self):
        with self.assertRaises(adapter.ModelCallStopped):
            self.run_two_paragraph_refinement(4, format_repair=False, connection_failures=(3,), retry_transient=True, role="media")
        data = adapter.read_json(self.budget.path)
        self.assertEqual(data["attempts"], 3, "retry consumed the final slot required for the summary")
        self.assertEqual(data["failed_attempts"], 1)
        self.assertEqual(data["provider_retries"], [])
        failed = data["provider_attempts"][-1]
        self.assertEqual(failed["error_type"], "APIConnectionError")
        self.assertEqual(failed["required_calls_after"], 1)
        self.assertEqual(failed["retry_blocked_reason"], "reserved_for_required_research_calls")
        self.assertEqual([row["kind"] for row in self.calls], ["decision", "summary", "decision"])
        state = adapter.read_json(self.output / "checkpoint.json")["state"]
        self.assertFalse(state["is_completed"])
        self.assertTrue(state["paragraphs"][0]["research"]["is_completed"])
        self.assertFalse(state["paragraphs"][1]["research"]["is_completed"])

    def test_five_request_bound_finishes_after_the_same_connection_failure_and_retry(self):
        state, rendered = self.run_two_paragraph_refinement(
            5, format_repair=False, connection_failures=(3,), retry_transient=True, role="media")
        self.assertTrue(state["is_completed"])
        self.assertTrue(all(row["research"]["is_completed"] for row in state["paragraphs"]))
        data = adapter.read_json(self.budget.path)
        self.assertEqual((data["limit"], data["attempts"], data["failed_attempts"]), (5, 5, 1))
        self.assertEqual([row["kind"] for row in self.calls], ["decision", "summary", "decision", "decision", "summary"])
        self.assertEqual(self.calls[2], self.calls[3], "retry changed the model request")
        self.assertEqual([row["required_calls_after"] for row in data["provider_attempts"]], [3, 2, 1, 1, 0])
        self.assertEqual(data["provider_attempts"][2]["request_sha256"], data["provider_attempts"][3]["request_sha256"])
        rows = {path.stem: adapter.read_json(path) for path in self.usage_dir.glob("*.json")}
        ordered = [rows[row["usage_call_id"]] for row in data["provider_attempts"]]
        self.assertEqual(len(rows), 5)
        self.assertEqual({row["agent"] for row in ordered}, {"media"})
        self.assertEqual([row["status"] for row in ordered], ["completed", "completed", "error", "completed", "completed"])
        self.assertFalse(ordered[2]["usage_present"])
        self.assertTrue(all(row["usage_present"] for index, row in enumerate(ordered) if index != 2))
        retry = data["provider_retries"][0]
        self.assertEqual((retry["failed_attempt"], retry["retry_attempt"]), (3, 4))
        self.assertEqual(retry["failed_usage_call_id"], ordered[2]["call_id"])
        self.assertEqual(retry["retry_usage_call_id"], ordered[3]["call_id"])
        self.assertTrue(all(row["feedback_consumed"] and row["reflections_executed"] == 1
                            for row in state["research_audit"]["paragraphs"]))
        self.assertEqual(len(self.fixture.trace["queries"]), 2)
        self.assertIn("角度0", rendered)
        self.assertIn("角度1", rendered)
        resumed = adapter.ModelBudget(self.budget.path, "same-input", 5, 120, 16000, retry_transient=True)
        with self.assertRaisesRegex(adapter.ModelCallStopped, "budget exhausted"):
            resumed.wrap(lambda *_args, **_kwargs: self.fail("resumed budget opened a sixth request"))(
                types.SimpleNamespace(max_retries=0), "query")
        self.assertEqual(adapter.read_json(self.budget.path)["attempts"], 5)

    def test_second_connection_failure_stops_without_spending_reserved_summary_capacity(self):
        with self.assertRaises(adapter.ModelCallStopped):
            self.run_two_paragraph_refinement(5, format_repair=False, connection_failures=(1, 4), retry_transient=True)
        data = adapter.read_json(self.budget.path)
        self.assertEqual((data["attempts"], data["failed_attempts"]), (4, 2))
        self.assertEqual(len(data["provider_retries"]), 1)
        self.assertEqual(data["provider_attempts"][-1]["retry_blocked_reason"], "reserved_for_required_research_calls")
        rows = {path.stem: adapter.read_json(path) for path in self.usage_dir.glob("*.json")}
        self.assertEqual(len(rows), 4)
        self.assertEqual(sum(row["status"] == "error" for row in rows.values()), 2)
        self.assertFalse(adapter.read_json(self.output / "checkpoint.json")["state"]["is_completed"])

    def test_format_correction_cannot_take_capacity_reserved_for_the_next_paragraph(self):
        with self.assertRaisesRegex(adapter.ModelCallStopped, "required research steps"):
            self.run_two_paragraph_refinement(4, retry_transient=True)
        data = adapter.read_json(self.budget.path)
        self.assertEqual((data["attempts"], data["failed_attempts"]), (2, 0))
        self.assertEqual([row["kind"] for row in self.calls], ["decision", "summary"])
        self.assertEqual(len(list(self.usage_dir.glob("*.json"))), 2)
        self.assertFalse(adapter.read_json(self.output / "checkpoint.json")["state"]["is_completed"])

    def test_five_request_bound_can_correct_summary_then_finish_required_next_paragraph(self):
        state, _ = self.run_two_paragraph_refinement(5, retry_transient=True)
        self.assertTrue(state["is_completed"])
        self.assert_usage_counts(5)
        self.assertEqual([row["validation"] for row in state["research_audit"]["summary_responses"]],
                         ["rejected", "accepted", "accepted"])
        self.assertEqual([row["required_calls_after"] for row in self.budget.data["provider_attempts"]], [3, 2, 2, 1, 0])

    def test_noncredential_false_setting_does_not_corrupt_saved_summary_or_raw_hash(self):
        raw = json.dumps({"paragraph_latest_state": "给定原文包含false，不能把环境开关当作凭据。"}, ensure_ascii=False)
        path = self.fixture.root / "summary-false.txt"
        audit = {}
        with patch.dict(adapter.os.environ, {"TOKENIZERS_PARALLELISM": "false"}, clear=True):
            result = adapter.archive_research_summary(raw, path, audit)
        self.assertEqual(result, json.loads(raw)["paragraph_latest_state"])
        self.assertEqual(path.read_text(encoding="utf-8"), raw)
        self.assertFalse(audit["summary_responses"][0]["sensitive_values_redacted"])
        self.assertEqual(audit["summary_responses"][0]["raw_response_sha256"], hashlib.sha256(raw.encode()).hexdigest())


if __name__ == "__main__":
    unittest.main()
