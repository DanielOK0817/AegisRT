"""Offline guards against turning uncertain source assessments into facts."""
from __future__ import annotations

import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch


INTEGRATION = Path(__file__).resolve().parents[1]
PROJECT = INTEGRATION.parents[1]
spec = importlib.util.spec_from_file_location(
    "uncertainty_report_under_test", INTEGRATION / "comprehensive_report.py")
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)

TENTH_WORKFLOW = "bf-auto-small-930cf40ae64446a38a"
TENTH_REPORT_DIRECTORY = (
    PROJECT / "evidence" / "bettafish-automatic-validation-20261003"
    / "real-runs" / TENTH_WORKFLOW / "exported" / "run"
    / (TENTH_WORKFLOW + "-report"))
TENTH_HASHES = {
    "report-content.json": "c3f7ebd459b65c488ff93618360966b88ccf7efc026944d7b83e317f9017e069",
    "report-evidence.json": "50b6235caa796ffacf0ded14057d257540ee02610adb3a25c6d63fba1ccd405f",
    "report-evidence-review.json": "b98f7e47fd5057dd35ef1faf958d43657c6f42f9660e1e8287946242998e12af",
}
# Verbatim sentences from the failed independent semantic review. The fallback
# deliberately preserves the uncertainty and its unsupported categorical rewrite.
TENTH_SOURCE_SENTENCE = (
    "然而，个别自媒体报道提及的“V1.2热修包含黑神话策略战棋玩法、装备合成概率及跨服战等内容”"
    "（http://m.52jiewu.com/wzkx/176545.html），与游戏实际机制存在显著偏差，疑似信息失真；"
    "此前社区中关于“绝不出DLC修补”或“销量好评系政治任务刷单”等极端指控均缺乏来源佐证，"
    "定性为未证实主观言论。")
TENTH_GENERATED_SENTENCE = "部分媒体报道中的推测与个别自媒体的失真信息亦需进一步独立核验。"


