"""Counted offline outline generation; never fix malformed JSON locally."""
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
    "structure_recovery_depth_fixture", Path(__file__).with_name("test_research_depth.py"))
depth = importlib.util.module_from_spec(spec)
spec.loader.exec_module(depth)
adapter = depth.adapter


class StructureRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.fixture = depth.ResearchDepthTests(methodName="runTest")
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.valid_rows = [{"title": "事件来源", "content": "核查给定报道及时间边界。"},
                           {"title": "观点分歧", "content": "核查原始评论与争议证据。"}]
        self.valid = json.dumps({"paragraphs": self.valid_rows}, ensure_ascii=False)

    def research_with_structure_responses(self, responses, limit=2, *, phase="initial", role="query", retry_transient=False):
        """Only outline providers are mocked; production hooks enforce bounds."""
        fixture = self.fixture
        request, payload = fixture.payload(max_paragraphs=2, max_reflections=0, research_phase=phase,
                                           max_model_requests=limit)
        package = role.capitalize() + "Engine"
        imports = fixture.modules(role)
        agent_class = imports[package].DeepSearchAgent
        original_initialize = agent_class.__init__
        self.calls = []
        self.structure_nodes = []
        self.budget = adapter.ModelBudget(fixture.root / "structure-budget.json", "outline-input", limit, 120, 16000,
                                         retry_transient=retry_transient)
        self.output = fixture.root / role

        def provider(_client, _role, **kwargs):
            index = len(self.calls)
            self.calls.append(copy.deepcopy(kwargs))
            response = responses[min(index, len(responses) - 1)]
            if isinstance(response, BaseException):
                raise response
            return types.SimpleNamespace(choices=[types.SimpleNamespace(
                finish_reason="stop", message=types.SimpleNamespace(content=response))])

        counted = self.budget.wrap(provider)
        llms = types.SimpleNamespace(recorded_completion=counted)
        imports[package + ".llms.base"] = llms
        structure_module = imports[package + ".nodes.report_structure_node"]
        seen_nodes = self.structure_nodes

        class StructureNode:
            def __init__(node, client, query):
                node.llm_client, node.query = client, query
                seen_nodes.append(node)

            def run(node, input_data=None, **kwargs):
                raw = node.llm_client.stream_invoke_to_string(structure_module.SYSTEM_PROMPT_REPORT_STRUCTURE,
                                                             node.query, **kwargs)
                return node.process_output(raw)

            def process_output(node, raw):
                return json.loads(raw)

            def mutate_state(node, input_data=None, state=None, **kwargs):
                rows = node.run(input_data, **kwargs)
                value = fixture.initial_state(count=0)
                value["query"] = node.query  # Mirrors the original node after its run returns.
                value["is_completed"] = False
                for row in rows:
                    value["paragraphs"].append({**row, "research": {"is_completed": False,
                        "latest_summary": "", "reflection_iteration": 0, "search_history": []}})
                return depth.FakeState(value)

        structure_module.ReportStructureNode = StructureNode

        def initialize(agent, config):
            original_initialize(agent, config)
            value = fixture.initial_state(count=0)
            value["is_completed"] = False
            agent.state = depth.FakeState(value)

            def invoke(system, user, **kwargs):
                response = llms.recorded_completion(types.SimpleNamespace(max_retries=0), role,
                                   messages=[{"role": "system", "content": system},
                                             {"role": "user", "content": user}], **kwargs)
                return response.choices[0].message.content

            agent.llm_client.invoke = invoke
            agent.llm_client.stream_invoke_to_string = invoke
            agent.llm_client.client = object()

        def generate(agent, query):
            fixture.trace["structure"] += 1
            agent.state = StructureNode(agent.llm_client, query).mutate_state(state=agent.state)

        arguments = argparse.Namespace(role=role, resume_seed=None)
        if phase == "refinement":
            arguments.initial_research_state = fixture.initial_state()
            arguments.initial_research_sha256 = "a" * 64
            arguments.forum_feedback = "仅依据当前原始摘录核查初轮分歧。"
        with patch("socket.socket", side_effect=AssertionError("offline outline test attempted network access")), \
                patch.object(agent_class, "__init__", initialize), \
                patch.object(agent_class, "_generate_report_structure", generate), \
                patch.object(adapter.importlib, "import_module", side_effect=lambda name: imports[name]):
            return adapter.research_live(arguments, request, payload, self.output)

    def assert_records(self, state, responses, statuses):
        records = state["research_audit"]["structure_responses"]
        self.assertEqual([record["validation"] for record in records], statuses)
        for record, raw in zip(records, responses):
            self.assertEqual(Path(record["path"]).read_text(encoding="utf-8"), raw)
            self.assertEqual(record["raw_response_sha256"], hashlib.sha256(raw.encode("utf-8")).hexdigest())
        self.assertEqual(self.budget.data["attempts"], len(responses))
        self.assertEqual(adapter.read_json(self.budget.path)["attempts"], len(responses))
        self.assertEqual(self.budget.data["agents"], {"query": len(responses)})
        self.assertEqual(len(self.calls), len(responses))
        requests = state["research_audit"]["structure_requests"]
        self.assertEqual(len(requests), len(responses))
        for request, call in zip(requests, self.calls):
            path = Path(request["path"])
            self.assertEqual(request["sha256"], hashlib.sha256(path.read_bytes()).hexdigest())
            self.assertEqual(adapter.read_json(path), {"system": call["messages"][0]["content"],
                                                       "user": call["messages"][1]["content"]})
        self.assertTrue(self.structure_nodes)
        self.assertTrue(all(node.query == self.fixture.topic for node in self.structure_nodes),
                        "correction query must be restored after success or failure")

    def test_valid_outline_is_saved_and_uses_only_one_original_provider_call(self):
        state, _ = self.research_with_structure_responses([self.valid])
        self.assertTrue(state["is_completed"])
        self.assert_records(state, [self.valid], ["accepted"])
        self.assertEqual(self.calls[0]["messages"][1]["content"], self.fixture.topic)
        self.assertEqual(self.calls[0]["response_format"], {"type": "json_object"})

    def test_decode_requires_configured_angle_count_and_only_original_fields(self):
        self.assertEqual(adapter.decode_research_structure(self.valid, 2), self.valid_rows)
        negative = [self.valid_rows, [], {}, {"paragraphs": []}, {"paragraphs": self.valid_rows[:1]},
                    {"paragraphs": self.valid_rows * 2}, {"paragraphs": self.valid_rows, "input_only": True},
                    {"paragraphs": [{**self.valid_rows[0], "search_scope": {}}] + self.valid_rows[1:]},
                    {"paragraphs": [{"title": "缺内容"}] + self.valid_rows[1:]},
                    {"paragraphs": [{"title": "空内容", "content": " "}] + self.valid_rows[1:]},
                    {"paragraphs": ["不是对象"] + self.valid_rows[1:]}]
        for value in negative:
            with self.subTest(value=value):
                with self.assertRaises(adapter.AdapterError):
                    adapter.decode_research_structure(json.dumps(value, ensure_ascii=False), 2)

    def test_decode_requires_all_five_configured_angles_not_the_old_single_example(self):
        rows = [{"title": "研究角度" + str(index), "content": "仅核查给定材料。"} for index in range(5)]
        self.assertEqual(adapter.decode_research_structure(json.dumps({"paragraphs": rows}, ensure_ascii=False), 5), rows)
        for incomplete in (rows[:1], rows[:2], rows[:4]):
            with self.subTest(count=len(incomplete)), self.assertRaises(adapter.AdapterError):
                adapter.decode_research_structure(json.dumps({"paragraphs": incomplete}, ensure_ascii=False), 5)

    def test_completed_malformed_json_is_regenerated_once_and_original_preserved(self):
        malformed = '{"paragraphs":[{"title":"原始\n非法换行","content":"范围"}]}'
        state, _ = self.research_with_structure_responses([malformed, self.valid])
        self.assertTrue(state["is_completed"])
        self.assertEqual(len(state["paragraphs"]), 2)
        self.assert_records(state, [malformed, self.valid], ["rejected", "accepted"])
        correction = self.calls[1]["messages"][1]["content"]
        self.assertIn("complete JSON", correction)
        self.assertIn("format_correction", correction, "Node.run ignores input_data; correction must reach self.query")
        self.assertIn(self.fixture.topic, correction)
        self.assertEqual(self.budget.data["failed_attempts"], 0)

    def test_missing_angle_is_regenerated_without_silently_accepting_less_work(self):
        missing = json.dumps({"paragraphs": self.valid_rows[:1]}, ensure_ascii=False)
        state, _ = self.research_with_structure_responses([missing, self.valid])
        self.assertEqual(len(state["paragraphs"]), 2)
        self.assert_records(state, [missing, self.valid], ["rejected", "accepted"])

    def test_extra_field_is_rejected_then_regenerated_not_locally_removed(self):
        rows = [{**self.valid_rows[0], "search_scope": {"input_only": True}}, self.valid_rows[1]]
        extra = json.dumps({"paragraphs": rows}, ensure_ascii=False)
        state, _ = self.research_with_structure_responses([extra, self.valid])
        self.assert_records(state, [extra, self.valid], ["rejected", "accepted"])
        self.assertIn("invalid research structure fields", json.dumps(self.calls[1]["messages"], ensure_ascii=False))

    def test_two_invalid_responses_still_fail_without_a_third_call_or_default_outline(self):
        wrong_root = json.dumps(self.valid_rows, ensure_ascii=False)
        with self.assertRaises(adapter.AdapterError):
            self.research_with_structure_responses([wrong_root], limit=3)
        state = adapter.read_json(self.output / "checkpoint.json")["state"]
        self.assertFalse(state["is_completed"])
        self.assertFalse(state["paragraphs"])
        self.assertFalse(self.fixture.trace["queries"])
        self.assert_records(state, [wrong_root, wrong_root], ["rejected", "rejected"])

    def test_outline_uses_object_mode_on_both_original_and_corrective_provider_calls(self):
        for role in ("query", "media", "insight"):
            with self.subTest(role=role):
                # Reset the same-task counted fixture between independent roles.
                fixture = depth.ResearchDepthTests(methodName="runTest")
                fixture.setUp()
                self.addCleanup(fixture.doCleanups)
                self.fixture = fixture
                if role == "insight":
                    with patch.object(adapter, "fresh_file_sample", return_value=None):
                        state, _ = self.research_with_structure_responses(["not JSON", self.valid], role=role)
                else:
                    state, _ = self.research_with_structure_responses(["not JSON", self.valid], role=role)
                self.assertTrue(state["is_completed"])
                self.assertEqual(len(self.calls), 2)
                self.assertTrue(all(row.get("response_format") == {"type": "json_object"} for row in self.calls))

    def test_refinement_reuses_initial_angles_without_a_new_outline_model_request(self):
        state, _ = self.research_with_structure_responses([], phase="refinement")
        self.assertTrue(state["is_completed"])
        self.assertEqual(self.budget.data["attempts"], 0)
        self.assertFalse(self.calls)
        self.assertEqual(self.fixture.trace["structure"], 0)
        self.assertEqual([row["title"] for row in state["paragraphs"]], ["角度0", "角度1"])
        self.assertEqual(state["research_audit"].get("structure_responses", []), [])
        self.assertTrue(all(row["feedback_consumed"] for row in state["research_audit"]["paragraphs"]))

    def test_structure_correction_cannot_reset_or_exceed_the_same_provider_budget(self):
        invalid = '{"unexpected":"object"}'
        with self.assertRaisesRegex(adapter.ModelCallStopped, "budget exhausted"):
            self.research_with_structure_responses([invalid, self.valid], limit=1)
        state = adapter.read_json(self.output / "checkpoint.json")["state"]
        self.assert_records(state, [invalid], ["rejected"])
        self.assertFalse(self.fixture.trace["queries"])
        reopened = adapter.ModelBudget(self.budget.path, "outline-input", 1, 120, 16000)
        self.assertEqual(reopened.data["attempts"], 1)

    def test_transient_provider_failure_is_not_misclassified_as_bad_outline_json(self):
        with self.assertRaises(adapter.ModelCallStopped) as failure:
            self.research_with_structure_responses([TimeoutError("offline provider timeout"), self.valid])
        self.assertTrue(failure.exception.retryable)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.budget.data["attempts"], 1)
        self.assertEqual(self.budget.data["failed_attempts"], 1)
        self.assertFalse(self.fixture.trace["queries"])

    def test_outline_reserves_initial_decision_and_summary_for_each_required_angle(self):
        state, _ = self.research_with_structure_responses([self.valid], limit=5, retry_transient=True)
        # Only outline calls are real counted wrappers in this focused fixture;
        # the reservation still includes both following decisions and summaries.
        self.assertTrue(state["is_completed"])
        self.assertEqual(self.budget.data["provider_attempts"][0]["required_calls_after"], 4)
        self.assertEqual(self.budget.data["provider_attempts"][0]["research_step"], "structure")
        self.assertEqual(self.budget.data["attempts"], 1)

    def test_outline_format_correction_cannot_consume_the_required_angle_capacity(self):
        with self.assertRaisesRegex(adapter.ModelCallStopped, "required research steps"):
            self.research_with_structure_responses(["invalid JSON", self.valid], limit=5, retry_transient=True)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.budget.data["blocked_requests"][-1]["required_calls_after"], 4)
        state = adapter.read_json(self.output / "checkpoint.json")["state"]
        self.assertFalse(state["is_completed"])
        self.assertFalse(state["paragraphs"])


if __name__ == "__main__":
    unittest.main()
