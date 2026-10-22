"""Real upstream search nodes with counted offline decision responses only."""
import argparse
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import Mock, patch


spec = importlib.util.spec_from_file_location("decision_contract_depth_fixture", Path(__file__).with_name("test_research_depth.py"))
depth = importlib.util.module_from_spec(spec)
spec.loader.exec_module(depth)
adapter = depth.adapter
SOURCE = Path(__file__).resolve().parents[4] / "reproductions/bettafish-20261001/source/QueryEngine/nodes"
if not SOURCE.is_dir():
    SOURCE = Path("/app/QueryEngine/nodes")


def load_node_source():
    """Load original run methods; adapter must replace their forgiving decoder."""
    stubs = {
        "loguru": types.SimpleNamespace(logger=Mock()),
        "QueryEngine.llms.base": types.SimpleNamespace(LLMClient=object),
        "QueryEngine.state.state": types.SimpleNamespace(State=object),
        "QueryEngine.prompts": types.SimpleNamespace(SYSTEM_PROMPT_FIRST_SEARCH="original first search",
                                                     SYSTEM_PROMPT_REFLECTION="original reflection"),
        "QueryEngine.utils.text_processing": types.SimpleNamespace(**{
            name: Mock(side_effect=AssertionError("upstream local JSON repair must not run")) for name in
            ("remove_reasoning_from_output", "clean_json_tags", "extract_clean_response", "fix_incomplete_json")}),
    }
    with patch.dict(sys.modules, stubs):
        base_spec = importlib.util.spec_from_file_location("QueryEngine.nodes.base_node", SOURCE / "base_node.py")
        base = importlib.util.module_from_spec(base_spec)
        base_spec.loader.exec_module(base)
        with patch.dict(sys.modules, {"QueryEngine.nodes.base_node": base}):
            node_spec = importlib.util.spec_from_file_location("QueryEngine.nodes.search_node", SOURCE / "search_node.py")
            nodes = importlib.util.module_from_spec(node_spec)
            node_spec.loader.exec_module(nodes)
    return nodes


