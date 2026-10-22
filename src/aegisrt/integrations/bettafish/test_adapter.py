"""Offline checks for file ownership, protocol output, caching and API budgets."""
import argparse
import ast
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import shutil
import subprocess
import sys
import tempfile
import time
import types
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("bettafish_adapter_under_test", Path(__file__).with_name("adapter.py"))
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


class AdapterTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "source"
        self.inputs = self.root / "inputs"
        self.run = self.root / "run"
        self.inputs.mkdir()
        (self.source / "ReportEngine").mkdir(parents=True)
        upstream = Path(__file__).resolve().parents[3] / "reproductions/bettafish-20261001/source/ReportEngine/sample_report.py"
        shutil.copyfile(upstream, self.source / "ReportEngine/sample_report.py")
        self.topic = "测试话题"
        manifest = {"topic": self.topic, "inputs": {}}
        for role in adapter.RESEARCH_ROLES:
            state = {"query": self.topic, "is_completed": True,
                     "paragraphs": [{"title": "来源核验", "research": {"is_completed": True,
                                      "search_history": [{"url": "https://example.com/" + role, "title": "可核查来源"}]}}]}
            report = self.inputs / (role + ".md")
            path = self.inputs / (role + ".json")
            report.write_text("本轮" + role + "分析结果", encoding="utf-8")
            adapter.atomic_json(path, state)
            manifest["inputs"][role] = {"report": report.name, "state": path.name,
                                       "report_sha256": adapter.digest_file(report), "state_sha256": adapter.digest_file(path)}
        comments = [{"comment_id": "original-a", "content": "原始样本甲", "nickname": "甲", "like_count": 3,
                     "source_url": "https://www.bilibili.com/video/fixture"},
                    {"comment_id": "original-b", "content": "原始样本乙", "nickname": "乙", "like_count": 4,
                     "source_url": "https://www.bilibili.com/video/fixture"}]
        adapter.atomic_json(self.inputs / "comments.json", comments)
        adapter.atomic_json(self.inputs / "provenance.json", {"topic": self.topic, "sampling": "test fixture"})
        manifest["comments_sha256"] = adapter.digest_file(self.inputs / "comments.json")
        adapter.atomic_json(self.inputs / "manifest.json", manifest)

    def args(self, role, mode="replay"):
        return argparse.Namespace(role=role, mode=mode, source_dir=str(self.source), run_dir=str(self.run),
                                  input_dir=str(self.inputs), env_file=None, resume_dir=None)

    def request(self, role, context=""):
        return {"task_id": "case-" + role, "workflow_id": "case", "attempt_id": "attempt-one", "attempt_number": 1,
                "goal": json.dumps({"topic": self.topic, "max_paragraphs": 1}, ensure_ascii=False), "context": context}

    def research_results(self):
        return [adapter.execute(self.args(role), self.request(role)) for role in adapter.RESEARCH_ROLES]

    @staticmethod
    def context(manifests):
        return "\n\n".join(adapter.canonical(item) for item in manifests)

    def test_offline_five_role_flow_uses_current_files_without_network(self):
        with patch("socket.socket", side_effect=AssertionError("offline mode opened a socket")):
            dependencies = self.research_results()
            forum = adapter.execute(self.args("forum"), self.request("forum", self.context(dependencies)))
            report = adapter.execute(self.args("report"), self.request("report", self.context(dependencies + [forum])))
        audit = adapter.read_json(Path(report["files"]["audit"]["path"]))
        self.assertEqual(audit["validation"]["research_reports_consumed"], list(adapter.RESEARCH_ROLES))
        self.assertTrue(audit["validation"]["forum_consumed"])
        self.assertEqual(len(audit["evidence"]), 2)
        page = Path(report["files"]["report"]["path"]).read_text(encoding="utf-8")
        self.assertIn("原始样本甲", page)
        self.assertIn("模式为replay", page)
        self.assertIn("未调用模型", page)
        self.assertNotIn("也尚未接入AegisRT", page)
        for result in dependencies + [forum, report]:
            self.assertEqual(result["mode"], "replay")
            for record in result["files"].values():
                self.assertEqual(adapter.digest_file(Path(record["path"])), record["sha256"])
            state = adapter.read_json(Path(result["files"]["state"]["path"]))
            self.assertEqual(state["status"], "completed")
            self.assertEqual(state["model_requests"]["attempts"], 0)

    def test_main_outputs_exactly_one_protocol_envelope(self):
        stdout, stderr = io.StringIO(), io.StringIO()
        argv = ["--role", "query", "--source-dir", str(self.source), "--run-dir", str(self.run),
                "--mode", "replay", "--input-dir", str(self.inputs)]
        with patch.object(sys, "stdin", io.StringIO(json.dumps(self.request("query")))), patch.object(sys, "stdout", stdout), patch.object(sys, "stderr", stderr):
            self.assertEqual(adapter.main(argv), 0)
        envelope = json.loads(stdout.getvalue())
        self.assertEqual(envelope["version"], "aegisrt.agent-output/v1")
        self.assertEqual(len(envelope["artifacts"]), 1)
        self.assertEqual(envelope["artifacts"][0]["visibility"], "workflow")
        self.assertEqual(json.loads(envelope["artifacts"][0]["content"])["role"], "query")

    def test_cp936_subprocess_reads_and_outputs_utf8_protocol(self):
        command = [sys.executable, str(Path(adapter.__file__).resolve()), "--role", "query", "--source-dir", str(self.source),
                   "--run-dir", str(self.run), "--mode", "replay", "--input-dir", str(self.inputs)]
        result = subprocess.run(command, input=json.dumps(self.request("query"), ensure_ascii=False).encode("utf-8"),
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15,
                                env={**os.environ, "PYTHONIOENCODING": "cp936"},
                                creationflags=0x08000000 if os.name == "nt" else 0)
        self.assertEqual(result.returncode, 0, result.stderr.decode("utf-8"))
        result_manifest = json.loads(json.loads(result.stdout.decode("utf-8"))["artifacts"][0]["content"])
        self.assertEqual(result_manifest["topic"], self.topic)

    def test_failed_main_emits_no_success_json(self):
        stdout, stderr = io.StringIO(), io.StringIO()
        argv = ["--role", "query", "--source-dir", str(self.source), "--run-dir", str(self.run), "--mode", "replay"]
        with patch.object(sys, "stdin", io.StringIO(json.dumps(self.request("query")))), patch.object(sys, "stdout", stdout), patch.object(sys, "stderr", stderr):
            self.assertEqual(adapter.main(argv), 1)
        self.assertEqual(stdout.getvalue(), "")
        self.assertIn("AdapterError", stderr.getvalue())

    def test_completed_task_reuses_cache_across_attempt_ids(self):
        first = adapter.execute(self.args("query"), self.request("query"))
        request = self.request("query")
        request.update(attempt_id="attempt-two", attempt_number=2)
        second = adapter.execute(self.args("query"), request)
        self.assertEqual(first, second)

    def test_completed_task_rejects_different_input(self):
        adapter.execute(self.args("query"), self.request("query"))
        request = self.request("query")
        request["goal"] = json.dumps({"topic": self.topic, "max_paragraphs": 2})
        with self.assertRaisesRegex(adapter.AdapterError, "different input"):
            adapter.execute(self.args("query"), request)

    def test_cached_task_detects_output_tampering(self):
        first = adapter.execute(self.args("query"), self.request("query"))
        Path(first["files"]["report"]["path"]).write_text("tampered", encoding="utf-8")
        with self.assertRaisesRegex(adapter.AdapterError, "checksum"):
            adapter.execute(self.args("query"), self.request("query"))

    def test_dependency_must_belong_to_same_workflow(self):
        results = self.research_results()
        results[0]["workflow_id"] = "other-run"
        with self.assertRaisesRegex(adapter.AdapterError, "mismatch"):
            adapter.execute(self.args("forum"), self.request("forum", self.context(results)))

    def test_dependency_must_match_mode(self):
        results = self.research_results()
        results[0]["mode"] = "live"
        with self.assertRaisesRegex(adapter.AdapterError, "mismatch"):
            adapter.execute(self.args("forum"), self.request("forum", self.context(results)))

    def test_duplicate_and_missing_dependency_roles_fail(self):
        results = self.research_results()
        with self.assertRaisesRegex(adapter.AdapterError, "duplicate"):
            adapter.execute(self.args("forum"), self.request("forum", self.context(results + results[:1])))
        with self.assertRaisesRegex(adapter.AdapterError, "missing dependency"):
            adapter.execute(self.args("forum"), self.request("forum", self.context(results[:2])))

    def test_dependency_hash_and_task_directory_enforced(self):
        results = self.research_results()
        original = copy.deepcopy(results)
        outside = self.root / "outside.md"
        outside.write_text("outside content", encoding="utf-8")
        results[0]["files"]["report"] = adapter.file_record(outside)
        with self.assertRaisesRegex(adapter.AdapterError, "outside this run"):
            adapter.execute(self.args("forum"), self.request("forum", self.context(results)))
        original[0]["files"]["report"]["sha256"] = "0" * 64
        with self.assertRaisesRegex(adapter.AdapterError, "checksum"):
            adapter.execute(self.args("forum"), self.request("forum", self.context(original)))

    def test_incomplete_or_zero_source_research_is_rejected(self):
        state = adapter.read_json(self.inputs / "query.json")
        state["paragraphs"][0]["research"]["search_history"] = []
        with self.assertRaisesRegex(adapter.AdapterError, "no source"):
            adapter.validate_research_state(state, self.topic)
        state["is_completed"] = False
        with self.assertRaisesRegex(adapter.AdapterError, "incomplete"):
            adapter.validate_research_state(state, self.topic)

    def test_frozen_comment_checksum_and_identity_are_checked(self):
        path = self.inputs / "comments.json"
        raw = adapter.read_json(path)
        raw[0]["content"] = "changed"
        adapter.atomic_json(path, raw)
        with self.assertRaisesRegex(adapter.AdapterError, "comments checksum"):
            adapter.execute(self.args("insight"), self.request("insight"))
        raw[1]["comment_id"] = raw[0]["comment_id"]
        with self.assertRaisesRegex(adapter.AdapterError, "duplicate"):
            adapter.normalize_comments(raw)

    def test_path_traversal_and_unbounded_goal_are_rejected(self):
        request = self.request("query")
        request["task_id"] = "../outside"
        with self.assertRaises(adapter.AdapterError):
            adapter.execute(self.args("query"), request)
        request = self.request("query")
        request["goal"] = json.dumps({"topic": self.topic, "max_paragraphs": 100})
        with self.assertRaises(adapter.AdapterError):
            adapter.execute(self.args("query"), request)

    def test_budget_counts_keyword_optimizer_and_blocks_before_next_api_call(self):
        calls = []
        budget = adapter.ModelBudget(self.root / "budget.json", "input-hash", 2, 120)
        client = types.SimpleNamespace(max_retries=10)
        def original(client, agent, **kwargs):
            calls.append((agent, kwargs))
            return types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="stop")])
        bounded = budget.wrap(original)
        bounded(client, "insight", timeout=900, max_tokens=9000)
        bounded(client, "keyword_optimizer")
        with self.assertRaises(adapter.ModelCallStopped):
            bounded(client, "insight")
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[0][1]["timeout"], 120)
        self.assertEqual(calls[0][1]["max_tokens"], 2000)
        self.assertEqual(client.max_retries, 0)
        self.assertEqual(budget.data["agents"], {"insight": 1, "keyword_optimizer": 1})
        persisted = adapter.ModelBudget(self.root / "budget.json", "input-hash", 2, 120)
        with self.assertRaises(adapter.ModelCallStopped):
            persisted.wrap(original)(client, "insight")
        self.assertEqual(len(calls), 2)

    def test_provider_error_cannot_be_hidden_by_upstream_exception_fallback(self):
        budget = adapter.ModelBudget(self.root / "budget.json", "input-hash", 2, 120)
        def original(*args, **kwargs):
            raise RuntimeError("private-provider-message")
        fallback_was_used = False
        with self.assertRaises(adapter.ModelCallStopped):
            try:
                budget.wrap(original)(types.SimpleNamespace(), "keyword_optimizer")
            except Exception:
                fallback_was_used = True
        self.assertFalse(fallback_was_used)
        self.assertEqual(budget.data["failed_attempts"], 1)
        self.assertNotIn("private-provider-message", (self.root / "budget.json").read_text())

    def test_stream_errors_and_output_truncation_are_failures(self):
        budget = adapter.ModelBudget(self.root / "budget.json", "input-hash", 2, 120)
        def broken_stream():
            yield types.SimpleNamespace(choices=[])
            raise RuntimeError("stream disconnected")
        stream = budget.wrap(lambda *args, **kwargs: broken_stream())(types.SimpleNamespace(), "query", stream=True)
        with self.assertRaises(adapter.ModelCallStopped):
            list(stream)
        truncated = types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="length")])
        with self.assertRaises(adapter.ModelCallStopped):
            budget.wrap(lambda *args, **kwargs: truncated)(types.SimpleNamespace(), "query")

    def test_valid_media_webpages_are_accepted_and_bounded(self):
        pages = [types.SimpleNamespace(url="https://example.com/" + str(index)) for index in range(20)]
        response = types.SimpleNamespace(webpages=pages, images=["retained-image"], modal_cards=["retained-card"])
        bounded = adapter.bound_search_response(response, "media")
        self.assertIs(bounded, response)
        self.assertEqual(bounded.webpages, pages[:12])
        self.assertEqual(bounded.images, ["retained-image"])
        self.assertEqual(bounded.modal_cards, ["retained-card"])
        self.assertFalse(hasattr(bounded, "results"))

    def test_actual_upstream_media_response_dataclasses_are_accepted(self):
        # Load only data declarations; importing the full source would require
        # provider configuration and external libraries in this offline suite.
        source = Path(__file__).resolve().parents[3] / "reproductions/bettafish-20261001/source/MediaEngine/tools/search.py"
        parsed = ast.parse(source.read_text(encoding="utf-8-sig"))
        names = {"WebpageResult", "ImageResult", "ModalCardResult", "BochaResponse", "AnspireResponse"}
        declarations = [node for node in parsed.body if isinstance(node, ast.ClassDef) and node.name in names]
        imports = ast.parse("from dataclasses import dataclass, field\nfrom typing import Optional, Dict, Any, List\n").body
        module = ast.fix_missing_locations(ast.Module(body=imports + declarations, type_ignores=[]))
        namespace = {"__name__": __name__}
        exec(compile(module, str(source), "exec"), namespace)
        page = namespace["WebpageResult"](name="source", url="https://example.com/source", snippet="source excerpt")
        for response_name in ("AnspireResponse", "BochaResponse"):
            with self.subTest(response=response_name):
                response = namespace[response_name](query="fixture", webpages=[page])
                self.assertIs(adapter.bound_search_response(response, "media"), response)
                self.assertEqual(response.webpages, [page])
                self.assertFalse(hasattr(response, "results"))

    def test_query_and_insight_search_results_are_accepted_and_bounded(self):
        for role in ("query", "insight"):
            with self.subTest(role=role):
                records = [types.SimpleNamespace(url="https://example.com/" + str(index)) for index in range(20)]
                response = types.SimpleNamespace(results=records, parameters={"query": "fixture"})
                bounded = adapter.bound_search_response(response, role)
                self.assertIs(bounded, response)
                self.assertEqual(bounded.results, records[:12])
                self.assertEqual(bounded.parameters, {"query": "fixture"})
                self.assertFalse(hasattr(bounded, "webpages"))

    def test_empty_search_and_error_responses_are_rejected(self):
        for role, field in (("media", "webpages"), ("query", "results"), ("insight", "results")):
            with self.subTest(role=role):
                with self.assertRaises(adapter.AdapterError):
                    adapter.bound_search_response(types.SimpleNamespace(**{field: []}), role)
                with self.assertRaises(adapter.AdapterError):
                    adapter.bound_search_response(types.SimpleNamespace(**{field: ["one"], "error_message": "search failed"}), role)
                with self.assertRaises(adapter.AdapterError):
                    adapter.bound_search_response(None, role)

    def resume_fixture(self):
        folder = self.root / "resume"
        checkpoint = folder / "media/checkpoint.json"
        state = adapter.read_json(self.inputs / "media.json")
        state["is_completed"] = False  # A completed paragraph is reusable before final formatting.
        state["paragraphs"][0]["research"]["latest_summary"] = "已完成的实际摘要，保留来源与不确定性。"
        seed = {"workflow_id": "previous-case", "task_id": "previous-case-media", "role": "media", "mode": "live", "state": state}
        adapter.atomic_json(checkpoint, seed)
        config_hash = "a" * 64
        manifest = {"topic": self.topic, "mode": "live", "configuration_sha256": config_hash,
                    "source_workflow_id": "previous-case", "roles": {"media": {"path": "media/checkpoint.json", "sha256": adapter.digest_file(checkpoint)}}}
        adapter.atomic_json(folder / "manifest.json", manifest)
        args = self.args("media", "live")
        args.resume_dir = str(folder)
        return args, manifest, checkpoint, config_hash

    def test_resume_is_optional_and_valid_explicit_seed_is_accepted(self):
        self.assertIsNone(adapter.load_resume_seed(self.args("media", "live"), {"topic": self.topic, "max_paragraphs": 1}, "a" * 64))
        args, manifest, checkpoint, config_hash = self.resume_fixture()
        result = adapter.load_resume_seed(args, {"topic": self.topic, "max_paragraphs": 1}, config_hash)
        self.assertIsInstance(result, dict)
        self.assertTrue(result)
        self.assertEqual(result["checkpoint"]["role"], "media")
        self.assertEqual(result["checkpoint"]["state"]["query"], self.topic)
        self.assertEqual(result["file"]["sha256"], adapter.digest_file(checkpoint))

    def test_resume_wrong_topic_mode_config_and_checksum_are_rejected(self):
        args, original, checkpoint, config_hash = self.resume_fixture()
        for field, changed in (("topic", "其他话题"), ("mode", "replay"), ("configuration_sha256", "b" * 64)):
            with self.subTest(field=field):
                manifest = copy.deepcopy(original)
                manifest[field] = changed
                adapter.atomic_json(Path(args.resume_dir) / "manifest.json", manifest)
                with self.assertRaises(adapter.AdapterError):
                    adapter.load_resume_seed(args, {"topic": self.topic, "max_paragraphs": 1}, config_hash)
        manifest = copy.deepcopy(original)
        manifest["roles"]["media"]["sha256"] = "0" * 64
        adapter.atomic_json(Path(args.resume_dir) / "manifest.json", manifest)
        with self.assertRaises(adapter.AdapterError):
            adapter.load_resume_seed(args, {"topic": self.topic, "max_paragraphs": 1}, config_hash)

    def test_resume_checkpoint_identity_and_state_topic_are_rejected(self):
        args, original_manifest, checkpoint, config_hash = self.resume_fixture()
        original = adapter.read_json(checkpoint)
        changes = [("role", "query"), ("mode", "replay"), ("workflow_id", "unrelated-case"), ("state_topic", "其他话题")]
        for field, value in changes:
            with self.subTest(field=field):
                seed = copy.deepcopy(original)
                if field == "state_topic":
                    seed["state"]["query"] = value
                else:
                    seed[field] = value
                adapter.atomic_json(checkpoint, seed)
                manifest = copy.deepcopy(original_manifest)
                manifest["roles"]["media"]["sha256"] = adapter.digest_file(checkpoint)
                adapter.atomic_json(Path(args.resume_dir) / "manifest.json", manifest)
                with self.assertRaises(adapter.AdapterError):
                    adapter.load_resume_seed(args, {"topic": self.topic, "max_paragraphs": 1}, config_hash)

    def test_resume_path_outside_explicit_directory_is_rejected(self):
        args, manifest, checkpoint, config_hash = self.resume_fixture()
        outside = self.root / "outside-checkpoint.json"
        shutil.copyfile(checkpoint, outside)
        manifest["roles"]["media"] = {"path": "../outside-checkpoint.json", "sha256": adapter.digest_file(outside)}
        adapter.atomic_json(Path(args.resume_dir) / "manifest.json", manifest)
        with self.assertRaises(adapter.AdapterError):
            adapter.load_resume_seed(args, {"topic": self.topic, "max_paragraphs": 1}, config_hash)

    def test_completed_resume_paragraph_is_formatted_without_any_new_api_call(self):
        args, manifest, checkpoint, config_hash = self.resume_fixture()
        request = self.request("media")
        payload = adapter.validate_request(request)
        args.resume_seed = adapter.load_resume_seed(args, payload, config_hash)
        observed = []
        def unexpected_model_call(*args, **kwargs):
            raise AssertionError("completed resume paragraph made an API call")
        class FakeState:
            def __init__(self, value):
                self.value = copy.deepcopy(value)
                self.is_completed = value.get("is_completed", False)
                self.final_report = value.get("final_report", "")
                self.report_title = value.get("report_title", "Seed report")
                self.paragraphs = [types.SimpleNamespace(title=row["title"], research=types.SimpleNamespace(**row["research"])) for row in value["paragraphs"]]
            @classmethod
            def from_dict(cls, value):
                return cls(value)
            def mark_completed(self):
                self.is_completed = True
            def to_dict(self):
                return {**self.value, "is_completed": self.is_completed, "final_report": self.final_report,
                        "paragraphs": [{"title": paragraph.title, "research": vars(paragraph.research).copy()} for paragraph in self.paragraphs]}
        def format_manually(data, title):
            observed.append((copy.deepcopy(data), title))
            return "# " + title + "\n\n" + data[0]["paragraph_latest_state"]
        class FakeAgent:
            def __init__(self, config):
                self.state = None
                self.first_summary_node = types.SimpleNamespace()
                self.reflection_summary_node = types.SimpleNamespace()
                self.report_formatting_node = types.SimpleNamespace(format_report_manually=format_manually)
                self.execute_search_tool = unexpected_model_call
                self._generate_report_structure = unexpected_model_call
                self._initial_search_and_summary = unexpected_model_call
                self._reflection_loop = unexpected_model_call
                self._generate_final_report = unexpected_model_call
            def research(self, query, save_report=True):
                self._generate_report_structure(query)
                self._process_paragraphs()
                return self._generate_final_report()
        imports = {
            "MediaEngine": types.SimpleNamespace(Settings=lambda **kwargs: types.SimpleNamespace(SEARCH_TOOL_TYPE="AnspireAPI", **kwargs),
                                                  DeepSearchAgent=FakeAgent, AnspireSearchAgent=FakeAgent),
            "MediaEngine.nodes.report_structure_node": types.SimpleNamespace(
                SYSTEM_PROMPT_REPORT_STRUCTURE="", ReportStructureNode=type("FakeReportStructureNode", (), {})),
            "MediaEngine.nodes.summary_node": types.SimpleNamespace(SYSTEM_PROMPT_FIRST_SUMMARY="", SYSTEM_PROMPT_REFLECTION_SUMMARY="", FORUM_READER_AVAILABLE=True),
            "MediaEngine.state.state": types.SimpleNamespace(State=FakeState),
        }
        target = self.run / "case" / "case-media"
        with patch("socket.socket", side_effect=unexpected_model_call), patch.object(adapter.importlib, "import_module", side_effect=lambda name: imports[name]):
            state, report = adapter.research_live(args, request, payload, target)
        self.assertTrue(state["is_completed"])
        self.assertIn("已完成的实际摘要", report)
        self.assertEqual(len(observed), 1)
        self.assertEqual(observed[0][0][0]["paragraph_latest_state"], args.resume_seed["checkpoint"]["state"]["paragraphs"][0]["research"]["latest_summary"])
        updated = adapter.read_json(target / "checkpoint.json")
        self.assertEqual(updated["workflow_id"], "case")
        self.assertEqual(updated["task_id"], "case-media")
        self.assertTrue(updated["state"]["is_completed"])

    def test_fixed_sample_filters_exact_comment_ids_and_bodies_before_local_models(self):
        comments = [{"comment_id": str(index), "content": "原始评论" + str(index)} for index in range(20)]
        records = [types.SimpleNamespace(source_id=index, title_or_content="原始评论" + str(index)) for index in range(20)]
        invalid = [types.SimpleNamespace(source_id="0", title_or_content="被修改的评论正文"),
                   types.SimpleNamespace(source_id="unknown-id", title_or_content=comments[1]["content"])]
        response = types.SimpleNamespace(results=invalid + records, results_count=22, parameters={"existing": "retained"})
        db_calls, clustering_calls, sentiment_calls = [], [], []
        sentiment = {"total_analyzed": 12, "distribution": {"positive": 4, "neutral": 5, "negative": 3}}
        def query(topic, limit):
            db_calls.append((topic, limit))
            return response
        def cluster(selected, **kwargs):
            clustering_calls.append((list(selected), kwargs))
            return selected[:kwargs["max_results"]]
        def analyze(selected):
            sentiment_calls.append(list(selected))
            return sentiment
        agent = types.SimpleNamespace(search_agency=types.SimpleNamespace(get_comments_for_topic=query),
                                      _cluster_and_sample_results=cluster, _perform_sentiment_analysis=analyze)
        with patch("socket.socket", side_effect=AssertionError("fixed sample validation opened network")):
            result = adapter.fixed_sample_response(agent, comments)
        self.assertEqual(db_calls, [("", len(comments))])
        self.assertEqual(clustering_calls[0][0], records)
        self.assertEqual(clustering_calls[0][1], {"max_results": 12, "results_per_cluster": 2})
        self.assertEqual(sentiment_calls, [records[:12]])
        self.assertEqual(result.results, records[:12])
        self.assertEqual(result.results_count, 12)
        self.assertEqual(result.parameters["existing"], "retained")
        self.assertEqual(result.parameters["raw_sample_count"], len(comments))
        self.assertTrue(result.parameters["frozen_sample_scope"])
        self.assertIs(result.parameters["sentiment_analysis"], sentiment)

    def test_fixed_sample_no_exact_match_fails_before_clustering_or_sentiment(self):
        comments = [{"comment_id": "kept-id", "content": "原始正文"}]
        response = types.SimpleNamespace(results=[types.SimpleNamespace(source_id="kept-id", title_or_content="不同正文"),
                                                 types.SimpleNamespace(source_id="other-id", title_or_content="原始正文")], parameters={})
        def unexpected_local_model(*args, **kwargs):
            raise AssertionError("unmatched DB data entered a local model")
        agent = types.SimpleNamespace(search_agency=types.SimpleNamespace(get_comments_for_topic=lambda *args, **kwargs: response),
                                      _cluster_and_sample_results=unexpected_local_model, _perform_sentiment_analysis=unexpected_local_model)
        with self.assertRaisesRegex(adapter.AdapterError, "no comments matching"):
            adapter.fixed_sample_response(agent, comments)

    def test_fixed_sample_sentiment_requires_all_selected_rows_analyzed(self):
        comments = [{"comment_id": "a", "content": "甲"}, {"comment_id": "b", "content": "乙"}]
        records = [types.SimpleNamespace(source_id="a", title_or_content="甲"), types.SimpleNamespace(source_id="b", title_or_content="乙")]
        for sentiment in (None, {}, {"total_analyzed": 0}, {"total_analyzed": 1}, {"total_analyzed": "2"}):
            with self.subTest(sentiment=sentiment):
                response = types.SimpleNamespace(results=list(records), parameters={})
                agent = types.SimpleNamespace(search_agency=types.SimpleNamespace(get_comments_for_topic=lambda *args, **kwargs: response),
                                              _cluster_and_sample_results=lambda selected, **kwargs: selected,
                                              _perform_sentiment_analysis=lambda selected: sentiment)
                with self.assertRaisesRegex(adapter.AdapterError, "sentiment analysis failed"):
                    adapter.fixed_sample_response(agent, comments)

    def test_fixed_topic_search_flag_requires_explicit_boolean(self):
        original = self.request("insight")
        self.assertFalse(adapter.validate_request(original)["fixed_topic_search"])
        for value in ("true", "false", 1, 0, None, []):
            with self.subTest(value=value):
                request = copy.deepcopy(original)
                request["goal"] = json.dumps({"topic": self.topic, "fixed_topic_search": value})
                with self.assertRaisesRegex(adapter.AdapterError, "fixed_topic_search"):
                    adapter.validate_request(request)
        request = copy.deepcopy(original)
        request["goal"] = json.dumps({"topic": self.topic, "fixed_topic_search": True})
        self.assertTrue(adapter.validate_request(request)["fixed_topic_search"])

    def test_decode_themes_accepts_plain_fenced_and_thinking_fenced_json(self):
        value = {"themes": [{"title": "样本观点", "summary": "部分评论表达谨慎态度", "comment_ids": ["C001"]}]}
        text = json.dumps(value, ensure_ascii=False)
        for response in (text, "```json\n" + text + "\n```", "<think>内部规划文本</think>\n```json\n" + text + "\n```"):
            with self.subTest(response=response):
                self.assertEqual(adapter.decode_theme_response(response), value)

    def test_decode_themes_rejects_non_json_arrays_and_trailing_text(self):
        for response in ("非JSON正文", "[]", '[{"themes": []}]', '{"themes": []}\nextra commentary',
                         '```json\n{"themes": []}\n```\nextra commentary'):
            with self.subTest(response=response):
                with self.assertRaises(adapter.AdapterError):
                    adapter.decode_theme_response(response)

    def test_research_structure_decodes_only_complete_bounded_schema(self):
        structure = [{"title": "来源核查", "content": "比较本轮检索记录并保留不确定性"}]
        plain = json.dumps({"paragraphs": structure}, ensure_ascii=False)
        for text in (plain, "```json\n" + plain + "\n```", "<think>plan</think>\n```json\n" + plain + "\n```"):
            with self.subTest(text=text):
                self.assertEqual(adapter.decode_research_structure(text, 1), structure)
        invalid = ("[]", "{}", "plain-text-outline", '{"paragraphs":[{"title":"valid","content":"unfinished"}',
                   json.dumps({"paragraphs": structure + structure}), '{"paragraphs":[{"title":"missing-content"}]}',
                   '{"paragraphs":[{"title":"valid","content":3}]}', '{"paragraphs":[{"title":" ","content":"valid"}]}',
                   '{"paragraphs":[{"title":"valid","content":"valid","extra":"ignored"}]}', plain + " additional text")
        for text in invalid:
            with self.subTest(invalid=text):
                with self.assertRaises(adapter.AdapterError):
                    adapter.decode_research_structure(text, 1)

    def test_research_summary_rejects_repairable_partial_or_fallback_text(self):
        value = {"paragraph_latest_state": "部分来源提供相互补充的线索，但传闻仍未独立核实。"}
        plain = json.dumps(value, ensure_ascii=False)
        for text in (plain, "```json\n" + plain + "\n```", "<think>analysis</think>\n" + plain):
            with self.subTest(text=text):
                self.assertEqual(adapter.decode_research_summary(text), value["paragraph_latest_state"])
        for text in ("段落总结生成失败", "plain report", "[]", "{}", '{"paragraph_latest_state":"unfinished"',
                     '{"paragraph_latest_state":" "}', '{"paragraph_latest_state":3}',
                     '{"paragraph_latest_state":"valid","unrecognized":"field"}', plain + " extra text"):
            with self.subTest(invalid=text):
                with self.assertRaises(adapter.AdapterError):
                    adapter.decode_research_summary(text)

    def tracked_stream(self, chunks):
        class Stream:
            def __init__(self, items):
                self.items, self.closed = items, False
            def __iter__(self):
                return iter(self.items)
            def close(self):
                self.closed = True
        return Stream(chunks)

    def test_model_stream_requires_stop_then_allows_usage_tail_and_closes(self):
        partial = types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason=None)])
        stopped = types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="stop")])
        usage_only = types.SimpleNamespace(choices=[], usage=types.SimpleNamespace(total_tokens=9))
        source = self.tracked_stream([partial, stopped, usage_only])
        budget = adapter.ModelBudget(self.root / "stream-budget.json", "stream-test", 1, 120)
        stream = budget.wrap(lambda *args, **kwargs: source)(types.SimpleNamespace(), "query", stream=True)
        self.assertEqual(list(stream), [partial, stopped, usage_only])
        self.assertTrue(source.closed)

    def test_model_stream_eof_without_stop_is_not_a_complete_response(self):
        for name, chunks in (("empty", []), ("partial", [types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason=None)])])):
            with self.subTest(stream=name):
                source = self.tracked_stream(chunks)
                budget = adapter.ModelBudget(self.root / (name + "-budget.json"), name, 1, 120)
                stream = budget.wrap(lambda *args, **kwargs: source)(types.SimpleNamespace(), "query", stream=True)
                with self.assertRaises(adapter.ModelCallStopped):
                    list(stream)
                self.assertTrue(source.closed)

    def test_model_stream_length_failure_and_early_consumer_close_release_source(self):
        partial = types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason=None)])
        truncated = types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="length")])
        source = self.tracked_stream([partial, truncated])
        budget = adapter.ModelBudget(self.root / "length-budget.json", "length", 1, 120)
        stream = budget.wrap(lambda *args, **kwargs: source)(types.SimpleNamespace(), "query", stream=True)
        with self.assertRaises(adapter.ModelCallStopped):
            list(stream)
        self.assertTrue(source.closed)
        source = self.tracked_stream([partial, types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="stop")])])
        budget = adapter.ModelBudget(self.root / "closed-budget.json", "closed", 1, 120)
        stream = budget.wrap(lambda *args, **kwargs: source)(types.SimpleNamespace(), "query", stream=True)
        next(stream)
        stream.close()
        self.assertTrue(source.closed)

    def test_non_stream_model_response_also_requires_stop(self):
        for reason in (None, "length", "content_filter", "tool_calls"):
            with self.subTest(reason=reason):
                response = types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason=reason)])
                budget = adapter.ModelBudget(self.root / (str(reason) + "-response-budget.json"), str(reason), 1, 120)
                with self.assertRaises(adapter.ModelCallStopped):
                    budget.wrap(lambda *args, **kwargs: response)(types.SimpleNamespace(), "query")

    @unittest.skipUnless(adapter.deadline_supported(), "requires native POSIX main-thread timers")
    def test_native_deadline_interrupts_request_and_restores_timer_and_handler(self):
        previous_handler = signal.getsignal(signal.SIGALRM)
        previous_timer = signal.getitimer(signal.ITIMER_REAL)
        if previous_timer[0]:
            self.skipTest("another active process timer would make this check ambiguous")
        started = time.monotonic()
        with self.assertRaisesRegex(TimeoutError, "deadline exceeded"):
            with adapter.model_deadline(0.03):
                time.sleep(0.2)
        self.assertLess(time.monotonic() - started, 0.18)
        self.assertEqual(signal.getsignal(signal.SIGALRM), previous_handler)
        self.assertEqual(signal.getitimer(signal.ITIMER_REAL), previous_timer)

    def forum_resume_fixture(self, dependencies):
        folder = self.root / "resume-forum"
        speech = folder / "forum.md"
        adapter.atomic_text(speech, "已完成的主持讨论，报告必须保留来源与不确定性。")
        checkpoint = folder / "forum/checkpoint.json"
        seed = {"workflow_id": "previous-case", "task_id": "previous-case-forum", "role": "forum", "mode": "live",
                "state": {"query": self.topic, "status": "completed"},
                "forum_report": {"path": "forum.md", "sha256": adapter.digest_file(speech)},
                "research_report_sha256": {role: dependencies[role]["files"]["report"]["sha256"] for role in adapter.RESEARCH_ROLES}}
        adapter.atomic_json(checkpoint, seed)
        configuration_hash = "a" * 64
        manifest = {"topic": self.topic, "mode": "live", "configuration_sha256": configuration_hash,
                    "source_workflow_id": "previous-case", "roles": {"forum": {"path": "forum/checkpoint.json", "sha256": adapter.digest_file(checkpoint)}}}
        adapter.atomic_json(folder / "manifest.json", manifest)
        args = self.args("forum", "live")
        args.resume_dir = str(folder)
        return args, manifest, checkpoint, speech, configuration_hash

    def test_forum_resume_requires_completed_state_and_verifies_speech_file(self):
        dependencies = {row["role"]: row for row in self.research_results()}
        args, manifest, checkpoint, speech, configuration_hash = self.forum_resume_fixture(dependencies)
        payload = adapter.validate_request(self.request("forum"))
        result = adapter.load_resume_seed(args, payload, configuration_hash)
        self.assertEqual(result["forum_report"], adapter.file_record(speech))
        self.assertEqual(result["checkpoint"]["research_report_sha256"], {role: dependencies[role]["files"]["report"]["sha256"] for role in adapter.RESEARCH_ROLES})
        original = adapter.read_json(checkpoint)
        for modification in ("incomplete", "wrong-state-topic", "wrong-speech-hash", "missing-research-hashes", "speech-path-outside"):
            with self.subTest(modification=modification):
                seed = copy.deepcopy(original)
                if modification == "incomplete":
                    seed["state"]["status"] = "running"
                elif modification == "wrong-state-topic":
                    seed["state"]["query"] = "无关话题"
                elif modification == "wrong-speech-hash":
                    seed["forum_report"]["sha256"] = "0" * 64
                elif modification == "missing-research-hashes":
                    seed.pop("research_report_sha256")
                else:
                    outside = self.root / "outside-forum.md"
                    shutil.copyfile(speech, outside)
                    seed["forum_report"] = {"path": "../outside-forum.md", "sha256": adapter.digest_file(outside)}
                adapter.atomic_json(checkpoint, seed)
                manifest["roles"]["forum"]["sha256"] = adapter.digest_file(checkpoint)
                adapter.atomic_json(Path(args.resume_dir) / "manifest.json", manifest)
                with self.assertRaises(adapter.AdapterError):
                    adapter.load_resume_seed(args, payload, configuration_hash)

    def test_forum_resume_reuses_exact_research_inputs_without_model_call(self):
        dependencies = {row["role"]: row for row in self.research_results()}
        args, manifest, checkpoint, speech, configuration_hash = self.forum_resume_fixture(dependencies)
        request = self.request("forum")
        payload = adapter.validate_request(request)
        args.resume_seed = adapter.load_resume_seed(args, payload, configuration_hash)
        target = self.run / "case/case-forum-resumed"
        with patch.object(adapter, "complete_text", side_effect=AssertionError("reused Forum called a model")):
            result = adapter.downstream_role(args, request, payload, dependencies, target, usage=None)
        self.assertEqual(Path(result["files"]["report"]["path"]).read_text(encoding="utf-8").strip(), speech.read_text(encoding="utf-8").strip())
        changed = copy.deepcopy(dependencies)
        path = Path(changed["query"]["files"]["report"]["path"])
        adapter.atomic_text(path, "新的研究正文，需要重新讨论")
        changed["query"]["files"]["report"] = adapter.file_record(path)
        with patch.object(adapter, "complete_text", side_effect=AssertionError("changed research called a model")):
            with self.assertRaisesRegex(adapter.AdapterError, "research inputs changed"):
                adapter.downstream_role(args, request, payload, changed, target, usage=None)

    def test_environment_and_logs_do_not_echo_secret_values(self):
        path = self.root / ".env"
        path.write_text("ADAPTER_FIXTURE_API_KEY='test-secret-value'\n", encoding="utf-8")
        with patch.dict("os.environ", {}, clear=False):
            secrets = adapter.load_environment(path)
        output = io.StringIO()
        stream = adapter.RedactedStream(output, secrets)
        stream.write("api=test-secret-value Bearer arbitrary-token")
        self.assertNotIn("test-secret-value", output.getvalue())
        self.assertNotIn("arbitrary-token", output.getvalue())


if __name__ == "__main__":
    unittest.main()
