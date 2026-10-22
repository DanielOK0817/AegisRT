"""Offline search recovery: one shared retry, real requests exception types."""
import argparse
import copy
from functools import wraps
import hashlib
import importlib.util
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import Mock, call, patch

import requests


spec = importlib.util.spec_from_file_location(
    "search_recovery_depth_fixture", Path(__file__).with_name("test_research_depth.py"))
depth = importlib.util.module_from_spec(spec)
spec.loader.exec_module(depth)
adapter = depth.adapter
SOURCE = Path(__file__).resolve().parents[4] / "reproductions/bettafish-20261001/source"


def http_error(status):
    response = requests.Response()
    response.status_code = status
    return requests.exceptions.HTTPError("offline HTTP failure", response=response)


class SearchTransientRecoveryTests(unittest.TestCase):
    def setUp(self):
        no_network = patch("socket.socket", side_effect=AssertionError("offline search test opened a socket"))
        no_network.start()
        self.addCleanup(no_network.stop)
        self.events = []
        self.query = "给定原文的补搜关键词"
        self.parameters = {"start_date": "2024-08-20", "end_date": "2024-08-21"}

    def response(self, role="query", *, empty=False, query=None, error_message=None):
        field = "webpages" if role == "media" else "results"
        records = [] if empty else [types.SimpleNamespace(title="source", name="source",
            url="https://example.com/source", content="原始摘录", snippet="原始摘录")]
        return types.SimpleNamespace(query=self.query if query is None else query,
                                     error_message=error_message, **{field: records})

    def run_search(self, effects, *, tool="search_news_by_date", role="query", parameters=None):
        self.search = Mock(side_effect=effects)
        if parameters is None:
            parameters = copy.deepcopy(self.parameters)
        self.supplied_parameters = copy.deepcopy(parameters)
        result = adapter.search_with_empty_date_fallback(self.search, role, tool, self.query, parameters,
            lambda response, event: self.events.append({"response": response, "event": copy.deepcopy(event)}),
            media_window_recovery=role == "media")
        self.assertEqual(parameters, self.supplied_parameters, "search recovery mutated the requested scope")
        return result

    def actual_media_search_module(self):
        logger = types.SimpleNamespace(**{name: lambda *args, **kwargs: None
                                         for name in ("info", "warning", "error", "exception")})
        retry_spec = importlib.util.spec_from_file_location("offline_real_retry_helper", SOURCE / "utils/retry_helper.py")
        retry = importlib.util.module_from_spec(retry_spec)
        with patch.dict(sys.modules, {"loguru": types.SimpleNamespace(logger=logger)}):
            retry_spec.loader.exec_module(retry)
        retry.SEARCH_API_RETRY_CONFIG.max_retries = 0  # Matches production boundary; no hidden waits.
        retry.logger = logger
        search_spec = importlib.util.spec_from_file_location("offline_real_media_search", SOURCE / "MediaEngine/tools/search.py")
        search = importlib.util.module_from_spec(search_spec)
        settings = types.SimpleNamespace(BOCHA_BASE_URL="https://offline.invalid/bocha",
                                         ANSPIRE_BASE_URL="https://offline.invalid/anspire",
                                         BOCHA_WEB_SEARCH_API_KEY=None, ANSPIRE_API_KEY=None)
        with patch.dict(sys.modules, {"retry_helper": retry, "config": types.SimpleNamespace(settings=settings),
                                      "loguru": types.SimpleNamespace(logger=logger), search_spec.name: search}), \
                patch.object(sys, "path", list(sys.path)):
            search_spec.loader.exec_module(search)
        search.logger = logger
        return search

    def test_real_requests_exception_ancestry_and_http_status_classification(self):
        cases = [(requests.exceptions.Timeout("offline timeout"), True),
                 (requests.exceptions.ReadTimeout("offline read timeout"), True),
                 (requests.exceptions.ConnectTimeout("offline connect timeout"), True),
                 (requests.exceptions.ConnectionError("offline connection error"), True),
                 (TimeoutError("offline builtin timeout"), True), (ConnectionError("offline builtin connection"), True),
                 (requests.exceptions.InvalidURL("offline invalid URL"), False),
                 (requests.exceptions.MissingSchema("offline invalid schema"), False),
                 (RuntimeError("unknown failure"), False)]
        cases += [(http_error(status), retryable) for status, retryable in
                  ((400, False), (401, False), (403, False), (404, False),
                   (408, True), (409, True), (429, True), (500, True), (503, True), (599, True))]
        for error, expected in cases:
            with self.subTest(type=type(error).__name__, status=getattr(error.response, "status_code", None)
                              if isinstance(error, requests.exceptions.RequestException) else None):
                self.assertIs(adapter.transient_search_error(error), expected)

    def test_one_temporary_failure_retries_same_tool_query_and_parameters(self):
        success = self.response()
        result, scope = self.run_search([requests.exceptions.ReadTimeout("offline read timeout"), success])
        self.assertIs(result, success)
        self.assertEqual(self.search.call_args_list, [call("search_news_by_date", self.query, **self.parameters)] * 2)
        self.assertEqual(scope["transient_retries"], 1)
        self.assertEqual(scope["search_attempts"], 2)
        self.assertFalse(scope["date_constraints_relaxed"])
        self.assertEqual([row["event"]["request_attempt"] for row in self.events], [1, 2])
        self.assertEqual([row["event"]["tool_attempt"] for row in self.events], [1, 2])
        first = self.events[0]["event"]
        self.assertEqual(first["error_type"], "ReadTimeout")
        self.assertEqual(first["error_module"], "requests.exceptions")
        self.assertIsNone(first["http_status"])
        self.assertIs(first["retryable"], True)
        self.assertEqual(first["parameters"], self.parameters)
        self.assertIsNone(self.events[0]["response"])

    def test_http_error_status_is_archived_and_retryable_503_recovers_once(self):
        success = self.response()
        _, scope = self.run_search([http_error(503), success])
        self.assertEqual(scope["transient_retries"], 1)
        self.assertEqual(self.events[0]["event"]["error_type"], "HTTPError")
        self.assertEqual(self.events[0]["event"]["http_status"], 503)
        self.assertIs(self.events[0]["event"]["retryable"], True)

    def test_two_temporary_failures_stop_without_a_third_attempt_or_date_relaxation(self):
        first, second = requests.exceptions.Timeout("offline timeout"), requests.exceptions.ReadTimeout("again")
        with self.assertRaises(requests.exceptions.ReadTimeout):
            self.run_search([first, second, self.response()])
        self.assertEqual(self.search.call_count, 2)
        self.assertEqual([row["event"]["request_attempt"] for row in self.events], [1, 2])
        self.assertTrue(all(row["event"]["tool"] == "search_news_by_date" for row in self.events))
        self.assertTrue(all(row["response"] is None for row in self.events))

    def test_permanent_http_and_unknown_errors_do_not_retry_or_relax_dates(self):
        for error in (http_error(400), http_error(401), http_error(403), RuntimeError("unknown failure")):
            with self.subTest(error=type(error).__name__):
                self.events = []
                with self.assertRaises(type(error)):
                    self.run_search([error, self.response()])
                self.assertEqual(self.search.call_count, 1)
                self.assertEqual(self.events[0]["event"]["request_attempt"], 1)
                self.assertIs(self.events[0]["event"]["retryable"], False)

    def test_successful_empty_date_window_and_one_fallback_retry_have_separate_records(self):
        empty, success = self.response(empty=True), self.response()
        result, scope = self.run_search([empty, requests.exceptions.ConnectionError("offline fallback failure"), success])
        self.assertIs(result, success)
        self.assertEqual(self.search.call_args_list, [call("search_news_by_date", self.query, **self.parameters),
                                                     call("basic_search_news", self.query), call("basic_search_news", self.query)])
        self.assertEqual(scope["search_attempts"], 3)
        self.assertEqual(scope["transient_retries"], 1)
        self.assertTrue(scope["date_constraints_relaxed"])
        self.assertEqual(scope["requested_start_date"], self.parameters["start_date"])
        self.assertEqual(scope["requested_end_date"], self.parameters["end_date"])
        self.assertEqual([row["event"]["request_attempt"] for row in self.events], [1, 2, 3])
        self.assertEqual([row["event"]["tool_attempt"] for row in self.events], [1, 1, 2])
        self.assertIs(self.events[0]["response"], empty)
        self.assertIsNone(self.events[1]["response"])
        self.assertIs(self.events[2]["response"], success)

    def test_retry_allowance_is_shared_with_the_date_fallback_not_reset(self):
        empty = self.response(empty=True)
        failure = requests.exceptions.ReadTimeout("offline final failure")
        with self.assertRaises(requests.exceptions.ReadTimeout):
            self.run_search([requests.exceptions.Timeout("offline initial failure"), empty, failure, self.response()])
        self.assertEqual(self.search.call_count, 3)
        self.assertEqual([row["event"]["tool"] for row in self.events],
                         ["search_news_by_date", "search_news_by_date", "basic_search_news"])
        self.assertEqual([row["event"]["request_attempt"] for row in self.events], [1, 2, 3])
        self.assertEqual([row["event"]["tool_attempt"] for row in self.events], [1, 2, 1])

    def test_unexplained_failed_sentinel_or_response_error_is_not_successful_empty(self):
        for response in (self.response(empty=True, query="搜索失败"), self.response(query="搜索失败"),
                         self.response(empty=True, error_message="HTTP 401")):
            with self.subTest(query=response.query, error=response.error_message):
                self.events = []
                actual, scope = self.run_search([response, self.response()])
                self.assertIs(actual, response)
                self.assertEqual(self.search.call_count, 1)
                self.assertEqual(scope["transient_retries"], 0)
                self.assertFalse(scope["date_constraints_relaxed"])
                with self.assertRaises(adapter.AdapterError):
                    adapter.bound_search_response(actual, "query")

    def test_actual_graceful_Anspire_method_is_unwrapped_only_on_this_instance(self):
        source = self.actual_media_search_module()
        agency = source.AnspireAISearch(api_key="offline-test-value")
        other = source.AnspireAISearch(api_key="offline-test-value")
        wrapped = type(agency)._search_internal
        error = requests.exceptions.Timeout("offline real method timeout")
        with patch.object(source.requests, "get", side_effect=error):
            self.assertEqual(agency.comprehensive_search(self.query).query, "搜索失败")
            self.assertIs(adapter.install_search_failure_boundary(types.SimpleNamespace(search_agency=agency)), True)
            self.assertIs(type(agency)._search_internal, wrapped)
            with self.assertRaises(requests.exceptions.Timeout) as raised:
                agency.comprehensive_search(self.query)
            self.assertIs(raised.exception, error)
            self.assertEqual(other.comprehensive_search(self.query).query, "搜索失败")

    def test_instance_boundary_removes_exactly_one_decorator_layer(self):
        layers = []
        def layer(name):
            def decorator(fn):
                @wraps(fn)
                def wrapper(*args, **kwargs):
                    layers.append(name)
                    return fn(*args, **kwargs)
                return wrapper
            return decorator
        class Agency:
            @layer("outer")
            @layer("inner")
            def _search_internal(self, **kwargs):
                layers.append("body")
                return kwargs
        agency = Agency()
        wrapped = Agency._search_internal
        self.assertTrue(adapter.install_search_failure_boundary(types.SimpleNamespace(search_agency=agency)))
        self.assertIs(Agency._search_internal, wrapped)
        self.assertEqual(agency._search_internal(query=self.query, top_k=3), {"query": self.query, "top_k": 3})
        self.assertEqual(layers, ["inner", "body"])
        layers.clear()
        self.assertFalse(adapter.install_search_failure_boundary(types.SimpleNamespace(search_agency=agency)))
        Agency()._search_internal(query=self.query)
        self.assertEqual(layers, ["outer", "inner", "body"])

    def test_agent_without_wrapped_search_method_keeps_existing_interface(self):
        self.assertIs(adapter.install_search_failure_boundary(types.SimpleNamespace()), False)
        self.assertIs(adapter.install_search_failure_boundary(types.SimpleNamespace(search_agency=object())), False)

    def test_research_live_installs_boundary_and_archives_real_timeout_before_recovery(self):
        fixture = depth.ResearchDepthTests(methodName="runTest")
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        request, payload = fixture.payload(max_paragraphs=1, max_reflections=0)
        imports = fixture.modules("media")
        cls = imports["MediaEngine"].DeepSearchAgent
        initialize = cls.__init__
        source = self.actual_media_search_module()
        received = []

        def init(agent, config):
            initialize(agent, config)
            agent.search_agency = source.AnspireAISearch(api_key="offline-test-value")
            received.append(agent)
            agent.first_search_node.run = lambda data: {"search_query": self.query,
                "search_tool": "comprehensive_search", "reasoning": "核查原摘录"}

        def execute(agent, tool, query, **kwargs):
            response = getattr(agent.search_agency, tool)(query, **kwargs)
            records = [types.SimpleNamespace(title=row.name, url=row.url, content=row.snippet) for row in response.webpages]
            return types.SimpleNamespace(query=response.query, results=records, webpages=response.webpages)

        success = types.SimpleNamespace(raise_for_status=lambda: None, json=lambda: {"results": [
            {"title": "给定来源", "url": "https://example.com/source", "content": "原始可见摘录"}]})
        with patch.object(cls, "__init__", init), patch.object(cls, "execute_search_tool", execute), \
                patch.object(adapter.importlib, "import_module", side_effect=lambda name: imports[name]), \
                patch.object(source.requests, "get", side_effect=[requests.exceptions.ReadTimeout("offline real timeout"), success]) as provider:
            state, _ = adapter.research_live(argparse.Namespace(role="media", resume_seed=None),
                                            request, payload, fixture.root / "media")
        self.assertTrue(state["is_completed"])
        self.assertEqual(provider.call_count, 2)
        self.assertEqual(provider.call_args_list[0], provider.call_args_list[1])
        scope = fixture.trace["summary_inputs"][0]["search_scope"]
        self.assertEqual(scope["search_attempts"], 2)
        self.assertEqual(scope["transient_retries"], 1)
        records = state["research_audit"]["search_responses"]
        self.assertEqual(len(records), 2)
        self.assertEqual([row["provider_error_present"] for row in records], [True, False])
        saved = [adapter.read_json(Path(row["path"])) for row in records]
        self.assertEqual([row["request_attempt"] for row in saved], [1, 2])
        self.assertEqual(saved[0]["error_type"], "ReadTimeout")
        self.assertEqual(saved[0]["error_module"], "requests.exceptions")
        self.assertTrue(saved[0]["retryable"])
        self.assertIsNone(saved[0]["http_status"])
        self.assertEqual(saved[1]["results"][0]["snippet"], "原始可见摘录")
        for row in records:
            self.assertEqual(row["sha256"], hashlib.sha256(Path(row["path"]).read_bytes()).hexdigest())


if __name__ == "__main__":
    unittest.main()
