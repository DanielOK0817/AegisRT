"""Check the production report adapter against all evidence categories."""
import argparse
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("report_adapter_test", ROOT / "adapter.py")
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


def model_report(model_input):
    sources = model_input["sources"]
    research = model_input["research"]
    roles = {role: next(row["id"] for row in research if row["role"] == role) for role in adapter.RESEARCH_ROLES}
    forum = next(row["id"] for row in research if row["role"] == "forum")
    sections = []
    for key, title in (("summary", "摘要"), ("background", "背景资料"), ("research", "多角度研究"),
                       ("controversies", "争议与证据"), ("comments", "评论观点"),
                       ("discussion", "讨论结论"), ("limitations", "研究局限")):
        paragraph = {"text": "材料需要结合来源核查，观点归纳保留不确定性。",
                     "source_ids": [], "comment_ids": [], "research_ids": []}
        if key in ("summary", "background", "controversies", "limitations"):
            paragraph["source_ids"] = [row["id"] for row in sources]
        elif key == "research":
            paragraph["research_ids"] = list(roles.values())
        elif key == "comments":
            paragraph["comment_ids"] = [model_input["comments"][0]["id"]]
        else:
            paragraph["research_ids"] = [forum]
        sections.append({"key": key, "title": title, "paragraphs": [paragraph]})
    return {"sections": sections}


def model_review(review_input, unsupported_paths=()):
    return {"checks": [{"path": row["path"], "supported": row["path"] not in unsupported_paths,
        "reason": "给定摘录支持该段归纳。" if row["path"] not in unsupported_paths else "该段具体结论超出所引摘录，需要删除或谨慎归因。",
        "evidence_ids": (row["source_ids"] + row["comment_ids"] + row["research_ids"])[:1],
        "sentence_checks": [{"index": sentence["index"], "supported": row["path"] not in unsupported_paths,
            "reason": "所引摘录支持该原句和范围。" if row["path"] not in unsupported_paths else "该原句缺少本段证据支持。",
            "evidence_ids": (row["source_ids"] + row["comment_ids"] + row["research_ids"])[:1]}
            for sentence in row["sentences"]]}
        for row in review_input["paragraphs"]]}


def model_response(model_input):
    value = model_review(model_input) if model_input.get("task") == "paragraph-evidence-review" else model_report(model_input)
    return json.dumps(value, ensure_ascii=False)


class ReportingIntegrationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.topic = "游戏体验分析"
        self.args = argparse.Namespace(role="report", mode="live", source_dir=str(self.root), run_dir=str(self.root))
        self.request = {"workflow_id": "report-test", "task_id": "report-test-report",
                        "goal": json.dumps({"topic": self.topic, "max_model_requests": 2})}
        self.payload = adapter.validate_request(self.request)
        self.dependencies = {}
        for role in adapter.RESEARCH_ROLES:
            folder = self.root / role
            folder.mkdir()
            report = folder / "report.md"
            report.write_text(role + "研究正文需要纳入综合报告", encoding="utf-8")
            state = folder / "state.json"
            adapter.atomic_json(state, {"source_snapshot": {}, "research_state": {
                "query": self.topic, "is_completed": True,
                "paragraphs": [{"title": "来源研究", "research": {"is_completed": True,
                    "latest_summary": role + "多角度研究总结", "search_history": [{
                        "title": role + "外部原文", "url": "https://example.org/" + role,
                        "content": role + "网页原文包含2024年信息，应区分历史与当前。"}]}}]}})
            files = {"report": adapter.file_record(report), "state": adapter.file_record(state)}
            if role == "insight":
                comments = folder / "comments.json"
                adapter.atomic_json(comments, [{"comment_id": "raw-" + str(index),
                    "content": text, "source_url": "https://example.org/comment/" + str(index)}
                    for index, text in enumerate(("原评论甲<script>bad()</script>", "原评论乙"))])
                files["comments"] = adapter.file_record(comments)
            self.dependencies[role] = {"files": files}
        forum = self.root / "forum.md"
        forum.write_text("主持意见：补充独立来源并讨论分歧。", encoding="utf-8")
        self.dependencies["forum"] = {"files": {"report": adapter.file_record(forum)}}
        self.target = self.root / "output"
        self.target.mkdir()

    def test_shared_evidence_semantics_reach_generation_and_review(self):
        captured=[]
        def complete(_usage,_role,system,text):
            captured.append((system,json.loads(text)))
            return model_response(json.loads(text))
        with patch.object(adapter,"complete_text",side_effect=complete):
            adapter.downstream_role(self.args,self.request,self.payload,self.dependencies,self.target,None)
        self.assertEqual(len(captured),2)
        marker="生成和审核共用以下证据规则。"
        for system,_ in captured:
            self.assertIn(marker,system)
            self.assertIn("不能概括为广泛认可",system)
            self.assertIn("不能推出事件虚假",system)
        self.assertFalse(captured[1][1]["scope"]["original_source_truth_verified"])

    def test_live_adapter_produces_all_sections_and_original_evidence(self):
        captured = []
        def completion(_usage, _role, _system, text):
            model_input = json.loads(text)
            captured.append(model_input)
            return model_response(model_input)
        with patch.object(adapter, "complete_text", side_effect=completion):
            result = adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
        page = Path(result["files"]["report"]["path"]).read_text(encoding="utf-8")
        audit = adapter.read_json(Path(result["files"]["audit"]["path"]))
        self.assertEqual(len(audit["sections"]), 7)
        self.assertTrue(result["comprehensive_report"])
        self.assertEqual(len(captured), 2)
        self.assertEqual([r['key'] for r in captured[0]['required_sections_in_order']],
                         ['summary','background','research','controversies','comments','discussion','limitations'])
        self.assertIn('EVERY section',captured[0]['citation_requirements']['named_role_attribution'])
        self.assertEqual({row["role"] for row in captured[0]["research"]}, {"query", "media", "insight", "forum"})
        self.assertIn("query网页原文包含2024年信息", page)
        self.assertIn("主持意见", page)
        self.assertIn("&lt;script&gt;bad()&lt;/script&gt;", page)
        self.assertNotIn("<script>bad()", page)
        self.assertEqual([row["status"] for row in audit["validation_attempts"]], ["candidate_validated", "passed"])
        self.assertTrue(audit["validation"]["model_evidence_review_performed"])
        self.assertIn("evidence_review", result["files"])

    def test_invalid_model_response_is_retained_and_one_correction_is_counted(self):
        self.payload["max_model_requests"] = 5
        calls = []
        def completion(_usage, _role, _system, text):
            model_input = json.loads(text)
            calls.append(model_input)
            return '{}' if len(calls) == 1 else model_response(model_input)
        with patch.object(adapter, "complete_text", side_effect=completion):
            result = adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
        self.assertEqual(len(calls), 3)
        self.assertIn("correction", calls[1])
        self.assertEqual((self.target / "model-report-response-1.txt").read_text(), '{}')
        audit = adapter.read_json(Path(result["files"]["audit"]["path"]))
        self.assertEqual([row["status"] for row in audit["validation_attempts"]], ["rejected", "candidate_validated", "passed"])

    def test_unknown_references_fail_without_publishing_a_report(self):
        def completion(_usage, _role, _system, text):
            candidate = model_report(json.loads(text))
            candidate["sections"][0]["paragraphs"][0]["source_ids"] = ["invented-source"]
            return json.dumps(candidate, ensure_ascii=False)
        with patch.object(adapter, "complete_text", side_effect=completion) as mock:
            with self.assertRaises(adapter.AdapterError):
                adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
        self.assertEqual(mock.call_count, 1)
        self.assertFalse((self.target / "report.html").exists())
        self.assertTrue((self.target / "report-validation-attempts.json").exists())

    def test_transient_provider_failure_is_retried_within_report_budget(self):
        self.payload["max_model_requests"] = 3
        calls = []
        def completion(_usage, _role, _system, text):
            supplied = json.loads(text)
            calls.append(supplied)
            if len(calls) == 1:
                raise adapter.ModelCallStopped("provider request failed", retryable=True)
            return model_response(supplied)
        with patch.object(adapter, "complete_text", side_effect=completion):
            result = adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
        audit = adapter.read_json(Path(result["files"]["audit"]["path"]))
        self.assertEqual(len(calls), 3)
        self.assertEqual([row["status"] for row in audit["validation_attempts"]], ["provider_error", "candidate_validated", "passed"])

    def test_missing_citations_then_transient_failure_can_be_corrected_automatically(self):
        self.payload["max_model_requests"] = 5
        calls = []
        def completion(_usage, _role, _system, text):
            supplied = json.loads(text)
            calls.append(supplied)
            if supplied.get("task") == "paragraph-evidence-review":
                return model_response(supplied)
            candidate = model_report(supplied)
            if len(calls) == 1:
                for index in (0, 6):
                    candidate["sections"][index]["paragraphs"][0]["source_ids"] = []
            if len(calls) == 2:
                raise adapter.ModelCallStopped("provider request failed", retryable=True)
            return json.dumps(candidate, ensure_ascii=False)
        with patch.object(adapter, "complete_text", side_effect=completion):
            result = adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
        self.assertEqual(len(calls), 4)
        correction = calls[1]["correction"]
        self.assertIn("previous_candidate", correction)
        self.assertEqual({row["path"] for row in correction["issues"]},
                         {"$.sections[0].paragraphs[0]", "$.sections[6].paragraphs[0]"})
        self.assertEqual(correction["previous_candidate"]["sections"][0]["paragraphs"][0]["source_ids"], [])
        self.assertEqual(calls[2]["correction"], correction)
        audit = adapter.read_json(Path(result["files"]["audit"]["path"]))
        self.assertEqual([row["status"] for row in audit["validation_attempts"]], ["rejected", "provider_error", "candidate_validated", "passed"])
        self.assertTrue((self.target / "model-report-response-1.txt").is_file())
        self.assertFalse((self.target / "model-report-response-2.txt").exists())

    def test_non_retryable_provider_failure_does_not_consume_more_requests(self):
        self.payload["max_model_requests"] = 3
        with patch.object(adapter, "complete_text", side_effect=adapter.ModelCallStopped("provider request failed")) as completion:
            with self.assertRaises(adapter.ModelCallStopped):
                adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
        self.assertEqual(completion.call_count, 1)
        self.assertFalse((self.target / "report.html").exists())
        attempts = adapter.read_json(self.target / "report-validation-attempts.json")
        self.assertEqual(attempts[0]["status"], "provider_error")

    def test_invalid_responses_preserve_review_slot_without_publishing(self):
        self.payload["max_model_requests"] = 3
        with patch.object(adapter, "complete_text", return_value="{}") as completion:
            with self.assertRaises(adapter.AdapterError):
                adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
        self.assertEqual(completion.call_count, 2)
        self.assertFalse((self.target / "report.html").exists())
        attempts = adapter.read_json(self.target / "report-validation-attempts.json")
        self.assertEqual(len(attempts), 2)
        self.assertTrue(all(row["status"] == "rejected" for row in attempts))

    def test_execute_allows_third_report_request_in_the_persisted_provider_budget(self):
        configuration = self.root / "offline-configuration.txt"
        configuration.write_text("# offline fixture", encoding="utf-8")
        self.args.env_file = str(configuration)
        self.args.resume_dir = None
        self.request["goal"] = json.dumps({"topic": self.topic, "max_model_requests": 5})
        provider_calls = []
        def provider(_client, _role, **kwargs):
            supplied = json.loads(kwargs["messages"][0]["content"])
            provider_calls.append(supplied)
            content = "{}" if len(provider_calls) < 3 else model_response(supplied)
            return types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="stop", message=types.SimpleNamespace(content=content))])
        def boundary(_source, budget, *_args):
            return types.SimpleNamespace(recorded_completion=budget.wrap(provider))
        def completion(usage, role, _system, text):
            response = usage.recorded_completion(types.SimpleNamespace(max_retries=0), role,
                                                  messages=[{"role": "user", "content": text}])
            return response.choices[0].message.content
        with patch("socket.socket", side_effect=AssertionError("offline test opened a socket")), \
                patch.object(adapter, "dependency_manifests", return_value=self.dependencies), \
                patch.object(adapter, "install_live_boundary", side_effect=boundary), \
                patch.object(adapter, "complete_text", side_effect=completion):
            result = adapter.execute(self.args, self.request)
        budget = adapter.read_json(Path(result["files"]["model_budget"]["path"]))
        self.assertEqual(budget["limit"], 5)
        self.assertEqual(budget["attempts"], 4)
        self.assertEqual(len(provider_calls), 4)
        audit = adapter.read_json(Path(result["files"]["audit"]["path"]))
        self.assertEqual([row["status"] for row in audit["validation_attempts"]], ["rejected", "rejected", "candidate_validated", "passed"])

    def test_unsupported_evidence_is_regenerated_and_reviewed_again(self):
        self.payload["max_model_requests"] = 5
        calls = []
        def completion(_usage, _role, _system, text):
            supplied = json.loads(text)
            calls.append(supplied)
            if supplied.get("task") == "paragraph-evidence-review":
                return json.dumps(model_review(supplied, [supplied["paragraphs"][0]["path"]] if len(calls) == 2 else []), ensure_ascii=False)
            return model_response(supplied)
        with patch.object(adapter, "complete_text", side_effect=completion):
            result = adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
        self.assertEqual(len(calls), 4)
        self.assertIn("previous_candidate", calls[2]["correction"])
        self.assertEqual(calls[2]["correction"]["issues"][0]["code"], "evidence_not_supported")
        audit = adapter.read_json(Path(result["files"]["audit"]["path"]))
        self.assertEqual([row["status"] for row in audit["validation_attempts"]], ["candidate_validated", "evidence_rejected", "candidate_validated", "passed"])
        review = adapter.read_json(Path(result["files"]["evidence_review"]["path"]))
        self.assertTrue(review["all_paragraphs_supported"])
        self.assertEqual(review["issues"], [])

    def test_unreviewed_or_continuously_unsupported_report_is_not_published(self):
        for limit in (1, 5):
            with self.subTest(limit=limit):
                self.payload["max_model_requests"] = limit
                def completion(_usage, _role, _system, text):
                    supplied = json.loads(text)
                    if supplied.get("task") == "paragraph-evidence-review":
                        return json.dumps(model_review(supplied, [row["path"] for row in supplied["paragraphs"]]), ensure_ascii=False)
                    return model_response(supplied)
                with patch.object(adapter, "complete_text", side_effect=completion) as provider:
                    with self.assertRaises(adapter.AdapterError):
                        adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
                self.assertEqual(provider.call_count, limit - limit % 2)
                self.assertFalse((self.target / "report.html").exists())

    def test_evidence_issues_survive_a_later_numeric_correction(self):
        self.payload["max_model_requests"] = 5
        calls = []
        rejected_path = "$.sections[0].paragraphs[0]"
        def completion(_usage, _role, _system, text):
            supplied = json.loads(text)
            calls.append(supplied)
            if supplied.get("task") == "paragraph-evidence-review":
                return json.dumps(model_review(supplied, [rejected_path] if len(calls) == 2 else []), ensure_ascii=False)
            candidate = model_report(supplied)
            if len(calls) == 3:
                candidate["sections"][0]["paragraphs"][0]["text"] = "官方发布了2026年1月的更新公告。"
            return json.dumps(candidate, ensure_ascii=False)
        with patch("socket.socket", side_effect=AssertionError("offline test opened a socket")), \
                patch.object(adapter, "complete_text", side_effect=completion):
            result = adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
        correction = calls[3]["correction"]
        self.assertEqual(correction["issues"][0]["code"], "numeric_narrative")
        pending = correction["unresolved_evidence_issues"]
        self.assertEqual(len(pending), 1)
        self.assertEqual((pending[0]["code"], pending[0]["path"]), ("evidence_not_supported", rejected_path))
        self.assertEqual(correction["evidence_reviewed_paragraphs"][0]["path"], rejected_path)
        self.assertEqual(correction["evidence_reviewed_paragraphs"][0]["text"], calls[1]["paragraphs"][0]["text"])
        self.assertEqual(len(calls), 5)
        audit = adapter.read_json(Path(result["files"]["audit"]["path"]))
        self.assertEqual([row["status"] for row in audit["validation_attempts"]],
                         ["candidate_validated", "evidence_rejected", "rejected", "candidate_validated", "passed"])
        for index in (1, 3, 4):
            self.assertEqual(adapter.read_json(self.target / f"report-generation-input-{index}.json"), calls[index - 1])

    def test_timeout_evidence_and_numeric_failure_preserve_final_review_slot(self):
        self.payload["max_model_requests"] = 5
        calls = []
        def completion(_usage, _role, _system, text):
            supplied = json.loads(text)
            calls.append(supplied)
            if len(calls) == 1:
                raise adapter.ModelCallStopped("provider request failed", retryable=True, error_type="APITimeoutError")
            if supplied.get("task") == "paragraph-evidence-review":
                return json.dumps(model_review(supplied, [supplied["paragraphs"][0]["path"]]), ensure_ascii=False)
            candidate = model_report(supplied)
            if len(calls) == 4:
                candidate["sections"][0]["paragraphs"][0]["text"] = "官方发布了2026年1月的更新公告。"
            return json.dumps(candidate, ensure_ascii=False)
        with patch("socket.socket", side_effect=AssertionError("offline test opened a socket")), \
                patch.object(adapter, "complete_text", side_effect=completion) as provider:
            with self.assertRaisesRegex(adapter.AdapterError, "within its request budget"):
                adapter.downstream_role(self.args, self.request, self.payload, self.dependencies, self.target, None)
        self.assertEqual(provider.call_count, 4)
        self.assertEqual(calls[3]["correction"]["issues"][0]["code"], "evidence_not_supported")
        self.assertFalse((self.target / "report.html").exists())
        self.assertFalse((self.target / "report-evidence-review.json").exists())
        attempts = adapter.read_json(self.target / "report-validation-attempts.json")
        self.assertEqual([row["status"] for row in attempts],
                         ["provider_error", "candidate_validated", "evidence_rejected", "rejected"])
        self.assertEqual(adapter.read_json(self.target / "report-generation-input-1.json"), calls[0])


if __name__ == "__main__":
    unittest.main()
