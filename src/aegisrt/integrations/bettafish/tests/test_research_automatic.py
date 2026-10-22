"""Offline regressions for bounded research recovery and provider classification."""
import argparse
import copy
import importlib.util
import json
from pathlib import Path
import types
import unittest
from unittest.mock import Mock, patch


spec = importlib.util.spec_from_file_location("bettafish_depth_fixture_for_automatic", Path(__file__).with_name("test_research_depth.py"))
depth = importlib.util.module_from_spec(spec)
spec.loader.exec_module(depth)
adapter = depth.adapter


class ProviderHTTPError(Exception):
    def __init__(self, status):
        super().__init__("offline provider status")
        self.status_code = status


class ResearchAutomaticBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.fixture = depth.ResearchDepthTests(methodName="runTest")
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)

    def research_with_node_responses(self, kind, responses, limit=3):
        """The selected node makes counted mock calls; other nodes are stubs."""
        fixture = self.fixture
        request, payload = fixture.payload(max_paragraphs=1, max_reflections=1)
        imports = fixture.modules("query")
        agent_class = imports["QueryEngine"].DeepSearchAgent
        original_init = agent_class.__init__
        self.node_attempts = []
        self.budget = adapter.ModelBudget(fixture.root / "format-budget.json", "same-task-input", limit, 120, 16000)
        def provider(client, role, **kwargs):
            index = len(self.node_attempts)
            self.node_attempts.append(copy.deepcopy(kwargs["node_input"]))
            raw = responses[min(index, len(responses) - 1)]
            if isinstance(raw, BaseException):
                raise raw
            return types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="stop", message=types.SimpleNamespace(content=raw))])
        counted = self.budget.wrap(provider)
        def initialize(agent, config):
            original_init(agent, config)
            node = agent.reflection_node if kind == "reflection-search" else agent.first_summary_node
            node.process_output = lambda raw: raw
            def response(data, **kwargs):
                result = counted(types.SimpleNamespace(max_retries=0), "query", node_input=data)
                return node.process_output(result.choices[0].message.content)
            node.run = response
        args = argparse.Namespace(role="query", resume_seed=None)
        with patch("socket.socket", side_effect=AssertionError("offline test opened a socket")), \
                patch.object(agent_class, "__init__", initialize), \
                patch.object(adapter.importlib, "import_module", side_effect=lambda name: imports[name]):
            return adapter.research_live(args, request, payload, fixture.root / "query")

    def test_completed_malformed_summary_is_regenerated_once_and_original_is_preserved(self):
        malformed = '{"paragraph_latest_state":"unescaped\nsummary"}'
        valid = json.dumps({"paragraph_latest_state": "valid regenerated summary"})
        state, _ = self.research_with_node_responses("summary", [malformed, valid])
        self.assertTrue(state["is_completed"])
        self.assertEqual(len(self.node_attempts), 2)
        self.assertNotIn("format_correction", self.node_attempts[0])
        self.assertIn("format_correction", self.node_attempts[1])
        self.assertEqual(self.node_attempts[0]["search_results"], self.node_attempts[1]["search_results"])
        records = state["research_audit"]["summary_responses"]
        self.assertEqual([record["validation"] for record in records], ["rejected", "accepted"])
        self.assertEqual(Path(records[0]["path"]).read_text(encoding="utf-8"), malformed)
        self.assertEqual(Path(records[1]["path"]).read_text(encoding="utf-8"), valid)
        self.assertEqual(self.budget.data["attempts"], 2)
        self.assertEqual(self.budget.data["failed_attempts"], 0)  # Provider completed; decoding failed.
        self.assertEqual(len(self.fixture.trace["queries"]), 2)  # Format repair did not repeat a search.

    def test_completed_malformed_reflection_query_is_regenerated_once_before_search(self):
        malformed = '{"search_query":"unescaped\nquery","reasoning":"verify evidence"}'
        valid = json.dumps({"search_query": "regenerated query", "reasoning": "verify evidence", "search_tool": "basic_search_news"})
        state, _ = self.research_with_node_responses("reflection-search", [malformed, valid])
        self.assertTrue(state["is_completed"])
        self.assertEqual(len(self.node_attempts), 2)
        self.assertIn("format_correction", self.node_attempts[1])
        records = state["research_audit"]["decision_responses"]
        self.assertEqual([record["validation"] for record in records], ["rejected", "accepted"])
        self.assertEqual(Path(records[0]["path"]).read_text(encoding="utf-8"), malformed)
        self.assertEqual(self.fixture.trace["queries"][-1], "regenerated query")
        self.assertEqual(len(self.fixture.trace["queries"]), 2)
        self.assertEqual(self.budget.data["attempts"], 2)

    def test_two_malformed_summaries_still_fail_without_a_third_attempt_or_fake_summary(self):
        malformed = '{"paragraph_latest_state":"unescaped\nsummary"}'
        with self.assertRaisesRegex(adapter.AdapterError, "complete JSON"):
            self.research_with_node_responses("summary", [malformed])
        self.assertEqual(len(self.node_attempts), 2)
        self.assertEqual(self.budget.data["attempts"], 2)
        state = adapter.read_json(self.fixture.root / "query/checkpoint.json")["state"]
        self.assertFalse(state["is_completed"])
        self.assertFalse(state["paragraphs"][0]["research"]["latest_summary"])
        self.assertEqual([row["validation"] for row in state["research_audit"]["summary_responses"]], ["rejected"] * 2)

    def test_two_malformed_decisions_still_fail_without_running_a_search_for_them(self):
        malformed = '{"search_query":"unescaped\nquery","reasoning":"verify"}'
        with self.assertRaises(adapter.AdapterError):
            self.research_with_node_responses("reflection-search", [malformed])
        self.assertEqual(len(self.node_attempts), 2)
        self.assertEqual(len(self.fixture.trace["queries"]), 1)  # Only the initial search ran.
        state = adapter.read_json(self.fixture.root / "query/checkpoint.json")["state"]
        self.assertEqual([row["validation"] for row in state["research_audit"]["decision_responses"]], ["rejected"] * 2)
        self.assertFalse(state["paragraphs"][0]["research"]["is_completed"])

    def test_format_retry_cannot_reset_or_exceed_same_task_request_budget(self):
        malformed = '{"paragraph_latest_state":"unescaped\nsummary"}'
        with self.assertRaisesRegex(adapter.ModelCallStopped, "budget exhausted"):
            self.research_with_node_responses("summary", [malformed, json.dumps({"paragraph_latest_state": "valid"})], limit=1)
        self.assertEqual(len(self.node_attempts), 1)
        self.assertEqual(self.budget.data["attempts"], 1)
        persisted = adapter.ModelBudget(self.fixture.root / "format-budget.json", "same-task-input", 1, 120, 16000)
        self.assertEqual(persisted.data["attempts"], 1)

    def test_transient_provider_failure_does_not_enter_a_research_format_retry(self):
        with self.assertRaises(adapter.ModelCallStopped) as caught:
            self.research_with_node_responses("summary", [TimeoutError("offline timeout")])
        self.assertTrue(caught.exception.retryable)
        self.assertEqual(len(self.node_attempts), 1)
        self.assertEqual(self.budget.data["failed_attempts"], 1)

    def test_legitimate_empty_recent_query_windows_recover_once_with_same_terms_and_scope(self):
        for tool in ("search_news_last_24_hours", "search_news_last_week"):
            with self.subTest(tool=tool):
                query = "official update"
                original = types.SimpleNamespace(query=query, results=[])
                source = types.SimpleNamespace(title="source", url="https://example.com/news", content="original evidence")
                recovered = types.SimpleNamespace(query=query, results=[source])
                search = Mock(side_effect=[original, recovered])
                responses = []
                response, scope = adapter.search_with_empty_date_fallback(search, "query", tool, query, {}, lambda value, event: responses.append((value, event)))
                self.assertIs(response, recovered)
                self.assertEqual(search.call_args_list, [unittest.mock.call(tool, query), unittest.mock.call("basic_search_news", query)])
                self.assertEqual(scope["requested_tool"], tool)
                self.assertEqual(scope["effective_tool"], "basic_search_news")
                self.assertEqual(scope["query"], query)
                self.assertTrue(scope["date_constraints_relaxed"])
                self.assertEqual(scope["search_attempts"], 2)
                self.assertIs(responses[0][0], original)
                self.assertTrue(responses[1][1]["date_constraints_relaxed"])

    def test_anspire_recent_media_windows_recover_once_with_same_terms(self):
        for tool in ("search_last_24_hours", "search_last_week"):
            with self.subTest(tool=tool):
                query = "official update"
                original = types.SimpleNamespace(query=query, webpages=[])
                source = types.SimpleNamespace(name="source", url="https://example.com/news", snippet="original evidence")
                recovered = types.SimpleNamespace(query=query, webpages=[source])
                search = Mock(side_effect=[original, recovered])
                response, scope = adapter.search_with_empty_date_fallback(search, "media", tool, query, {}, lambda *_: None, media_window_recovery=True)
                self.assertIs(response, recovered)
                self.assertEqual(search.call_args_list, [unittest.mock.call(tool, query), unittest.mock.call("comprehensive_search", query)])
                self.assertTrue(scope["date_constraints_relaxed"])
                self.assertEqual(scope["requested_tool"], tool)
                self.assertEqual(scope["effective_tool"], "comprehensive_search")

    def test_bocha_empty_response_is_not_assumed_successful_or_recovered(self):
        query = "official update"
        search = Mock(return_value=types.SimpleNamespace(query=query, webpages=[]))
        response, scope = adapter.search_with_empty_date_fallback(search, "media", "search_last_week", query, {}, lambda *_: None, media_window_recovery=False)
        self.assertEqual(search.call_count, 1)
        self.assertFalse(scope["date_constraints_relaxed"])
        with self.assertRaises(adapter.AdapterError):
            adapter.bound_search_response(response, "media")

    def test_search_api_error_and_failure_sentinel_never_trigger_window_recovery(self):
        query = "official update"
        for role, tool, field in (("query", "search_news_last_week", "results"), ("media", "search_last_week", "webpages")):
            for error, response_query in (("HTTP 401", query), (None, "搜索失败")):
                with self.subTest(role=role, error=error, response_query=response_query):
                    original = types.SimpleNamespace(query=response_query, error_message=error, **{field: []})
                    search = Mock(return_value=original)
                    response, scope = adapter.search_with_empty_date_fallback(search, role, tool, query, {}, lambda *_: None, media_window_recovery=True)
                    self.assertIs(response, original)
                    self.assertEqual(search.call_count, 1)
                    self.assertFalse(scope["date_constraints_relaxed"])
                    with self.assertRaises(adapter.AdapterError):
                        adapter.bound_search_response(response, role)

    def test_both_window_and_unrestricted_queries_empty_remain_failure_without_fake_sources(self):
        for role, tool, field in (("query", "search_news_last_week", "results"), ("media", "search_last_week", "webpages")):
            with self.subTest(role=role):
                query = "no matching evidence"
                search = Mock(return_value=types.SimpleNamespace(query=query, **{field: []}))
                response, scope = adapter.search_with_empty_date_fallback(search, role, tool, query, {}, lambda *_: None, media_window_recovery=True)
                self.assertEqual(search.call_count, 2)
                self.assertTrue(scope["date_constraints_relaxed"])
                self.assertEqual(getattr(response, field), [])
                with self.assertRaises(adapter.AdapterError):
                    adapter.bound_search_response(response, role)

    def test_transient_search_error_is_retried_once_and_propagated_without_date_fallback(self):
        error = ProviderHTTPError(503)
        search = Mock(side_effect=error)
        events = []
        with self.assertRaises(ProviderHTTPError):
            adapter.search_with_empty_date_fallback(search, "query", "search_news_last_week", "query", {}, lambda value, event: events.append((value, event)))
        self.assertEqual(search.call_count, 2)
        self.assertEqual(search.call_args_list[0], search.call_args_list[1])
        self.assertEqual([event["request_attempt"] for _, event in events], [1, 2])
        for response, event in events:
            self.assertIsNone(response)
            self.assertEqual(event["error_type"], "ProviderHTTPError")
            self.assertEqual(event["http_status"], 503)
            self.assertTrue(event["retryable"])
            self.assertFalse(event.get("date_constraints_relaxed", False))

    def test_actual_research_enables_object_format_for_all_five_structured_prompts(self):
        fixture = self.fixture
        request, payload = fixture.payload(max_paragraphs=1, max_reflections=1)
        imports = fixture.modules("query")
        agent_class = imports["QueryEngine"].DeepSearchAgent
        original_init = agent_class.__init__
        calls = []
        llms = types.SimpleNamespace(recorded_completion=lambda client, role, **kwargs: calls.append(kwargs))
        imports["QueryEngine.llms.base"] = llms
        def initialize(agent, config):
            original_init(agent, config)
            agent.llm_client.client = object()
        with patch("socket.socket", side_effect=AssertionError("offline test opened a socket")), \
                patch.object(agent_class, "__init__", initialize), \
                patch.object(adapter.importlib, "import_module", side_effect=lambda name: imports[name]):
            adapter.research_live(argparse.Namespace(role="query", resume_seed=None), request, payload, fixture.root / "query")
        search_prompts = imports["QueryEngine.nodes.search_node"]
        summary_prompts = imports["QueryEngine.nodes.summary_node"]
        prompts = (search_prompts.SYSTEM_PROMPT_FIRST_SEARCH, search_prompts.SYSTEM_PROMPT_REFLECTION,
                   summary_prompts.SYSTEM_PROMPT_FIRST_SUMMARY, summary_prompts.SYSTEM_PROMPT_REFLECTION_SUMMARY)
        for prompt in prompts:
            llms.recorded_completion(object(), "query", messages=[{"role": "system", "content": prompt}])
            self.assertEqual(calls[-1]["response_format"], {"type": "json_object"})
        outline = imports["QueryEngine.nodes.report_structure_node"].SYSTEM_PROMPT_REPORT_STRUCTURE
        llms.recorded_completion(object(), "query", messages=[{"role": "system", "content": outline}])
        self.assertEqual(calls[-1]["response_format"], {"type": "json_object"})

    def test_provider_output_truncation_remains_an_explicit_failure_with_no_hidden_token_increase(self):
        budget = adapter.ModelBudget(self.fixture.root / "length-budget.json", "test-input", 2, 120, 16000)
        calls = []
        def provider(client, role, **kwargs):
            calls.append(kwargs)
            return types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="length", message=types.SimpleNamespace(content='{"paragraph_latest_state":"incomplete'))])
        with self.assertRaises(adapter.ModelCallStopped) as caught:
            budget.wrap(provider)(types.SimpleNamespace(max_retries=0), "query")
        self.assertFalse(caught.exception.retryable)
        self.assertEqual(budget.data["attempts"], 1)
        self.assertEqual(budget.data["failed_attempts"], 1)
        self.assertEqual(budget.data["last_response_finish_reasons"], ["length"])
        self.assertEqual(calls[0]["max_tokens"], 16000)

    def test_provider_transient_classification_distinguishes_retryable_and_permanent_errors(self):
        errors = [(ProviderHTTPError(code), expected) for code, expected in
                  ((429, True), (503, True), (408, True), (409, True), (500, True), (599, True),
                   (401, False), (403, False), (400, False), (404, False), ("503", False), (True, False))]
        errors.extend([(TimeoutError("timeout"), True), (ConnectionError("connection"), True),
                       (type("APITimeoutError", (Exception,), {})(), True),
                       (type("APIConnectionError", (Exception,), {})(), True),
                       (RuntimeError("unknown"), False), (ValueError("invalid"), False)])
        for index, (error, retryable) in enumerate(errors):
            with self.subTest(error_type=type(error).__name__, status=getattr(error, "status_code", None)):
                budget = adapter.ModelBudget(self.fixture.root / f"classification-{index}.json", "input", 1, 120)
                provider = Mock(side_effect=error)
                with self.assertRaises(adapter.ModelCallStopped) as caught:
                    budget.wrap(provider)(types.SimpleNamespace(max_retries=0), "report")
                self.assertEqual(caught.exception.retryable, retryable)
                self.assertEqual(caught.exception.error_type, type(error).__name__)
                expected_status = getattr(error, "status_code", None)
                self.assertEqual(caught.exception.http_status, expected_status if type(expected_status) is int else None)
                self.assertEqual(budget.data["attempts"], 1)
                self.assertEqual(budget.data["failed_attempts"], 1)
                self.assertEqual(provider.call_count, 1)

    def test_transient_retry_attempts_accumulate_and_persist_before_budget_exhaustion(self):
        path = self.fixture.root / "cumulative-budget.json"
        budget = adapter.ModelBudget(path, "same-input", 2, 120)
        ok = types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="stop")])
        provider = Mock(side_effect=[ProviderHTTPError(503), ok])
        counted = budget.wrap(provider)
        with self.assertRaises(adapter.ModelCallStopped) as caught:
            counted(types.SimpleNamespace(max_retries=0), "report")
        self.assertTrue(caught.exception.retryable)
        self.assertIs(counted(types.SimpleNamespace(max_retries=0), "report"), ok)
        resumed = adapter.ModelBudget(path, "same-input", 2, 120)
        with self.assertRaisesRegex(adapter.ModelCallStopped, "budget exhausted") as exhausted:
            resumed.wrap(provider)(types.SimpleNamespace(max_retries=0), "report")
        self.assertFalse(exhausted.exception.retryable)
        self.assertEqual(provider.call_count, 2)
        self.assertEqual(resumed.data["attempts"], 2)
        self.assertEqual(resumed.data["failed_attempts"], 1)
        self.assertEqual(resumed.data["agents"], {"report": 2})

    def test_stream_failure_transient_classification_also_closes_stream_and_counts_once(self):
        for index, (error, retryable) in enumerate(((ProviderHTTPError(429), True), (ProviderHTTPError(503), True),
                                                   (TimeoutError("timeout"), True), (ProviderHTTPError(401), False))):
            with self.subTest(error_type=type(error).__name__, status=getattr(error, "status_code", None)):
                closed = []
                def stream():
                    try:
                        raise error
                        yield None
                    finally:
                        closed.append(True)
                budget = adapter.ModelBudget(self.fixture.root / f"stream-{index}.json", "input", 1, 120)
                bounded = budget.wrap(lambda client, role, **kwargs: stream())
                with self.assertRaises(adapter.ModelCallStopped) as caught:
                    list(bounded(types.SimpleNamespace(max_retries=0), "report", stream=True))
                self.assertEqual(caught.exception.retryable, retryable)
                self.assertEqual(budget.data["attempts"], 1)
                self.assertEqual(budget.data["failed_attempts"], 1)
                self.assertEqual(closed, [True])


if __name__ == "__main__":
    unittest.main()