class ReportUncertaintyTests(unittest.TestCase):
    def setUp(self):
        network = patch("socket.socket", side_effect=AssertionError(
            "Offline uncertainty test attempted a network call"))
        network.start()
        self.addCleanup(network.stop)
        self.bundle, self.candidate = self.make_fixture()

    @staticmethod
    def make_fixture(media_report=None, media_source=None, extra_media_sources=()):
        reports = {
            "query": "查询研究仅核对给定报道中的表述，保留待核验事项。",
            "media": TENTH_SOURCE_SENTENCE + "另有技术评测提到画面失真与图像失真。",
            "insight": "洞察研究仅归纳给定历史评论样本，不能代表全网。",
        }
        if media_report is not None:
            reports["media"] = media_report
        external = {
            "query": {"url": "https://example.test/query", "title": "社区讨论报道",
                      "content": "报道转述玩家对游戏体验的不同意见。"},
            "media": {"url": "https://example.test/media", "title": "来源可靠性分析",
                      "content": TENTH_SOURCE_SENTENCE},
        }
        if media_source is not None:
            external["media"]["content"] = media_source
        states = {role: {"paragraphs": [{"title": role + "研究角度", "research": {
            "latest_summary": text,
            "search_history": ([external[role]] + list(extra_media_sources) if role == "media"
                               else [external[role]] if role in external else []),
        }}]} for role, text in reports.items()}
        bundle = report.build_evidence(states, reports, {
            "C001": {"content": "客观评价也是需要的。",
                     "source_url": "https://example.test/comment"},
            "C002": {"content": "需要保留对不同体验的讨论。",
                     "source_url": "https://example.test/comment-other"},
        }, "主持讨论建议核实疑似失真消息。", max_content_length=12000)
        ids = {role: next(key for key in bundle["included_research_ids"]
            if bundle["research"][key]["role"] == role
            and bundle["research"][key]["kind"] == "report")
            for role in report.RESEARCH_ROLES}
        refs = {
            "summary": ([], [], list(ids.values())),
            "background": (bundle["included_source_ids"], [], []),
            "research": ([], [], list(ids.values())),
            "controversies": (bundle["included_source_ids"], [], [ids["media"]]),
            "comments": ([], ["C001"], []),
            "discussion": ([], [], [bundle["forum_id"]]),
            "limitations": ([], [], [ids["media"]]),
        }
        texts = {
            "summary": "给定材料应区分来源评估与已经核实的事实。",
            "background": "给定报道转述玩家的不同意见，并保留来源。",
            "research": "查询研究、媒体研究与洞察研究分别分析所引资料。",
            "controversies": "所引媒体研究对个别自媒体消息作出疑似信息失真的判断。",
            "comments": "给定评论提出客观评价的建议。",
            "discussion": "主持讨论建议核实疑似失真消息。",
            "limitations": "所引媒体研究认为个别自媒体消息疑似信息失真。",
        }
        candidate = {"schema": report.REPORT_SCHEMA, "sections": [
            {"key": key, "title": title, "paragraphs": [{"text": texts[key],
                "source_ids": refs[key][0], "comment_ids": refs[key][1],
                "research_ids": refs[key][2]}]}
            for key, title in report.SECTION_TITLES.items()]}
        return bundle, candidate

    @staticmethod
    def all_true_review(candidate):
        checks = []
        for paragraph in report._review_paragraphs(candidate):
            ids = paragraph["source_ids"] + paragraph["comment_ids"] + paragraph["research_ids"]
            checks.append({"path": paragraph["path"], "supported": True,
                "reason": "Deliberately fallible offline reviewer approves every sentence.",
                "evidence_ids": ids, "sentence_checks": [{
                    "index": sentence["index"], "supported": True,
                    "reason": "Deliberately fallible offline reviewer approves this sentence.",
                    "evidence_ids": ids,
                } for sentence in paragraph["sentences"]]})
        return {"checks": checks}

    def limitation(self):
        return self.candidate["sections"][6]["paragraphs"][0]

    def assert_rejected_at_both_gates(self, candidate=None, bundle=None, review=None,
                                     expected_path="$.sections[6].paragraphs[0]"):
        candidate = self.candidate if candidate is None else candidate
        bundle = self.bundle if bundle is None else bundle
        review = self.all_true_review(candidate) if review is None else review
        original = copy.deepcopy((candidate, bundle, review))
        for stage, validate in (
                ("report", lambda: report.validate_report(candidate, bundle)),
                ("evidence_review", lambda: report.validate_evidence_review(review, candidate, bundle))):
            with self.subTest(stage=stage):
                with self.assertRaises(report.ReportValidationError) as caught:
                    validate()
                self.assertTrue(any(issue["path"].startswith(expected_path)
                                    for issue in caught.exception.issues))
                self.assertTrue(any(issue["code"] == "unqualified_information_judgment"
                                    for issue in caught.exception.issues))
        self.assertEqual((candidate, bundle, review), original)

    def assert_accepted_without_rewriting(self):
        original = copy.deepcopy((self.candidate, self.bundle))
        self.assertEqual(report.validate_report(self.candidate, self.bundle), self.candidate)
        _, issues = report.validate_evidence_review(
            self.all_true_review(self.candidate), self.candidate, self.bundle)
        self.assertEqual(issues, [])
        self.assertEqual((self.candidate, self.bundle), original)

    def test_actual_tenth_immutable_candidate_is_rejected_despite_saved_true_review(self):
        if TENTH_REPORT_DIRECTORY.exists():
            records = {}
            for name, expected in TENTH_HASHES.items():
                with self.subTest(archive_file=name):
                    data = (TENTH_REPORT_DIRECTORY / name).read_bytes()
                    self.assertEqual(hashlib.sha256(data).hexdigest(), expected)
                    records[name] = json.loads(data.decode("utf-8"))
            candidate = records["report-content.json"]
            bundle = records["report-evidence.json"]
            review = records["report-evidence-review.json"]["review"]
            self.assertIn(TENTH_GENERATED_SENTENCE, candidate["sections"][6]["paragraphs"][0]["text"])
            self.assertTrue(review["checks"][6]["sentence_checks"][-1]["supported"])
            self.assert_rejected_at_both_gates(candidate, bundle, review)
        else:
            self.limitation()["text"] = TENTH_GENERATED_SENTENCE
            self.assert_rejected_at_both_gates()

    def test_minimal_verbatim_tenth_uncertainty_loss_is_rejected(self):
        self.limitation()["text"] = TENTH_GENERATED_SENTENCE
        self.assert_rejected_at_both_gates()

    def test_explicit_uncertainty_variants_are_accepted(self):
        for qualification in ("疑似信息失真", "疑似失真信息", "可能信息失真"):
            with self.subTest(qualification=qualification):
                self.limitation()["text"] = "所引媒体研究认为个别自媒体消息" + qualification + "，仍需核验。"
                self.assert_accepted_without_rewriting()

    def test_trailing_pending_verification_does_not_withdraw_categorical_claim(self):
        for text in ("个别自媒体的失真信息仍待核验。",
                     "个别自媒体报道存在信息失真，还需独立核对。",
                     "个别自媒体的消息属于失真信息，需进一步核验。"):
            with self.subTest(text=text):
                self.limitation()["text"] = text
                self.assert_rejected_at_both_gates()

    def test_previous_sentence_uncertainty_does_not_qualify_next_definite_claim(self):
        self.limitation()["text"] = "所引媒体研究认为个别自媒体消息疑似信息失真。该消息属于失真信息。"
        self.assert_rejected_at_both_gates()

    def test_unrelated_clause_uncertainty_does_not_qualify_distortion(self):
        self.limitation()["text"] = "其他传闻疑似未经证实，个别自媒体的报道属于失真信息。"
        self.assert_rejected_at_both_gates()

    def test_double_negation_does_not_make_definite_distortion_cautious(self):
        for text in ("不能否认个别自媒体的消息属于失真信息。",
                     "并非不能认定个别自媒体的报道存在信息失真。"):
            with self.subTest(text=text):
                self.limitation()["text"] = text
                self.assert_rejected_at_both_gates()

    def test_negated_uncertainty_is_not_a_cautious_assessment(self):
        self.limitation()["text"] = "该消息并非疑似失真信息，而是失真信息。"
        self.assert_rejected_at_both_gates()

    def test_direct_negation_of_a_definite_assessment_is_accepted(self):
        self.limitation()["text"] = "不能将疑似信息失真直接认定为确定的失真信息。"
        self.assert_accepted_without_rewriting()

    def test_visual_distortion_does_not_assert_unreliable_information(self):
        for text in ("所引技术评测提到画面失真。", "所引技术评测讨论图像失真。"):
            with self.subTest(text=text):
                self.limitation()["text"] = text
                self.assert_accepted_without_rewriting()

    def test_internal_modals_and_unconfirmed_authenticity_are_accepted(self):
        for text in ("所引报道的信息可能失真。", "所引报道的信息疑似失真。",
                     "所引消息真实性尚无法确认。", "所引报道可能属于虚假消息。",
                     "所引报道疑似不实报道。", "给定材料涉嫌伪造证据。"):
            with self.subTest(text=text):
                self.limitation()["text"] = text
                self.assert_accepted_without_rewriting()

    def test_falsity_label_variants_require_qualification(self):
        for text in ("该内容属于虚假消息，仍需核查。", "所引内容属于不实报道。",
                     "所引材料包含伪造证据，尚待核验。"):
            with self.subTest(text=text):
                self.limitation()["text"] = text
                self.assert_rejected_at_both_gates()

    def test_relative_evidence_quality_is_not_a_truth_label(self):
        self.limitation()["text"] = "所引媒体研究的证据相对扎实，但不能代表全网。"
        self.assert_accepted_without_rewriting()

    def media_report_id(self):
        return next(key for key in self.bundle["included_research_ids"]
                    if self.bundle["research"][key]["role"] == "media"
                    and self.bundle["research"][key]["kind"] == "report")

    def media_source_id(self, content=None):
        return next(key for key, row in self.bundle["sources"].items()
                    if "media" in row["roles"] and (content is None or row["content"] == content))

    def source_quote(self, text, source_id=None):
        self.limitation().update(text=text, research_ids=[],
                                source_ids=[source_id or self.media_source_id()])

    def test_exact_complete_attributed_media_report_unit_is_accepted(self):
        self.bundle, self.candidate = self.make_fixture(media_report="装备合成公告的信息失真。")
        self.limitation()["text"] = "媒体研究指出：装备合成公告的信息失真。"
        self.assert_accepted_without_rewriting()

    def test_exact_complete_attributed_external_source_unit_is_accepted(self):
        self.bundle, self.candidate = self.make_fixture(media_source="装备合成公告的信息失真。")
        self.source_quote("所引报道原文写道：装备合成公告的信息失真。")
        self.assert_accepted_without_rewriting()

    def test_complete_quote_cannot_borrow_a_supplied_uncited_source(self):
        self.bundle, self.candidate = self.make_fixture(media_source="装备合成公告的信息失真。")
        query_id = next(key for key, row in self.bundle["sources"].items() if row["roles"] == ["query"])
        self.source_quote("所引报道原文写道：装备合成公告的信息失真。", query_id)
        self.assert_rejected_at_both_gates()

    def test_complete_quote_cannot_use_retained_but_unsupplied_source(self):
        sentence = "装备合成公告的信息失真。"
        extra = {"url": "https://example.test/retained", "title": "保留原件",
                 "content": sentence}
        self.bundle, self.candidate = self.make_fixture(extra_media_sources=[extra])
        key = self.media_source_id(sentence)
        self.bundle["model_input"]["sources"] = [row for row in self.bundle["model_input"]["sources"]
                                                  if row["id"] != key]
        self.bundle["included_source_ids"].remove(key)
        self.bundle["coverage"]["included_source_count"] -= 1
        self.bundle["coverage"]["omitted_source_count"] += 1
        self.bundle["model_input"]["coverage"]["sources_supplied"] -= 1
        for section in self.candidate["sections"]:
            for paragraph in section["paragraphs"]:
                paragraph["source_ids"] = [ref for ref in paragraph["source_ids"] if ref != key]
        self.source_quote("所引报道原文写道：装备合成公告的信息失真。", key)
        self.assertIn(key, self.bundle["sources"])
        self.assert_rejected_at_both_gates()

    def test_matching_other_role_does_not_change_query_attribution(self):
        self.bundle, self.candidate = self.make_fixture(media_report="装备合成公告的信息失真。")
        query_id = next(key for key in self.bundle["included_research_ids"]
                        if self.bundle["research"][key]["role"] == "query"
                        and self.bundle["research"][key]["kind"] == "report")
        self.limitation().update(text="查询研究指出：装备合成公告的信息失真。",
                                 research_ids=[query_id, self.media_report_id()])
        self.assert_rejected_at_both_gates()

    def test_source_confirmation_cannot_change_what_named_media_report_says(self):
        self.bundle, self.candidate = self.make_fixture(media_source="装备合成公告的信息失真。")
        self.limitation().update(text="媒体研究指出：装备合成公告的信息失真。",
                                 source_ids=[self.media_source_id()])
        self.assert_rejected_at_both_gates()

    def test_quote_must_match_a_whole_supplied_unit_not_an_internal_fragment(self):
        self.bundle, self.candidate = self.make_fixture(
            media_source="官方勘误针对装备合成公告指出信息失真。")
        self.source_quote("所引报道原文写道：信息失真。")
        self.assert_rejected_at_both_gates()

    def test_comma_does_not_allow_dropping_source_uncertainty(self):
        self.bundle, self.candidate = self.make_fixture(
            media_report="尚不能确定，个别报道存在信息失真。")
        self.limitation()["text"] = "媒体研究指出：个别报道存在信息失真。"
        self.assert_rejected_at_both_gates()

    def test_truncated_final_source_unit_cannot_authorize_definite_quote(self):
        complete = "已给出来源线索。装备合成公告的信息失真，但该判断仍需核验。"
        for ending in ("装备合成公告的信息失真", "装备合成公告的信息失真，"):
            with self.subTest(ending=ending):
                self.bundle, self.candidate = self.make_fixture(media_source=complete)
                key = self.media_source_id()
                supplied = next(row for row in self.bundle["model_input"]["sources"] if row["id"] == key)
                supplied.update(excerpt="已给出来源线索。" + ending, excerpt_complete=False)
                self.source_quote("所引报道原文写道：装备合成公告的信息失真。", key)
                self.assert_rejected_at_both_gates()

    def test_truncation_of_later_paragraph_does_not_hide_earlier_complete_paragraph(self):
        self.bundle, self.candidate = self.make_fixture(
            media_source="装备合成公告的信息失真。\n\n后续不同消息仍需进一步核验。")
        key = self.media_source_id()
        supplied = next(row for row in self.bundle["model_input"]["sources"] if row["id"] == key)
        supplied.update(excerpt="装备合成公告的信息失真。\n\n后续不同消息", excerpt_complete=False)
        self.source_quote("所引报道原文写道：装备合成公告的信息失真。", key)
        self.assert_accepted_without_rewriting()

    def test_blank_line_separates_confirmed_object_from_different_uncertain_object(self):
        self.bundle, self.candidate = self.make_fixture(
            media_report="装备合成公告的信息失真。\n\n另一跨服战消息疑似信息失真。")
        self.limitation()["text"] = "媒体研究指出：装备合成公告的信息失真。"
        self.assert_accepted_without_rewriting()

    def test_uncertainty_suffix_under_negation_does_not_license_a_fact(self):
        for text in ("该消息并非信息失真的可能性，而是已经确定的事实。",
                     "这不是伪造证据的嫌疑，而是已经确定的事实。"):
            with self.subTest(text=text):
                self.limitation()["text"] = text
                self.assert_rejected_at_both_gates()

    def test_copular_fabrication_is_an_unqualified_truth_judgment(self):
        self.limitation()["text"] = "消息纯属捏造。"
        self.assert_rejected_at_both_gates()

    def test_cannot_yet_assert_a_definite_label_is_cautious(self):
        self.limitation()["text"] = "尚不能断言这是失真信息。"
        self.assert_accepted_without_rewriting()

    def test_direct_denial_is_still_a_definite_truth_judgment(self):
        self.limitation()["text"] = "这并不是失真信息。"
        self.assert_rejected_at_both_gates()

    def test_attributed_question_cannot_be_changed_to_a_definite_statement(self):
        for question in ("？", "?"):
            with self.subTest(question=question):
                self.bundle, self.candidate = self.make_fixture(media_report="这条消息不实" + question)
                self.limitation()["text"] = "媒体研究指出：这条消息不实。"
                self.assert_rejected_at_both_gates()

    def test_exact_attributed_question_remains_a_question(self):
        for question in ("？", "?"):
            with self.subTest(question=question):
                self.bundle, self.candidate = self.make_fixture(media_report="这条消息不实" + question)
                self.limitation()["text"] = "媒体研究指出：这条消息不实" + question
                self.assert_accepted_without_rewriting()

    def test_complete_attributed_source_paragraph_keeps_all_sentences(self):
        paragraph = "官方勘误指出装备合成公告的信息失真。处理意见是更新原始记录。"
        self.bundle, self.candidate = self.make_fixture(media_report=paragraph)
        self.limitation()["text"] = "媒体研究指出：" + paragraph
        self.assert_accepted_without_rewriting()

    def test_complete_sentence_is_not_complete_source_paragraph(self):
        self.bundle, self.candidate = self.make_fixture(
            media_report="尚不能确定该说法是否失实。个别报道存在信息失真。")
        self.limitation()["text"] = "媒体研究指出：个别报道存在信息失真。"
        self.assert_rejected_at_both_gates()

    def test_semicolon_cannot_drop_different_object_inside_source_paragraph(self):
        self.bundle, self.candidate = self.make_fixture(
            media_report="装备合成公告的信息失真；另一跨服战消息疑似信息失真。")
        self.limitation()["text"] = "媒体研究指出：装备合成公告的信息失真。"
        self.assert_rejected_at_both_gates()

    def test_global_pending_qualification_cannot_be_removed_from_later_paragraph(self):
        self.bundle, self.candidate = self.make_fixture(
            media_report="以下说法均待核实。\n\n装备合成公告的信息失真。")
        self.limitation()["text"] = "媒体研究指出：装备合成公告的信息失真。"
        self.assert_rejected_at_both_gates()

    def test_ambiguous_media_report_attribution_does_not_enable_quote_exception(self):
        sentence = "装备合成公告的信息失真。"
        self.bundle, self.candidate = self.make_fixture(media_report=sentence, media_source=sentence)
        self.limitation().update(text="媒体报告指出：" + sentence,
                                 source_ids=[self.media_source_id()])
        self.assert_rejected_at_both_gates()

    def test_section_title_cannot_make_an_unqualified_truth_judgment(self):
        for title in ("失真信息", "虚假消息", "伪造证据"):
            with self.subTest(title=title):
                self.candidate["sections"][6]["title"] = title
                self.assert_rejected_at_both_gates(expected_path="$.sections[6].title")


