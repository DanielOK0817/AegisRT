"""Fresh file sample checks with no database, Runtime or model API calls."""
import argparse
import ast
import copy
from dataclasses import dataclass, field
from datetime import datetime
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import tempfile
import types
from typing import Any, Dict, List, Optional
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


samples = load("bettafish_samples_under_test", "sample_inputs.py")
adapter = load("bettafish_sample_adapter_under_test", "adapter.py")


class SampleTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.topic = "openEuler社区技术交流"
        self.comments = [{"comment_id": "comment-a", "content": "期待更多开源教程。", "source_url": "https://example.com/video/a",
                          "platform": "bilibili", "nickname": "甲", "like_count": 3},
                         {"comment_id": 2, "content": " 原文保留换行\n期待安装说明。 ", "source_url": "https://example.com/video/b",
                          "publish_time": "2026-10-02T10:00:00+08:00"}]
        self.provenance = {"topic": self.topic, "collection_method": "test fixture only",
                           "source_urls": [row["source_url"] for row in self.comments]}

    def freeze(self, name="fresh"):
        return samples.freeze_sample_values(self.topic, self.comments, self.provenance, self.root / name)

    def test_validate_values_is_detached_and_does_not_write(self):
        validated = samples.validate_sample(self.topic, self.comments, self.provenance)
        self.assertEqual(validated["comments"], self.comments)
        validated["comments"][0]["content"] = "changed"
        self.assertNotEqual(validated["comments"], self.comments)
        self.assertEqual(list(self.root.iterdir()), [])

    def test_fresh_freeze_has_only_sample_files_unique_identity_and_matching_hashes(self):
        first, second = self.freeze(), self.freeze("another")
        first_input = samples.load_sample_inputs(first["input_dir"], self.topic)
        second_input = samples.load_sample_inputs(second["input_dir"], self.topic)
        self.assertNotEqual(first_input["manifest"]["input_id"], second_input["manifest"]["input_id"])
        self.assertEqual(set(path.name for path in Path(first["input_dir"]).iterdir()), {"manifest.json", "comments.json", "provenance.json"})
        self.assertEqual(first_input["manifest"]["inputs"], {})
        self.assertEqual(first_input["comments"], self.comments)
        self.assertEqual(first_input["sample_count"], 2)
        self.assertEqual(first_input["total_characters"], sum(len(row["content"]) for row in self.comments))
        for record in first_input["files"].values():
            self.assertEqual(hashlib.sha256(Path(record["path"]).read_bytes()).hexdigest(), record["sha256"])

    def test_fresh_manifest_can_be_relocated_without_source_directory(self):
        frozen = self.freeze()
        relocated = self.root / "relocated"
        shutil.copytree(frozen["input_dir"], relocated)
        self.assertEqual(samples.load_sample_inputs(relocated, self.topic)["comments"], self.comments)

    def test_existing_destination_or_invalid_upload_is_not_overwritten(self):
        target = self.root / "existing"
        target.mkdir()
        (target / "keep.txt").write_text("original", encoding="utf-8")
        with self.assertRaises(samples.SampleInputError):
            samples.freeze_sample_values(self.topic, self.comments, self.provenance, target)
        with self.assertRaises(samples.SampleInputError):
            samples.freeze_sample_values("wrong topic", self.comments, self.provenance, self.root / "invalid")
        self.assertFalse((self.root / "invalid").exists())
        self.assertEqual((target / "keep.txt").read_text(encoding="utf-8"), "original")

    def test_raw_files_preserve_original_source_digests_without_old_research(self):
        comment_file, provenance_file = self.root / "raw-comments.json", self.root / "raw-provenance.json"
        comment_file.write_text(json.dumps(self.comments, ensure_ascii=False), encoding="utf-8-sig")
        provenance_file.write_text(json.dumps(self.provenance, ensure_ascii=False), encoding="utf-8")
        frozen = samples.freeze_sample_inputs(self.topic, comment_file, provenance_file, self.root / "from-paths")
        manifest = samples.load_sample_inputs(frozen["input_dir"], self.topic)["manifest"]
        self.assertEqual(manifest["source_file_sha256"]["comments"], hashlib.sha256(comment_file.read_bytes()).hexdigest())
        self.assertEqual(manifest["source_file_sha256"]["provenance"], hashlib.sha256(provenance_file.read_bytes()).hexdigest())

    def test_topic_identity_and_provenance_sources_are_strict(self):
        for topic in (None, "", " ", " topic ", "x" * 301, "topic\n"):
            with self.subTest(topic=topic), self.assertRaises(samples.SampleInputError):
                samples.validate_sample(topic, self.comments, {"topic": topic})
        for provenance in (None, {}, {"topic": "other"}, {"topic": self.topic, "source_urls": []},
                           {"topic": self.topic, "source_urls": [self.comments[0]["source_url"]]}):
            with self.subTest(provenance=provenance), self.assertRaises(samples.SampleInputError):
                samples.validate_sample(self.topic, self.comments, provenance)

    def test_comment_identity_body_source_and_metadata_are_strict(self):
        changes = [{"comment_id": None}, {"comment_id": True}, {"comment_id": 0}, {"comment_id": " "},
                   {"comment_id": "x" * 129}, {"content": " "}, {"content": "x\x00"}, {"content": 42},
                   {"source_url": "https://"}, {"source_url": "file:///private"}, {"source_url": "https://user:pass@example.com/"},
                   {"source_url": "https://example.com/a b"}, {"source_url": "https://example.com:bad/"},
                   {"like_count": True}, {"like_count": -1}, {"nickname": 1}, {"publish_time": "not a date"}, {"topic": "other"}]
        for change in changes:
            comments = copy.deepcopy(self.comments)
            comments[0].update(change)
            with self.subTest(change=change), self.assertRaises(samples.SampleInputError):
                samples.validate_sample(self.topic, comments, self.provenance)
        comments = copy.deepcopy(self.comments)
        comments[0]["comment_id"] = "2"
        with self.assertRaisesRegex(samples.SampleInputError, "duplicate"):
            samples.validate_sample(self.topic, comments, self.provenance)

    def test_count_character_and_metadata_limits_fail_before_writing(self):
        cases = [[], self.comments[:1], [{**self.comments[0], "comment_id": str(i)} for i in range(201)],
                 [{**row, "content": "x" * 2001} for row in self.comments],
                 [{**self.comments[0], "comment_id": str(i), "content": "x" * 2000} for i in range(21)]]
        for comments in cases:
            with self.subTest(count=len(comments)), self.assertRaises(samples.SampleInputError):
                samples.validate_sample(self.topic, comments, self.provenance)
        for limits in ({"max_comments": 201}, {"max_comments": True}, {"max_comment_characters": 2001},
                       {"max_total_characters": 40001}, {"max_total_characters": 1}):
            with self.subTest(limits=limits), self.assertRaises(samples.SampleInputError):
                samples.validate_sample(self.topic, self.comments, self.provenance, **limits)
        with self.assertRaises(samples.SampleInputError):
            samples.validate_sample(self.topic, self.comments, {"topic": self.topic, "extra": float("nan")})

    def test_complete_utf8_json_with_unique_keys_is_required(self):
        comments_path = self.root / "comments.json"
        provenance_path = self.root / "provenance.json"
        comments_path.write_text(json.dumps(self.comments), encoding="utf-8")
        for raw in (b'{"topic":"first","topic":"second"}', b'{"topic":', b'\xff', b'{"topic":NaN}'):
            provenance_path.write_bytes(raw)
            with self.subTest(raw=raw), self.assertRaises(samples.SampleInputError):
                samples.freeze_sample_inputs(self.topic, comments_path, provenance_path, self.root / "bad")
        self.assertFalse((self.root / "bad").exists())

    def test_frozen_manifest_checksum_identity_paths_and_metadata_are_verified(self):
        frozen = self.freeze()
        target = Path(frozen["input_dir"])
        manifest_path = target / "manifest.json"
        original = json.loads(manifest_path.read_text(encoding="utf-8"))
        changes = [{"topic": "wrong"}, {"schema": "wrong"}, {"sample_source": "database"}, {"input_id": "bad"},
                   {"sample_count": True}, {"total_characters": 0}, {"inputs": {"query": "old.md"}},
                   {"files": {**original["files"], "comments": {"path": "../outside.json", "sha256": original["comments_sha256"]}}},
                   {"comments_sha256": "0" * 64}, {"source_urls": []}]
        for change in changes:
            manifest_path.write_text(json.dumps({**original, **change}), encoding="utf-8")
            with self.subTest(change=change), self.assertRaises(samples.SampleInputError):
                samples.load_sample_inputs(target, self.topic)
        manifest_path.write_text(json.dumps(original), encoding="utf-8")
        with (target / "comments.json").open("a", encoding="utf-8") as stream:
            stream.write(" ")
        with self.assertRaisesRegex(samples.SampleInputError, "checksum"):
            samples.load_sample_inputs(target, self.topic)

    @staticmethod
    def upstream_types():
        source = ROOT.parents[2] / "reproductions/bettafish-20261001/source/InsightEngine/tools/search.py"
        tree = ast.parse(source.read_text(encoding="utf-8"))
        classes = [node for node in tree.body if isinstance(node, ast.ClassDef) and node.name in {"QueryResult", "DBResponse"}]
        namespace = {"__name__": __name__, "dataclass": dataclass, "field": field, "datetime": datetime,
                     "Optional": Optional, "Dict": Dict, "Any": Any, "List": List}
        exec(compile(ast.Module(body=classes, type_ignores=[]), str(source), "exec"), namespace)
        return types.SimpleNamespace(QueryResult=namespace["QueryResult"], DBResponse=namespace["DBResponse"])

    def test_insight_record_values_fit_actual_upstream_dataclass(self):
        upstream = self.upstream_types()
        records = [upstream.QueryResult(**values) for values in samples.insight_record_values(self.comments, self.topic)]
        self.assertEqual([row.source_id for row in records], ["comment-a", "2"])
        self.assertEqual([row.title_or_content for row in records], [row["content"] for row in self.comments])
        self.assertEqual(records[0].url, self.comments[0]["source_url"])
        self.assertEqual(records[0].source_table, "file_sample_comments")
        self.assertIsInstance(records[1].publish_time, datetime)

    def test_file_mode_calls_sentiment_on_original_sample_without_database(self):
        analyzed = []
        sentiment = {"total_analyzed": 2, "sentiment_distribution": {"positive": 1, "neutral": 1}}
        def no_database(*args, **kwargs):
            raise AssertionError("file sample touched database")
        def analyze(records):
            analyzed.extend(records)
            return sentiment
        agent = types.SimpleNamespace(search_agency=types.SimpleNamespace(get_comments_for_topic=no_database),
                                      _perform_sentiment_analysis=analyze)
        with patch("socket.socket", side_effect=no_database), patch.object(adapter.importlib, "import_module", return_value=self.upstream_types()):
            response = adapter.fixed_sample_response(agent, self.comments, source_mode="file", topic=self.topic)
        self.assertEqual(response.results_count, 2)
        self.assertEqual([row.source_id for row in analyzed], ["comment-a", "2"])
        self.assertEqual(response.parameters["sample_source"], "file")
        self.assertIs(response.parameters["sentiment_analysis"], sentiment)
        self.assertFalse(response.parameters["clustering"]["performed"])

    def test_file_mode_sentiment_failure_or_partial_inference_is_rejected(self):
        for result in (None, {}, {"total_analyzed": 0}, {"total_analyzed": 1}, {"total_analyzed": 2.0}):
            agent = types.SimpleNamespace(_perform_sentiment_analysis=lambda _records: result)
            with patch.object(adapter.importlib, "import_module", return_value=self.upstream_types()), self.subTest(result=result):
                with self.assertRaisesRegex(adapter.AdapterError, "sentiment"):
                    adapter.fixed_sample_response(agent, self.comments, source_mode="file", topic=self.topic)

    def test_unknown_sample_source_does_not_default_to_database(self):
        agent = types.SimpleNamespace()
        for source in (None, "FILE", "unknown", False):
            with self.subTest(source=source), self.assertRaises(adapter.AdapterError):
                adapter.fixed_sample_response(agent, self.comments, source_mode=source, topic=self.topic)

    def test_large_file_sample_uses_insight_encoder_and_kmeans_before_sentiment(self):
        comments = [{**self.comments[0], "comment_id": str(index), "content": "样本正文" + str(index), "like_count": index} for index in range(20)]
        encoded, clustering, analyzed = [], [], []
        def encode(texts, **kwargs):
            encoded.append((texts, kwargs))
            return [[float(index)] for index in range(len(texts))]
        class KMeansDouble:
            def __init__(self, **kwargs):
                clustering.append(kwargs)
            def fit_predict(self, embeddings):
                return [index % 6 for index in range(len(embeddings))]
        def analyze(records):
            analyzed.extend(records)
            return {"total_analyzed": len(records)}
        core = self.upstream_types()
        core.KMeans = KMeansDouble
        agent = types.SimpleNamespace(_get_clustering_model=lambda: types.SimpleNamespace(encode=encode), _perform_sentiment_analysis=analyze)
        with patch.object(adapter.importlib, "import_module", return_value=core):
            response = adapter.fixed_sample_response(agent, comments, source_mode="file", topic=self.topic)
        self.assertEqual(len(encoded[0][0]), 20)
        self.assertEqual(encoded[0][1], {"show_progress_bar": False})
        self.assertEqual(clustering, [{"n_clusters": 6, "random_state": 42, "n_init": 10}])
        self.assertEqual(len(analyzed), 12)
        self.assertEqual(len({row.source_id for row in analyzed}), 12)
        self.assertTrue(response.parameters["clustering"]["performed"])
        self.assertEqual(response.parameters["clustering"]["encoded_count"], 20)

    def test_large_file_clustering_failures_never_use_first_twelve_fallback(self):
        comments = [{**self.comments[0], "comment_id": str(index)} for index in range(20)]
        def fail(*args, **kwargs):
            raise RuntimeError("local inference failed")
        def no_sentiment(_rows):
            raise AssertionError("failed clustering proceeded to sentiment")
        for mode in ("encode-error", "incomplete-encoding", "kmeans-error", "invalid-labels"):
            core = self.upstream_types()
            class KMeansDouble:
                def __init__(self, **kwargs):
                    pass
                def fit_predict(self, embeddings):
                    if mode == "kmeans-error":
                        return fail()
                    return [0] if mode == "invalid-labels" else [index % 6 for index in range(20)]
            core.KMeans = KMeansDouble
            encode = fail if mode == "encode-error" else lambda texts, **kwargs: [[0]] * (1 if mode == "incomplete-encoding" else len(texts))
            agent = types.SimpleNamespace(_get_clustering_model=lambda: types.SimpleNamespace(encode=encode), _perform_sentiment_analysis=no_sentiment)
            with self.subTest(mode=mode), patch.object(adapter.importlib, "import_module", return_value=core):
                with self.assertRaisesRegex(adapter.AdapterError, "clustering"):
                    adapter.fixed_sample_response(agent, comments, source_mode="file", topic=self.topic)

    def test_file_clustering_cannot_change_comment_identity_body_or_source(self):
        upstream = self.upstream_types()
        for change in ({"source_id": "different"}, {"title_or_content": "different"}, {"url": "https://different.example/"}):
            rows = [upstream.QueryResult(**values) for values in samples.insight_record_values(self.comments, self.topic)]
            for key, value in change.items():
                setattr(rows[0], key, value)
            with self.subTest(change=change), patch.object(adapter.importlib, "import_module", return_value=upstream), patch.object(adapter, "cluster_file_sample", return_value=(rows, {})):
                with self.assertRaisesRegex(adapter.AdapterError, "identities"):
                    adapter.fixed_sample_response(types.SimpleNamespace(), self.comments, source_mode="file", topic=self.topic)

    def test_invalid_file_values_are_rejected_before_upstream_tools_import(self):
        with patch.object(adapter.importlib, "import_module", side_effect=AssertionError("invalid sample imported tools")):
            with self.assertRaises(adapter.AdapterError):
                adapter.fixed_sample_response(types.SimpleNamespace(), [], source_mode="file", topic=self.topic)

    def test_file_research_binds_database_precondition_to_verified_samples(self):
        frozen = self.freeze()
        observed = []
        class ProbeReached(RuntimeError):
            pass
        def no_database(*args, **kwargs):
            raise AssertionError("fresh file research queried a database")
        class FakeAgent:
            def __init__(self, config):
                self.search_agency = types.SimpleNamespace(require_source_data=no_database)
                self.first_search_node = types.SimpleNamespace(run=no_database)
                self.first_summary_node = types.SimpleNamespace(run=no_database)
                self.reflection_summary_node = types.SimpleNamespace()
                self.execute_search_tool = no_database
                self._generate_report_structure = no_database
            def research(self, topic, **kwargs):
                observed.append(self.search_agency.require_source_data())
                observed.append(self.first_search_node.run({})["search_query"])
                raise ProbeReached("past upstream database precondition")
        imports = {"InsightEngine": types.SimpleNamespace(Settings=lambda **kwargs: types.SimpleNamespace(**kwargs), DeepSearchAgent=FakeAgent),
                   "InsightEngine.nodes.report_structure_node": types.SimpleNamespace(ReportStructureNode=type("StructureDouble", (), {})),
                   "InsightEngine.nodes.summary_node": types.SimpleNamespace()}
        args = argparse.Namespace(role="insight", input_dir=frozen["input_dir"])
        request = {"workflow_id": "fresh", "task_id": "fresh-insight",
                   "goal": json.dumps({"topic": self.topic, "fixed_topic_search": True})}
        with patch.object(adapter.importlib, "import_module", side_effect=lambda name: imports[name]):
            with self.assertRaises(ProbeReached):
                adapter.research_live(args, request, adapter.validate_request(request), self.root / "task")
        self.assertEqual(observed, [2, self.topic])

    def test_file_sample_report_scope_preserves_historical_dates_and_escapes_notes(self):
        self.comments[0]["create_time"] = 1723820936
        self.comments[1]["publish_time"] = "2024-08-17T12:00:00+08:00"
        self.provenance.update(finished_at="2026-10-02T15:00:00+08:00", sampling="每视频自然返回少量评论", note="<script>untrusted</script>")
        frozen = samples.load_sample_inputs(self.freeze()["input_dir"], self.topic)
        state = self.root / "insight-state.json"
        adapter.atomic_json(state, {"source_snapshot": {"sample_source": "file"}})
        files = {**frozen["files"], "state": adapter.file_record(state)}
        scope = adapter.file_sample_report_scope(files, self.topic)
        self.assertEqual(scope["comment_date_min"], "2024-08-16")
        self.assertEqual(scope["comment_date_max"], "2024-08-17")
        self.assertTrue(scope["historical_relative_to_collection"])
        self.assertEqual(scope["provenance_sha256"], frozen["files"]["provenance"]["sha256"])
        page = adapter.file_sample_scope_html(scope)
        self.assertIn("不能称为最新舆情", page)
        self.assertIn("&lt;script&gt;", page)
        self.assertNotIn("<script>", page)
        self.assertIn(self.provenance["sampling"], page)

    def test_original_unix_publish_date_is_not_replaced_by_freeze_date(self):
        self.comments[0]["create_time"] = 1723820936
        frozen = samples.load_sample_inputs(self.freeze()["input_dir"], self.topic)
        self.assertEqual(frozen["comments"][0]["create_time"], 1723820936)
        record = samples.insight_record_values(frozen["comments"], self.topic)[0]
        self.assertEqual(record["publish_time"].year, 2024)

    def test_file_report_receives_time_scope_and_publishes_visible_notice_and_audit(self):
        self.comments[0]["create_time"] = 1723820936
        self.comments[1]["publish_time"] = "2024-08-17T12:00:00+08:00"
        self.provenance.update(finished_at="2026-10-02T15:00:00+08:00", sampling="两个来源的少量原始评论", note="<source-note>")
        frozen = samples.load_sample_inputs(self.freeze()["input_dir"], self.topic)
        dependencies = {}
        for role in adapter.RESEARCH_ROLES:
            folder = self.root / role
            report, state = folder / "report.md", folder / "state.json"
            adapter.atomic_text(report, "测试分报告，未调用研究模型")
            adapter.atomic_json(state, {"source_snapshot": {"sample_source": "file"} if role == "insight" else {},
                "research_state": {"paragraphs": [{"research": {"search_history": [{"title": "来源", "url": "https://example.com/" + role}]}}]}})
            dependencies[role] = {"files": {"report": adapter.file_record(report), "state": adapter.file_record(state)}}
        dependencies["insight"]["files"].update({key: frozen["files"][key] for key in ("comments", "provenance")})
        forum = self.root / "forum.md"
        adapter.atomic_text(forum, "测试讨论，保持来源边界")
        dependencies["forum"] = {"files": {"report": adapter.file_record(forum)}}
        target = self.root / "report"
        args = argparse.Namespace(role="report", mode="live", source_dir=str(ROOT.parents[2] / "reproductions/bettafish-20261001/source"))
        request = {"workflow_id": "file-report", "task_id": "file-report-report",
                   "goal": json.dumps({"topic": self.topic, "max_content_length": 12000, "max_model_requests": 2})}
        payload = adapter.validate_request(request)
        captured = []
        def complete(_usage, role, system, user):
            model_input = json.loads(user)
            captured.append((role, system, model_input))
            if model_input.get("task") == "paragraph-evidence-review":
                return json.dumps({"checks": [{"path": row["path"], "supported": True,
                    "reason": "所引材料支持测试段落。",
                    "evidence_ids": (row["source_ids"] + row["comment_ids"] + row["research_ids"])[:1],
                    "sentence_checks": [{"index": sentence["index"], "supported": True,
                        "reason": "所引材料支持测试句。",
                        "evidence_ids": (row["source_ids"] + row["comment_ids"] + row["research_ids"])[:1]}
                        for sentence in row["sentences"]]}
                    for row in model_input["paragraphs"]]}, ensure_ascii=False)
            sources = [row["id"] for row in model_input["sources"]]
            research = {role: next(row["id"] for row in model_input["research"] if row["role"] == role)
                        for role in (*adapter.RESEARCH_ROLES, "forum")}
            sections = []
            for key in ("summary", "background", "research", "controversies", "comments", "discussion", "limitations"):
                paragraph = {"text": "原始评论属于历史样本，分析应保留时间范围与采样局限。",
                             "source_ids": [], "comment_ids": [], "research_ids": []}
                if key in ("background", "controversies"):
                    paragraph["source_ids"] = sources
                elif key == "comments":
                    paragraph["comment_ids"] = [row["id"] for row in model_input["comments"]]
                elif key == "research":
                    paragraph["research_ids"] = [research[role] for role in adapter.RESEARCH_ROLES]
                else:
                    paragraph["research_ids"] = [research["forum"]]
                sections.append({"key": key, "title": "历史样本与来源核验", "paragraphs": [paragraph]})
            return json.dumps({"sections": sections}, ensure_ascii=False)
        with patch.object(adapter, "complete_text", side_effect=complete):
            result = adapter.downstream_role(args, request, payload, dependencies, target, None)
        self.assertIn("不能将历史评论当作最新舆情", captured[0][1])
        self.assertEqual(captured[0][2]["sample_scope"]["comment_date_min"], "2024-08-16")
        page = Path(result["files"]["report"]["path"]).read_text(encoding="utf-8")
        self.assertIn("本轮样本口径", page)
        self.assertIn("2024-08-16", page)
        self.assertIn("&lt;source-note&gt;", page)
        self.assertIn(self.provenance["sampling"], page)
        audit = adapter.read_json(Path(result["files"]["audit"]["path"]))
        self.assertTrue(audit["sample_scope"]["historical_relative_to_collection"])
        self.assertEqual(audit["sample_scope"]["provenance_sha256"], frozen["files"]["provenance"]["sha256"])


if __name__ == "__main__":
    unittest.main()
