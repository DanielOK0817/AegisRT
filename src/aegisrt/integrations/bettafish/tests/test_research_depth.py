"""Offline behavioral regressions for bounded research and forum refinement."""
import argparse
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("bettafish_depth_adapter", Path(__file__).resolve().parents[1] / "adapter.py")
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


class FakeState:
    def __init__(self, value):
        self.value = copy.deepcopy(value)
        self.query = value["query"]
        self.report_title = value.get("report_title", "Research")
        self.is_completed = value.get("is_completed", False)
        self.final_report = value.get("final_report", "")
        self.paragraphs = []
        for row in value["paragraphs"]:
            research = types.SimpleNamespace(**copy.deepcopy(row["research"]))
            research.mark_completed = lambda target=research: setattr(target, "is_completed", True)
            self.paragraphs.append(types.SimpleNamespace(title=row["title"], content=row.get("content", ""), research=research))

    @classmethod
    def from_dict(cls, value):
        return cls(value)

    def to_dict(self):
        return {"query": self.query, "report_title": self.report_title, "is_completed": self.is_completed,
                "final_report": self.final_report, "paragraphs": [
                    {"title": row.title, "content": row.content, "research": {
                        key: value for key, value in vars(row.research).items() if not callable(value)}} for row in self.paragraphs]}

    def mark_completed(self):
        self.is_completed = True


class ResearchDepthTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.topic = "测试事件"
        self.trace = {"structure": 0, "search_inputs": [], "summary_inputs": [], "queries": [], "config": None}

    def payload(self, **changes):
        goal = {"topic": self.topic, **changes}
        request = {"workflow_id": "depth-case", "task_id": "research", "goal": json.dumps(goal)}
        return request, adapter.validate_request(request)

    def initial_state(self, count=2):
        return {"query": self.topic, "report_title": "事件研究", "is_completed": True, "paragraphs": [
            {"title": f"角度{index}", "content": "核验范围", "research": {"is_completed": True,
                "latest_summary": "初轮已知材料https://example.com/initial", "reflection_iteration": 2,
                "search_history": [{"query": "initial", "title": "初轮来源", "url": "https://example.com/initial", "content": "原有事实"}]}}
            for index in range(count)]}

    def modules(self, role="query"):
        trace, topic = self.trace, self.topic
        class FakeSummary:
            def run(node, data, **kwargs):
                trace["summary_inputs"].append(copy.deepcopy(data))
                return "更新材料" + str(data.get("search_results")) + ("已核验反馈疑点" if data.get("forum_feedback") else "")
            def mutate_state(node, data, state, index):
                state.paragraphs[index].research.latest_summary = node.run(data)
                state.paragraphs[index].research.reflection_iteration += 1
                return state
        class FakeSearch:
            def run(node, data, **kwargs):
                trace["search_inputs"].append(copy.deepcopy(data))
                if data.get("paragraph_latest_state") and trace.get("reflection_decision"):
                    return trace["reflection_decision"]
                query = "反馈疑点独立来源" if data.get("forum_feedback") else "生成角度关键词" + data["title"]
                return {"search_query": query, "reasoning": "核验缺失信息", "search_tool": "basic_search_news"}
        class FakeAgent:
            def __init__(agent, config):
                agent.config = config
                trace["config"] = config
                agent.state = None
                agent.llm_client = types.SimpleNamespace(invoke=lambda *args: None)
                agent.first_search_node, agent.reflection_node = FakeSearch(), FakeSearch()
                agent.first_summary_node, agent.reflection_summary_node = FakeSummary(), FakeSummary()
                agent.report_formatting_node = types.SimpleNamespace(format_report_manually=lambda data, title: "\n".join(row["paragraph_latest_state"] for row in data))
            def _generate_report_structure(agent, query):
                trace["structure"] += 1
                raw = ResearchDepthTests.initial_state(self, agent.config.MAX_PARAGRAPHS)
                raw["is_completed"] = False
                for row in raw["paragraphs"]:
                    row["research"].update(is_completed=False, latest_summary="", search_history=[], reflection_iteration=0)
                agent.state = FakeState(raw)
            def execute_search_tool(agent, tool, query, **kwargs):
                trace["queries"].append(query)
                if tool == "search_news_by_date" and trace.get("date_empty"):
                    return types.SimpleNamespace(query=query, results=[], response_time=0.1)
                row = types.SimpleNamespace(title=query, url="https://example.com/" + str(len(trace["queries"])), content="检索原文", raw_content="检索原文", score=1, published_date=None)
                return types.SimpleNamespace(query=query, results=[row], webpages=[types.SimpleNamespace(name=query, url=row.url, snippet=row.content, date_last_crawled=None)])
            def _step(agent, index, reflection):
                row = agent.state.paragraphs[index]
                data = {"title": row.title, "content": row.content, "paragraph_latest_state": row.research.latest_summary}
                decision = (agent.reflection_node if reflection else agent.first_search_node).run(data)
                dates = {key: decision[key] for key in ("start_date", "end_date") if key in decision}
                response = agent.execute_search_tool(decision["search_tool"], decision["search_query"], **dates)
                source = response.results[0]
                row.research.search_history.append({"query": decision["search_query"], "title": source.title, "url": source.url, "content": source.content})
                data.update(search_query=decision["search_query"], search_results=source.content)
                agent.state = (agent.reflection_summary_node if reflection else agent.first_summary_node).mutate_state(data, agent.state, index)
            def _initial_search_and_summary(agent, index):
                agent._step(index, False)
                agent.state.paragraphs[index].research.reflection_iteration = 0
            def _reflection_loop(agent, index):
                for _ in range(agent.config.MAX_REFLECTIONS):
                    agent._step(index, True)
            def research(agent, query, save_report=False):
                agent._generate_report_structure(query)
                agent._process_paragraphs()
                return agent._generate_final_report()
        package = role.capitalize() + "Engine"
        return {package: types.SimpleNamespace(Settings=lambda **kwargs: types.SimpleNamespace(**kwargs), DeepSearchAgent=FakeAgent, AnspireSearchAgent=FakeAgent),
                package + ".nodes.report_structure_node": types.SimpleNamespace(ReportStructureNode=type("Structure", (), {})),
                package + ".nodes.summary_node": types.SimpleNamespace(FORUM_READER_AVAILABLE=True),
                package + ".nodes.search_node": types.SimpleNamespace(SYSTEM_PROMPT_FIRST_SEARCH="search", SYSTEM_PROMPT_REFLECTION="reflect"),
                package + ".state.state": types.SimpleNamespace(State=FakeState)}

    def run_research(self, payload, request, args):
        imports = self.modules(args.role)
        with patch("socket.socket", side_effect=AssertionError("offline regression opened network")), \
                patch.object(adapter.importlib, "import_module", side_effect=lambda name: imports[name]), \
                patch.object(adapter, "fresh_file_sample", return_value=None):
            return adapter.research_live(args, request, payload, self.root / args.role)

    def test_restored_defaults_allow_five_paragraphs_and_three_insight_reflections(self):
        _, defaults = self.payload()
        self.assertEqual(defaults["max_paragraphs"], 5)
        self.assertEqual(defaults["max_reflections"], 2)
        self.assertEqual(defaults["insight_max_reflections"], 3)
        request, payload = self.payload(max_paragraphs=5, max_reflections=3)
        state, _ = self.run_research(payload, request, argparse.Namespace(role="query", resume_seed=None))
        self.assertEqual(len(state["paragraphs"]), 5)
        self.assertEqual(self.trace["config"].MAX_REFLECTIONS, 2)
        self.assertTrue(all(row["research"]["reflection_iteration"] == 2 for row in state["paragraphs"]))

    def test_insight_executes_three_reflections_and_summary_has_source_urls(self):
        request, payload = self.payload(max_paragraphs=1)
        state, _ = self.run_research(payload, request, argparse.Namespace(role="insight", resume_seed=None, input_dir=None))
        self.assertEqual(state["research_audit"]["paragraphs"][0]["reflections_executed"], 3)
        self.assertEqual(len(self.trace["queries"]), 4)
        self.assertTrue(all(row["search_results"][0]["url"].startswith("https://example.com/") for row in self.trace["summary_inputs"]))

    def test_feedback_drives_real_reflection_search_and_summary_of_every_initial_paragraph(self):
        request, payload = self.payload(max_paragraphs=2, max_reflections=0, research_phase="refinement")
        initial = self.initial_state()
        args = argparse.Namespace(role="query", resume_seed=None, initial_research_state=initial,
                                  initial_research_sha256="a" * 64, forum_feedback="请寻找独立证据核对争议说法")
        state, report = self.run_research(payload, request, args)
        self.assertEqual(self.trace["structure"], 0, "refinement regenerated a structure instead of using the initial state")
        self.assertEqual(self.trace["queries"], ["反馈疑点独立来源"] * 2)
        self.assertTrue(all(row.get("forum_feedback") == args.forum_feedback for row in self.trace["search_inputs"]))
        self.assertTrue(all(row.get("forum_feedback") == args.forum_feedback for row in self.trace["summary_inputs"]))
        self.assertIn("已核验反馈疑点", report)
        self.assertEqual(initial["paragraphs"][0]["research"]["latest_summary"], "初轮已知材料https://example.com/initial")
        audit = state["research_audit"]
        self.assertEqual(audit["phase"], "refinement")
        self.assertEqual(audit["forum_feedback_sha256"], adapter.hashlib.sha256(args.forum_feedback.encode()).hexdigest())
        self.assertTrue(all(row["feedback_consumed"] and row["reflections_executed"] >= 1 for row in audit["paragraphs"]))
        self.assertTrue(all(len(row["research"]["search_history"]) == 2 for row in state["paragraphs"]))
        self.assertEqual(adapter.read_json(self.root / "query/checkpoint.json")["state"]["research_audit"], audit)

    def test_refinement_rejects_missing_feedback_before_model_calls(self):
        request, payload = self.payload(research_phase="refinement")
        args = argparse.Namespace(role="query", resume_seed=None, initial_research_state=self.initial_state(), forum_feedback="")
        with self.assertRaisesRegex(adapter.AdapterError, "feedback"):
            self.run_research(payload, request, args)
        self.assertFalse(self.trace["queries"])

    def test_refinement_rejects_other_topic_and_wrong_resume_feedback(self):
        request, payload = self.payload(research_phase="refinement")
        initial = self.initial_state()
        initial["query"] = "其他事件"
        args = argparse.Namespace(role="query", resume_seed=None, initial_research_state=initial,
                                  initial_research_sha256="a" * 64, forum_feedback="请核验争议")
        with self.assertRaisesRegex(adapter.AdapterError, "topic mismatch"):
            self.run_research(payload, request, args)
        initial["query"] = self.topic
        args.resume_seed = {"checkpoint": {"state": {**initial, "research_audit": {"phase": "refinement",
                            "forum_feedback_sha256": "b" * 64, "initial_research_sha256": "a" * 64}}}}
        with self.assertRaisesRegex(adapter.AdapterError, "feedback or initial state mismatch"):
            self.run_research(payload, request, args)
        self.assertFalse(self.trace["queries"])

    def test_completed_refinement_resume_preserves_audit_without_new_search(self):
        request, payload = self.payload(max_paragraphs=2, research_phase="refinement")
        args = argparse.Namespace(role="query", resume_seed=None, initial_research_state=self.initial_state(),
                                  initial_research_sha256="a" * 64, forum_feedback="请核验争议")
        state, _ = self.run_research(payload, request, args)
        first_count = len(self.trace["queries"])
        args.resume_seed = {"checkpoint": adapter.read_json(self.root / "query/checkpoint.json")}
        resumed, _ = self.run_research(payload, request, args)
        self.assertEqual(first_count, len(self.trace["queries"]))
        self.assertEqual(state["research_audit"], resumed["research_audit"])

    def test_partial_initial_resume_runs_only_remaining_reflections(self):
        request, payload = self.payload(max_paragraphs=1)
        state = self.initial_state(1)
        state["is_completed"] = False
        state["paragraphs"][0]["research"]["is_completed"] = False
        state["paragraphs"][0]["research"]["reflection_iteration"] = 1
        state["research_audit"] = {"phase": "initial", "forum_feedback_sha256": None,
            "initial_research_sha256": None, "reflections_per_paragraph": 2,
            "paragraphs": [{"index": 0, "title": "角度0", "reflections_executed": 1, "search_calls": 2,
                "source_records_before": 0, "source_records_after": 1, "feedback_consumed": False, "queries": []}]}
        args = argparse.Namespace(role="query", resume_seed={"checkpoint": {"state": state}})
        result, _ = self.run_research(payload, request, args)
        self.assertEqual(self.trace["structure"], 0)
        self.assertEqual(len(self.trace["queries"]), 1)
        self.assertEqual(result["research_audit"]["paragraphs"][0]["reflections_executed"], 2)

    def test_focused_sample_selection_uses_generated_query_without_changing_originals(self):
        raw = [{"comment_id": str(index), "content": "价格质疑" if index < 6 else "配送延误", "source_url": "https://example.com/comments"} for index in range(20)]
        agent = types.SimpleNamespace()
        selected, audit = adapter.focus_sample_comments(agent, raw, "商品事件", "配送延误", "用户体验")
        self.assertTrue(selected)
        self.assertTrue(all(row["comment_id"] in {str(index) for index in range(6, 20)} for row in selected))
        self.assertTrue(all(row is raw[int(row["comment_id"])] for row in selected))
        self.assertEqual(audit["query"], "配送延误")

    def test_bounded_source_records_keep_original_urls_and_total_content_budget(self):
        rows = [types.SimpleNamespace(title="新闻", url=f"https://example.com/{index}", content="原文" * 1000) for index in range(20)]
        records = adapter.bounded_source_records(types.SimpleNamespace(results=rows), "query", 1200)
        self.assertEqual(len(records), 12)
        self.assertLessEqual(sum(len(row["content"]) for row in records), 1200)
        self.assertTrue(all(row["content_truncated"] for row in records))
        self.assertEqual([row["url"] for row in records], [row.url for row in rows[:12]])

    def test_model_search_decoder_preserves_tool_and_dates_and_rejects_default_fallback(self):
        decision = {"search_query": "争议核验", "reasoning": "核对独立来源", "search_tool": "search_news_by_date",
                    "start_date": "2026-09-01", "end_date": "2026-09-30"}
        self.assertEqual(adapter.decode_research_search(json.dumps(decision)), decision)
        for value in ({}, {"search_query": ""}, {"search_query": "查询词"}, [decision]):
            with self.subTest(value=value), self.assertRaises(adapter.AdapterError):
                adapter.decode_research_search(json.dumps(value))

    def test_structured_json_mode_reaches_counted_provider_boundary_only_for_selected_prompts(self):
        calls = []
        def recorded(client, role, **kwargs):
            calls.append((client, role, kwargs))
            return "counted-result"
        llms = types.SimpleNamespace(recorded_completion=recorded)
        with patch.object(adapter.importlib, "import_module", return_value=llms):
            adapter.install_research_json_mode("MediaEngine", {"strict summary JSON"})
        client = object()
        self.assertEqual(llms.recorded_completion(client, "media", messages=[{"role": "system", "content": "strict summary JSON"}], stream=True), "counted-result")
        self.assertEqual(calls[0][2]["response_format"], {"type": "json_object"})
        self.assertTrue(calls[0][2]["stream"])
        llms.recorded_completion(client, "media", messages=[{"role": "system", "content": "report structure JSON array"}])
        self.assertNotIn("response_format", calls[1][2])
        self.assertEqual(len(calls), 2)

    def test_malformed_complete_summary_is_archived_and_remains_rejected(self):
        # This is the second live run's exact failure class: literal newline in
        # a string despite a provider finish marker indicating completion.
        malformed = '{"paragraph_latest_state":"第一段\n第二段"}'
        audit = {}
        path = self.root / "model-responses/summary-invalid.txt"
        with self.assertRaisesRegex(adapter.AdapterError, "complete JSON"):
            adapter.archive_research_summary(malformed, path, audit)
        self.assertEqual(path.read_text(encoding="utf-8"), malformed)
        self.assertEqual(audit["summary_responses"][0]["validation"], "rejected")
        self.assertEqual(audit["summary_responses"][0]["sha256"], adapter.digest_file(path))
        with self.assertRaises(adapter.AdapterError):
            adapter.archive_research_summary('{"paragraph_latest_state":"truncated', self.root / "partial.txt", {})

    def test_raw_summary_archive_redacts_credentials_without_losing_parse_result(self):
        credential = "fixture-only-private-credential"
        raw = json.dumps({"paragraph_latest_state": "材料中的" + credential})
        audit = {}
        path = self.root / "credential-summary.txt"
        with patch.dict(adapter.os.environ, {"MEDIA_ENGINE_API_KEY": credential}):
            result = adapter.archive_research_summary(raw, path, audit)
        self.assertIn(credential, result)
        self.assertNotIn(credential, path.read_text(encoding="utf-8"))
        self.assertTrue(audit["summary_responses"][0]["sensitive_values_redacted"])

    def test_legal_empty_date_search_falls_back_once_with_same_query_and_audit(self):
        query = "争议 官方公告"
        dated = types.SimpleNamespace(query=query, results=[], response_time=1.2)
        basic = types.SimpleNamespace(query=query, results=[types.SimpleNamespace(title="原始来源", url="https://example.com/source", content="原文")])
        calls, responses = [], []
        def search(tool, terms, **kwargs):
            calls.append((tool, terms, kwargs))
            return dated if len(calls) == 1 else basic
        response, scope = adapter.search_with_empty_date_fallback(search, "query", "search_news_by_date", query,
            {"start_date": "2025-02-21", "end_date": "2026-10-02"}, lambda value, event: responses.append((value, event)))
        self.assertIs(response, basic)
        self.assertEqual(calls, [("search_news_by_date", query, {"start_date": "2025-02-21", "end_date": "2026-10-02"}), ("basic_search_news", query, {})])
        self.assertTrue(scope["date_constraints_relaxed"])
        self.assertEqual(scope["requested_start_date"], "2025-02-21")
        self.assertEqual(scope["requested_end_date"], "2026-10-02")
        self.assertEqual(len(responses), 2)
        self.assertIs(responses[0][0], dated)

    def test_search_provider_errors_or_invalid_responses_do_not_fallback(self):
        query = "查询词"
        invalid = [types.SimpleNamespace(query=query, results=[], error_message="HTTP 401"),
                   types.SimpleNamespace(query="搜索失败", results=[]),
                   types.SimpleNamespace(query=query, results=None)]
        for original in invalid:
            with self.subTest(response=vars(original)):
                search = unittest.mock.Mock(return_value=original)
                response, scope = adapter.search_with_empty_date_fallback(search, "query", "search_news_by_date", query, {}, lambda *_: None)
                self.assertIs(response, original)
                self.assertEqual(search.call_count, 1)
                self.assertFalse(scope["date_constraints_relaxed"])
                with self.assertRaises(adapter.AdapterError):
                    adapter.bound_search_response(response, "query")

    def test_both_date_and_basic_empty_remain_failure_after_only_two_searches(self):
        query = "不存在的事件"
        search = unittest.mock.Mock(return_value=types.SimpleNamespace(query=query, results=[]))
        response, scope = adapter.search_with_empty_date_fallback(search, "query", "search_news_by_date", query, {}, lambda *_: None)
        self.assertEqual(search.call_count, 2)
        with self.assertRaises(adapter.AdapterError):
            adapter.bound_search_response(response, "query")

    def test_research_date_fallback_preserves_empty_response_and_warns_summary_about_dates(self):
        request, payload = self.payload(max_paragraphs=1, max_reflections=1)
        self.trace["date_empty"] = True
        self.trace["reflection_decision"] = {"search_query": "更新官方公告", "reasoning": "核验后续更新", "search_tool": "search_news_by_date",
                                              "start_date": "2025-02-21", "end_date": "2026-10-02"}
        state, _ = self.run_research(payload, request, argparse.Namespace(role="query", resume_seed=None))
        audit = state["research_audit"]
        self.assertEqual(len(self.trace["queries"]), 3)
        self.assertEqual(self.trace["queries"][-2:], ["更新官方公告"] * 2)
        self.assertTrue(audit["search_scope_changes"][0]["date_constraints_relaxed"])
        self.assertTrue(self.trace["summary_inputs"][-1]["search_scope"]["date_constraints_relaxed"])
        self.assertIn("不得", self.trace["summary_inputs"][-1]["date_constraint_caution"])
        dated = audit["search_responses"][1]
        self.assertEqual(dated["result_count"], 0)
        original = adapter.read_json(Path(dated["path"]))
        self.assertEqual(original["results"], [])
        self.assertEqual(original["parameters"], {"start_date": "2025-02-21", "end_date": "2026-10-02"})


if __name__ == "__main__":
    unittest.main()