class ReportingUncertaintyCorrectionTests(unittest.TestCase):
    def setUp(self):
        helper_spec = importlib.util.spec_from_file_location(
            "uncertainty_reporting_fixture", INTEGRATION / "tests" / "test_reporting_integration.py")
        self.helper = importlib.util.module_from_spec(helper_spec)
        helper_spec.loader.exec_module(self.helper)
        self.adapter = self.helper.adapter
        self.fixture = self.helper.ReportingIntegrationTests(
            "test_live_adapter_produces_all_sections_and_original_evidence")
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        network = patch("socket.socket", side_effect=AssertionError(
            "Offline uncertainty correction attempted a network call"))
        network.start()
        self.addCleanup(network.stop)
        self.fixture.args.env_file = str(self.fixture.root / "offline-configuration.txt")
        Path(self.fixture.args.env_file).write_text("# offline test fixture\n", encoding="utf-8")
        self.fixture.args.resume_dir = None
        self.fixture.request["goal"] = json.dumps({"topic": self.fixture.topic, "max_model_requests": 5})
        media = self.fixture.dependencies["media"]
        media_report = Path(media["files"]["report"]["path"])
        media_report.write_text(TENTH_SOURCE_SENTENCE, encoding="utf-8")
        state_path = Path(media["files"]["state"]["path"])
        state = self.adapter.read_json(state_path)
        state["research_state"]["paragraphs"][0]["research"]["latest_summary"] = TENTH_SOURCE_SENTENCE
        self.adapter.atomic_json(state_path, state)
        media["files"]["report"] = self.adapter.file_record(media_report)
        media["files"]["state"] = self.adapter.file_record(state_path)
        self.output = self.fixture.root / self.fixture.request["workflow_id"] / self.fixture.request["task_id"]

    def execute_with_offline_provider(self, continuous_error=False):
        calls = []
        def provider(_client, _role, **kwargs):
            supplied = json.loads(kwargs["messages"][1]["content"])
            calls.append(supplied)
            self.assertEqual(kwargs["response_format"], {"type": "json_object"})
            if supplied.get("task") == "paragraph-evidence-review":
                value = self.helper.model_review(supplied)
            else:
                value = self.helper.model_report(supplied)
                paragraph = value["sections"][6]["paragraphs"][0]
                paragraph.update(source_ids=[], research_ids=[next(row["id"]
                    for row in supplied["research"] if row["role"] == "media" and row["kind"] == "report")])
                paragraph["text"] = (TENTH_GENERATED_SENTENCE if continuous_error or len(calls) == 1
                                     else "所引媒体研究认为个别自媒体消息疑似信息失真，仍需核验。")
            return types.SimpleNamespace(choices=[types.SimpleNamespace(finish_reason="stop",
                message=types.SimpleNamespace(content=json.dumps(value, ensure_ascii=False)))])
        def boundary(_source, budget, *_args):
            return types.SimpleNamespace(recorded_completion=budget.wrap(provider))
        config = types.ModuleType("config")
        config.settings = types.SimpleNamespace(REPORT_ENGINE_API_KEY="offline-placeholder",
            REPORT_ENGINE_BASE_URL="https://unused.invalid", REPORT_ENGINE_MODEL_NAME="offline-fixture")
        openai = types.ModuleType("openai")
        def offline_client(**kwargs):
            self.assertEqual(kwargs["max_retries"], 0)
            return types.SimpleNamespace(max_retries=0)
        openai.OpenAI = offline_client
        with patch.dict(sys.modules, {"config": config, "openai": openai}), \
                patch.object(self.adapter, "dependency_manifests", return_value=self.fixture.dependencies), \
                patch.object(self.adapter, "install_live_boundary", side_effect=boundary):
            if continuous_error:
                with self.assertRaisesRegex(self.adapter.AdapterError, "within its request budget"):
                    self.adapter.execute(self.fixture.args, self.fixture.request)
                result = None
            else:
                result = self.adapter.execute(self.fixture.args, self.fixture.request)
        return calls, result

    def test_real_adapter_rejects_definite_label_then_corrects_within_same_budget(self):
        calls, result = self.execute_with_offline_provider()
        self.assertEqual(len(calls), 3)
        self.assertNotIn("task", calls[0])
        self.assertNotIn("task", calls[1])
        self.assertEqual(calls[2]["task"], "paragraph-evidence-review")
        correction = calls[1]["correction"]
        self.assertEqual(correction["issues"][0]["code"], "unqualified_information_judgment")
        self.assertEqual(correction["previous_candidate"]["sections"][6]["paragraphs"][0]["text"],
                         TENTH_GENERATED_SENTENCE)
        budget = self.adapter.read_json(self.output / "model-budget.json")
        self.assertEqual((budget["limit"], budget["attempts"], budget["failed_attempts"]), (5, 3, 0))
        self.assertEqual(len(budget["provider_attempts"]), 3)
        audit = self.adapter.read_json(self.output / "report-audit.json")
        self.assertEqual([row["status"] for row in audit["validation_attempts"]],
                         ["rejected", "candidate_validated", "passed"])
        final = self.adapter.read_json(self.output / "report-content.json")
        self.assertIn("疑似信息失真", final["sections"][6]["paragraphs"][0]["text"])
        self.assertIn(TENTH_GENERATED_SENTENCE,
                      (self.output / "model-report-response-1.txt").read_text(encoding="utf-8"))
        self.assertTrue(Path(result["files"]["report"]["path"]).is_file())
        self.assertTrue((self.output / "result.json").is_file())

    def test_continuous_false_labels_preserve_review_slot_without_accepted_outputs(self):
        calls, result = self.execute_with_offline_provider(continuous_error=True)
        self.assertIsNone(result)
        self.assertEqual(len(calls), 4)
        self.assertTrue(all(row.get("task") != "paragraph-evidence-review" for row in calls))
        budget = self.adapter.read_json(self.output / "model-budget.json")
        self.assertEqual((budget["limit"], budget["attempts"]), (5, 4))
        self.assertEqual(len(budget["provider_attempts"]), 4)
        attempts = self.adapter.read_json(self.output / "report-validation-attempts.json")
        self.assertEqual(len(attempts), 4)
        self.assertTrue(all(row["status"] == "rejected" and
                            row["issues"][0]["code"] == "unqualified_information_judgment"
                            for row in attempts))
        self.assertEqual(self.adapter.read_json(self.output / "state.json")["status"], "failed")
        for name in ("report.html", "report-content.json", "report-evidence-review.json", "result.json"):
            self.assertFalse((self.output / name).exists(), name)
        for index in range(1, 5):
            self.assertTrue((self.output / f"model-report-response-{index}.txt").is_file())
            self.assertTrue((self.output / f"report-generation-input-{index}.json").is_file())


if __name__ == "__main__":
    unittest.main(verbosity=2)