class DecisionContractTests(unittest.TestCase):
    def setUp(self):
        self.fixture = depth.ResearchDepthTests(methodName="runTest")
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.valid = json.dumps({"search_query": "真实待核查关键词", "reasoning": "核查本段证据缺口",
                                 "search_tool": "basic_search_news"}, ensure_ascii=False)
        self.code = "以下是舆情采集流程：\n```python\nprint('collect evidence')\n```"
        self.prefixed = '下面给出决策：\n```json\n{"search_query":"关键词","reasoning":"核查","search_tool":"web_search"}\n```'

    def run_responses(self, responses, limit=4, phase="initial"):
        fixture = self.fixture
        request, payload = fixture.payload(max_paragraphs=1, max_reflections=1,
                                           max_model_requests=limit, research_phase=phase)
        imports = fixture.modules("query")
        self.nodes = load_node_source()
        imports["QueryEngine.nodes.search_node"] = self.nodes
        agent_class = imports["QueryEngine"].DeepSearchAgent
        original_initialize = agent_class.__init__
        self.calls = []
        self.budget = adapter.ModelBudget(fixture.root / "decision-budget.json", "same-task-input", limit, 120, 16000)
        self.output = fixture.root / "query"
        def provider(_client, _role, **kwargs):
            index = len(self.calls)
            self.calls.append(copy.deepcopy(kwargs))
            raw = responses[min(index, len(responses) - 1)]
            return types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="stop", message=types.SimpleNamespace(content=raw))])
        llms = types.SimpleNamespace(recorded_completion=self.budget.wrap(provider))
        imports["QueryEngine.llms.base"] = llms
        def initialize(agent, config):
            original_initialize(agent, config)
            def invoke(system, user, **kwargs):
                result = llms.recorded_completion(types.SimpleNamespace(max_retries=0), "query",
                    messages=[{"role": "system", "content": system}, {"role": "user", "content": user}], **kwargs)
                return result.choices[0].message.content
            agent.llm_client.invoke = invoke
            agent.llm_client.stream_invoke_to_string = invoke
            agent.llm_client.client = object()
            agent.first_search_node = self.nodes.FirstSearchNode(agent.llm_client)
            agent.reflection_node = self.nodes.ReflectionNode(agent.llm_client)
            agent.first_search_node._get_default_search_query = Mock(side_effect=AssertionError("default query forbidden"))
            agent.reflection_node._get_default_reflection_query = Mock(side_effect=AssertionError("default reflection forbidden"))
        args = argparse.Namespace(role="query", resume_seed=None)
        if phase == "refinement":
            args.initial_research_state = fixture.initial_state(1)
            args.initial_research_sha256 = "a" * 64
            args.forum_feedback = "核查初轮疑点，不能扩大已给证据范围。"
        with patch("socket.socket", side_effect=AssertionError("offline decision test opened socket")), \
                patch.object(agent_class, "__init__", initialize), \
                patch.object(adapter.importlib, "import_module", side_effect=lambda name: imports[name]):
            return adapter.research_live(args, request, payload, self.output)

    def checkpoint(self):
        return adapter.read_json(self.output / "checkpoint.json")["state"]

    def assert_archives(self, state, raws, statuses):
        audit = state["research_audit"]
        self.assertEqual([row["validation"] for row in audit["decision_responses"]], statuses)
        self.assertEqual(len(self.calls), len(raws))
        self.assertEqual(self.budget.data["attempts"], len(raws))
        self.assertEqual(adapter.read_json(self.budget.path)["attempts"], len(raws))
        for row, raw in zip(audit["decision_responses"], raws):
            self.assertEqual(row["raw_response_sha256"], hashlib.sha256(raw.encode()).hexdigest())
            self.assertEqual(row["sha256"], hashlib.sha256(Path(row["path"]).read_bytes()).hexdigest())
        requests = audit["decision_requests"]
        self.assertEqual(len(requests), len(raws))
        for row, call in zip(requests, self.calls):
            self.assertEqual(row["sha256"], hashlib.sha256(Path(row["path"]).read_bytes()).hexdigest())
            self.assertEqual(adapter.read_json(Path(row["path"])),
                             {"system": call["messages"][0]["content"], "user": call["messages"][1]["content"]})
            self.assertEqual(call["response_format"], {"type": "json_object"})

    def test_only_whole_json_or_whole_fence_is_decoded_never_extracted_from_explanation(self):
        self.assertEqual(adapter.decode_query_search_decision(self.valid)["search_query"], "真实待核查关键词")
        self.assertEqual(adapter.decode_query_search_decision("```json\n" + self.valid + "\n```"), json.loads(self.valid))
        for raw in (self.code, self.prefixed, "说明\n" + self.valid, self.valid + "\n说明"):
            with self.subTest(raw=raw), self.assertRaises(adapter.AdapterError):
                adapter.decode_query_search_decision(raw)

    def test_query_tool_contract_requires_real_tool_exact_fields_and_valid_dates(self):
        base = json.loads(self.valid)
        for tool in adapter.QUERY_SEARCH_TOOLS:
            value = {**base, "search_tool": tool}
            if tool == "search_news_by_date":
                value.update(start_date="2024-08-01", end_date="2024-08-31")
            self.assertEqual(adapter.decode_query_search_decision(json.dumps(value)), value)
        negatives = [{**base, "search_tool": "web_search"}, {**base, "search_scope": {}},
                     {**base, "reasoning": " "}, {**base, "search_query": "q" * 601},
                     {**base, "start_date": "2024-08-01"},
                     {**base, "search_tool": "search_news_by_date", "start_date": "2024-02-30", "end_date": "2024-03-01"},
                     {**base, "search_tool": "search_news_by_date", "start_date": "2024-09-01", "end_date": "2024-08-01"}]
        for value in negatives:
            with self.subTest(value=value), self.assertRaises(adapter.AdapterError):
                adapter.decode_query_search_decision(json.dumps(value))

    def test_real_initial_and_reflection_nodes_send_json_mode_and_archive_exact_input(self):
        state, _ = self.run_responses([self.valid])
        self.assertTrue(state["is_completed"])
        self.assert_archives(state, [self.valid] * 2, ["accepted"] * 2)
        for call in self.calls:
            prompt = call["messages"][0]["content"]
            self.assertIn("不执行检索", prompt)
            self.assertIn("不向用户提问或请求权限", prompt)
            self.assertIn("search_tool", prompt)
            self.assertEqual(json.loads(call["messages"][1]["content"])["topic"], self.fixture.topic)

    def test_bad_decision_is_corrected_once_with_actual_previous_response_and_same_scope(self):
        state, _ = self.run_responses([self.code, self.valid])
        self.assert_archives(state, [self.code, self.valid, self.valid], ["rejected", "accepted", "accepted"])
        original = json.loads(self.calls[0]["messages"][1]["content"])
        corrected = json.loads(self.calls[1]["messages"][1]["content"])
        correction = corrected.pop("format_correction")
        self.assertEqual(corrected, original)
        self.assertEqual(correction["previous_response_excerpt"], self.code)
        self.assertIs(correction["previous_response_truncated"], False)
        self.assertIn("complete JSON", correction["validation_error"])
        self.assertEqual(Path(state["research_audit"]["decision_responses"][0]["path"]).read_text(encoding="utf-8"), self.code)
        self.assertEqual(self.fixture.trace["queries"], ["真实待核查关键词"] * 2)

    def test_two_real_failure_shapes_remain_failed_without_extracting_json_or_default_search(self):
        with self.assertRaises(adapter.AdapterError):
            self.run_responses([self.code, self.prefixed], limit=2)
        state = self.checkpoint()
        self.assertFalse(state["is_completed"])
        self.assert_archives(state, [self.code, self.prefixed], ["rejected", "rejected"])
        self.assertEqual(self.fixture.trace["queries"], [])
        self.assertFalse(state["paragraphs"][0]["research"]["latest_summary"])

    def test_unknown_tool_is_regenerated_before_any_execution(self):
        bad = json.dumps({**json.loads(self.valid), "search_tool": "web_search"})
        state, _ = self.run_responses([bad, self.valid])
        self.assert_archives(state, [bad, self.valid, self.valid], ["rejected", "accepted", "accepted"])
        correction = json.loads(self.calls[1]["messages"][1]["content"])["format_correction"]
        self.assertIn("available Query search_tool", correction["validation_error"])
        self.assertEqual(self.fixture.trace["queries"], ["真实待核查关键词"] * 2)

    def test_correction_cannot_reset_persistent_one_request_budget(self):
        with self.assertRaisesRegex(adapter.ModelCallStopped, "budget exhausted"):
            self.run_responses([self.code, self.valid], limit=1)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.budget.data["attempts"], 1)
        self.assertEqual(adapter.read_json(self.budget.path)["attempts"], 1)
        self.assertEqual(self.fixture.trace["queries"], [])
        self.assertEqual(len(self.checkpoint()["research_audit"]["decision_responses"]), 1)

    def test_actual_sanitized_long_response_excerpt_and_truncation_enter_correction(self):
        raw = self.code + "\n" + "offline-sensitive-key-value" + "x" * 18000
        with patch.dict(os.environ, {"QUERY_ENGINE_API_KEY": "offline-sensitive-key-value"}):
            state, _ = self.run_responses([raw, self.valid])
        self.assert_archives(state, [raw, self.valid, self.valid], ["rejected", "accepted", "accepted"])
        first = state["research_audit"]["decision_responses"][0]
        stored = Path(first["path"]).read_text(encoding="utf-8")
        self.assertTrue(first["sensitive_values_redacted"])
        self.assertNotIn("offline-sensitive-key-value", stored)
        correction = json.loads(self.calls[1]["messages"][1]["content"])["format_correction"]
        self.assertEqual(correction["previous_response_excerpt"], stored[:16000])
        self.assertIs(correction["previous_response_truncated"], True)

    def test_feedback_correction_preserves_existing_state_topic_and_forum_input(self):
        state, _ = self.run_responses([self.code, self.valid], limit=2, phase="refinement")
        self.assert_archives(state, [self.code, self.valid], ["rejected", "accepted"])
        self.assertEqual(self.fixture.trace["structure"], 0)
        first = json.loads(self.calls[0]["messages"][1]["content"])
        second = json.loads(self.calls[1]["messages"][1]["content"])
        self.assertEqual(first["forum_feedback"], second["forum_feedback"])
        self.assertEqual(first["paragraph_latest_state"], second["paragraph_latest_state"])
        self.assertEqual(first["title"], second["title"])
        self.assertTrue(state["research_audit"]["paragraphs"][0]["feedback_consumed"])


if __name__ == "__main__":
    unittest.main()
