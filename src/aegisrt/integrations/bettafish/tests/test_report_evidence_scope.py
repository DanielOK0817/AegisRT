"""Offline regressions reduced from the eighth real Report failure.

The excerpts below are verbatim subsets of the archived supplied evidence.
Fixture IDs are rebuilt from those subsets; they are not the historical IDs.
The original candidate/evidence/review archives are only read, never changed.
"""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch


INTEGRATION = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("scope_report_under_test", INTEGRATION / "comprehensive_report.py")
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)

ORIGINAL_WORKFLOW = "bf-auto-small-26f3c4e359284721b6"
ORIGINAL_SHA256 = {
    "model-report-response-4.txt": "3a27f8a86c0c725502f5ecf04aa4d5264f52c9d1602a4b314907aa26ad229722",
    "report-evidence.json": "3f6a4ecdd0c5131fee6e51b42893e3d05e84792bdc8c69629b7120a27abe28b6",
    "report-evidence-review-5.json": "0877fbdaf0ec0111c4db96eae1759c45c2b904b18b9f861bd812793b88ac7561",
}
QUERY_ANALYSIS = "禁止讨论话题清单和办公室不当行为的指控为媒体转述，未获游戏科学或相关方直接回应，应标为未证实。"
MEDIA_ANALYSIS = "现有材料缺乏全硬件段卡顿掉帧的无偏差定量普查，Steam差评中闪退比例尚缺精确统计；"
INSIGHT_ANALYSIS = (
    "评议反馈提及的性别争议、媒体不当行为指控及相关管控传闻，在当前样本中均无直接报道与核验事实，"
    "均标注为未证实指控，坚决不把意见与传闻当作事实。"
)
QUERY_SOURCE = "有网友怀疑，IGN指控游戏科学性别歧视的报道，或许是和SBI合流的结果。"
MEDIA_SOURCE = (
    "据Digital Foundry的最新评测显示，即使在开启了帧生成和FSR 3技术的1080P分辨率下，"
    "《黑神话：悟空》在PS5 Pro的性能模式下仍然无法稳定达到60帧。"
)
UNCITED_ASSESSMENT = (
    "此外，游戏还面临场外争议。有报道提及社交平台上围绕游戏产生的非理性言论导致部分账号被禁言，"
    "同时也有媒体转述了关于开发团队过往言论的性别争议指控，"
    "但这些指控在当前材料中缺乏当事方的直接回应与独立核验。"
)
UNCITED_MEDIA_LIMITATION = (
    "本报告的分析受限于当前提供的检索材料与评论样本。"
    "现有材料缺乏全硬件段卡顿掉帧的无偏差定量普查，也未提供各大平台评价分层的量化数据，"
    "因此无法推算全网舆情的真实倾向。"
)
EXPANDED_ABSENCE = (
    "此外，关于性别争议、媒体不当行为指控及相关管控传闻等场外议题，"
    "在当前材料中均无直接报道与核验事实，只能作为未证实指控处理。"
)


class ReportEvidenceScopeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # The functional tests remain self-contained in the frozen container.
        # On the host, bind the reduced fixture back to the untouched real files.
        original = INTEGRATION.parents[1] / "evidence/bettafish-automatic-validation-20261003/real-runs" / (
            ORIGINAL_WORKFLOW + "/exported/run/" + ORIGINAL_WORKFLOW + "-report")
        if original.is_dir():
            for name, expected in ORIGINAL_SHA256.items():
                if hashlib.sha256((original / name).read_bytes()).hexdigest() != expected:
                    raise AssertionError("Historical evidence bytes changed: " + name)
            bundle = json.loads((original / "report-evidence.json").read_text(encoding="utf-8-sig"))
            raw = (original / "model-report-response-4.txt").read_text(encoding="utf-8-sig")
            for excerpt in (UNCITED_ASSESSMENT, UNCITED_MEDIA_LIMITATION, EXPANDED_ABSENCE):
                if excerpt not in raw:
                    raise AssertionError("Candidate regression is not an exact historical excerpt")
            for role, excerpt in (("query", QUERY_ANALYSIS), ("media", MEDIA_ANALYSIS), ("insight", INSIGHT_ANALYSIS)):
                if not any(row["role"] == role and excerpt in row["text"] for row in bundle["model_input"]["research"]):
                    raise AssertionError("Research regression is not an exact supplied excerpt: " + role)
            for excerpt in (QUERY_SOURCE, MEDIA_SOURCE):
                if not any(excerpt in row["excerpt"] for row in bundle["model_input"]["sources"]):
                    raise AssertionError("Source regression is not an exact supplied excerpt")

    def setUp(self):
        self.network = patch("socket.socket", side_effect=AssertionError("Offline report scope test attempted a network call"))
        self.network.start()
        self.addCleanup(self.network.stop)
        reports = {"query": QUERY_ANALYSIS, "media": MEDIA_ANALYSIS, "insight": INSIGHT_ANALYSIS}
        sources = {
            "query": {"url": "https://news.qq.com/rain/a/20240624A01JIP00", "title": "场外争议报道", "content": QUERY_SOURCE},
            "media": {"url": "http://m.itbear.com.cn/html/2024-12/657679.html", "title": "性能模式评测报道", "content": MEDIA_SOURCE},
        }
        states = {role: {"paragraphs": [{"title": role + "研究角度", "research": {
            "latest_summary": text, "search_history": [sources[role]] if role in sources else []}}]}
            for role, text in reports.items()}
        comments = {"C001": {"content": "看上去能走其实不能走，确实是一个不可忽视的缺点。",
                             "source_url": "https://www.bilibili.com/video/BV1P4421f79Y/#reply238210853472"},
                    "C002": {"content": "客观评价也是需要的。",
                             "source_url": "https://www.bilibili.com/video/BV1P4421f79Y/#reply238208235264"}}
        self.bundle = report.build_evidence(states, reports, comments,
            "建议核查游戏科学官方回应、微博原始封禁公告及清单原件，以确认事实边界。", max_content_length=12000)
        self.ids = {role: next(key for key in self.bundle["included_research_ids"]
            if self.bundle["research"][key]["role"] == role and self.bundle["research"][key]["kind"] == "report")
            for role in report.RESEARCH_ROLES}
        self.sources = self.bundle["included_source_ids"]
        references = {
            "summary": ([], [], list(self.ids.values())),
            "background": (self.sources, [], []),
            "research": ([], [], list(self.ids.values())),
            "controversies": (self.sources, [], [self.ids["query"]]),
            "comments": ([], ["C001", "C002"], []),
            "discussion": ([], [], [self.bundle["forum_id"]]),
            "limitations": ([], [], [self.ids["insight"]]),
        }
        texts = {
            "summary": "给定材料应区分媒体转述与评论观点，并保留未证实说明。",
            "background": "给定报道提到相关争议，技术报道记录了性能测试。",
            "research": "研究角色分别给出资料核查与样本限制的分析意见。",
            "controversies": "查询分析指出，相关指控为媒体转述，未获相关方直接回应，应标为未证实。",
            "comments": "评论样本中有用户指出探索缺点，也有用户提出应客观评价。",
            "discussion": "主持讨论建议核查官方回应与原始公告，以确认事实边界。",
            "limitations": "洞察视角指出，场外争议在当前评论样本中缺乏直接报道与核验事实。",
        }
        self.candidate = {"schema": report.REPORT_SCHEMA, "sections": [
            {"key": key, "title": title, "paragraphs": [{"text": texts[key],
                "source_ids": references[key][0], "comment_ids": references[key][1], "research_ids": references[key][2]}]}
            for key, title in report.SECTION_TITLES.items()]}

    def paragraph(self, key):
        return next(row for row in self.candidate["sections"] if row["key"] == key)["paragraphs"][0]

    def true_review(self):
        """A deliberately fallible reviewer must not overrule deterministic gaps."""
        checks = []
        for paragraph in report._review_paragraphs(self.candidate):
            ids = paragraph["source_ids"] + paragraph["comment_ids"] + paragraph["research_ids"]
            check = {"path": paragraph["path"], "supported": True,
                "reason": "Offline reviewer fixture marks the supplied citations supported.", "evidence_ids": ids}
            if "sentences" in paragraph:
                check["sentence_checks"] = [{"index": sentence["index"], "supported": True,
                    "reason": "Offline reviewer fixture marks this exact sentence supported.", "evidence_ids": ids}
                    for sentence in paragraph["sentences"]]
            checks.append(check)
        return {"checks": checks}

    def assert_rejected_with_true_review(self, paragraph_path):
        for stage, validate in (
                ("report", lambda: report.validate_report(self.candidate, self.bundle)),
                ("evidence_review", lambda: report.validate_evidence_review(self.true_review(), self.candidate, self.bundle))):
            with self.subTest(stage=stage):
                with self.assertRaises(report.ReportValidationError) as caught:
                    validate()
                self.assertTrue(any(issue["path"].startswith(paragraph_path) for issue in caught.exception.issues))

    def assert_approved_without_rewriting(self):
        original = copy.deepcopy((self.candidate, self.bundle))
        self.assertEqual(report.validate_report(self.candidate, self.bundle), self.candidate)
        _, issues = report.validate_evidence_review(self.true_review(), self.candidate, self.bundle)
        self.assertEqual(issues, [])
        self.assertEqual((self.candidate, self.bundle), original)

    def test_actual_eighth_source_assessment_without_research_reference_is_rejected(self):
        paragraph = self.paragraph("controversies")
        paragraph.update(text=UNCITED_ASSESSMENT, research_ids=[])
        self.assert_rejected_with_true_review("$.sections[3].paragraphs[0]")

    def test_benchmark_gap_without_material_keyword_requires_local_scope(self):
        # Exact sentence from bf-complete-aegisrt-r1, final rejected candidate.
        self.paragraph("controversies").update(
            text="由于缺乏独立硬件测试机构的基准数据，这些优化问题的实际表现尚无法确认。",
            research_ids=[])
        self.assert_rejected_with_true_review("$.sections[3].paragraphs[0]")

    def test_audit_and_patch_artifact_gaps_require_scope(self):
        for artifact in ("官方审计报告", "官方补丁日志", "独立测试报告"):
            with self.subTest(artifact=artifact):
                self.paragraph("controversies").update(
                    text="由于缺乏" + artifact + "，相关结论仍需核验。", research_ids=[])
                self.assert_rejected_with_true_review("$.sections[3].paragraphs[0]")

    def test_artifact_gap_with_explicit_cited_subset_is_structurally_valid(self):
        self.paragraph("controversies").update(
            text="本段所引来源摘录未提供独立硬件测试机构的基准数据。", research_ids=[])
        self.assert_approved_without_rewriting()

    def test_negated_artifact_absence_is_not_a_gap_assertion(self):
        self.paragraph("controversies").update(
            text="不能据此断言缺乏官方审计报告。", research_ids=[])
        self.assert_approved_without_rewriting()

    def test_same_source_assessment_with_actual_query_analysis_reference_is_accepted(self):
        self.paragraph("controversies").update(
            text="查询分析指出，办公室不当行为的指控为媒体转述，未获游戏科学或相关方直接回应，应标为未证实。",
            research_ids=[self.ids["query"]])
        self.assert_approved_without_rewriting()

    def test_actual_eighth_media_limitation_cannot_borrow_uncited_research(self):
        self.paragraph("limitations").update(text=UNCITED_MEDIA_LIMITATION,
            research_ids=[self.ids["query"], self.ids["insight"]])
        self.assert_rejected_with_true_review("$.sections[6].paragraphs[0]")

    def test_media_limitation_with_same_paragraph_media_reference_is_accepted(self):
        self.paragraph("limitations").update(
            text="媒体研究指出，现有材料缺乏全硬件段卡顿掉帧的无偏差定量普查。",
            research_ids=[self.ids["media"]])
        self.assert_approved_without_rewriting()

    def test_actual_eighth_sample_absence_cannot_expand_to_all_supplied_material(self):
        self.paragraph("limitations").update(text=EXPANDED_ABSENCE,
            research_ids=[self.ids["query"], self.ids["insight"]])
        self.assert_rejected_with_true_review("$.sections[6].paragraphs[0]")

    def test_absence_remains_valid_with_explicit_insight_sample_scope(self):
        self.paragraph("limitations").update(
            text="洞察分析指出，关于性别争议、媒体不当行为指控及相关管控传闻，在当前样本中均无直接报道与核验事实。",
            research_ids=[self.ids["insight"]])
        self.assert_approved_without_rewriting()

    def test_previous_sentence_sample_qualifier_does_not_authorize_next_global_absence(self):
        self.paragraph("limitations").update(
            text="洞察分析仅基于给定评论样本。当前材料中均无直接报道与核验事实。",
            research_ids=[self.ids["insight"]])
        self.assert_rejected_with_true_review("$.sections[6].paragraphs[0]")

    def test_explicit_media_attribution_cannot_use_only_query_report_reference(self):
        self.paragraph("limitations").update(
            text="媒体研究指出，现有材料缺乏全硬件段卡顿掉帧的无偏差定量普查。",
            research_ids=[self.ids["query"]])
        self.assert_rejected_with_true_review("$.sections[6].paragraphs[0]")

    def test_all_research_references_do_not_repair_expanded_sample_absence(self):
        self.paragraph("limitations").update(text=EXPANDED_ABSENCE,
            research_ids=list(self.ids.values()))
        self.assert_rejected_with_true_review("$.sections[6].paragraphs[0]")

    def test_cited_source_excerpt_absence_can_remain_explicitly_local_without_research(self):
        self.paragraph("controversies").update(
            text="本段所引来源摘录未提供当事方的直接回应与独立核验，不能据此断言相关指控属实。",
            research_ids=[])
        self.assert_approved_without_rewriting()

    def test_directly_negated_global_absence_is_allowed_with_sources_only(self):
        self.paragraph("controversies").update(
            text="不能断言当前材料中均无直接报道。", research_ids=[])
        self.assert_approved_without_rewriting()

    def test_not_no_reporting_is_allowed_with_sources_only(self):
        self.paragraph("controversies").update(
            text="当前材料并非没有直接报道。", research_ids=[])
        self.assert_approved_without_rewriting()

    def test_cannot_deny_global_absence_still_requires_a_supported_scope(self):
        self.paragraph("controversies").update(
            text="不能否认当前材料中均无直接报道。", research_ids=[])
        self.assert_rejected_with_true_review("$.sections[3].paragraphs[0]")

    def test_double_negation_cannot_turn_global_absence_into_a_valid_disclaimer(self):
        self.paragraph("controversies").update(
            text="并非不能断言当前材料中均无直接报道。", research_ids=[])
        self.assert_rejected_with_true_review("$.sections[3].paragraphs[0]")

    def test_ordinary_media_analysis_is_not_the_media_research_role(self):
        self.paragraph("controversies").update(
            text="媒体分析指出，所引报道呈现观点分歧。", research_ids=[])
        self.assert_approved_without_rewriting()

    def test_real_warning_against_scope_expansion_is_not_an_absence_assertion(self):
        self.paragraph("limitations").update(
            text="洞察研究关于评论样本没找到报道的判断只属于该样本，不能扩大成查询研究、媒体研究和全部材料都无报道。",
            research_ids=list(self.ids.values()))
        self.assert_approved_without_rewriting()

    def test_real_comparison_of_reporting_and_verification_is_not_global_absence(self):
        self.paragraph("limitations").update(
            text="在查询研究与媒体研究引用的给定材料子集中，有相关报道但缺独立核验，与没有相关报道是不同判断；不可互换。",
            research_ids=[self.ids["query"], self.ids["media"]])
        self.assert_approved_without_rewriting()

    def test_real_warning_can_name_alternative_roles_and_current_material(self):
        self.paragraph("limitations").update(
            text="洞察研究明确指出，其关于评论样本没找到报道的判断仅限于该冻结样本，不能扩大为查询研究、媒体研究或当前全部材料都无报道。",
            research_ids=list(self.ids.values()))
        self.assert_approved_without_rewriting()

    def test_warning_cannot_hide_later_affirmative_absence_or_double_negation(self):
        for text in ("不能扩大成查询研究、媒体研究和全部材料都无报道，但当前材料确实都无报道。",
                     "并非不能扩大成查询研究、媒体研究和全部材料都无报道。",
                     "不能否认全部材料都无报道。"):
            with self.subTest(text=text):
                self.paragraph("limitations").update(text=text, research_ids=list(self.ids.values()))
                self.assert_rejected_with_true_review("$.sections[6].paragraphs[0]")

    def test_indefinite_qualitative_noun_is_not_a_sample_statistic(self):
        self.assertFalse(report._has_unsupported_numbers("空气墙是一个不可忽视的缺点。"))
        for text in ("只有一个缺点。", "共发现一个问题。", "一个玩家提出批评。", "这是一个风险，影响两个玩家。"):
            with self.subTest(text=text):
                self.assertTrue(report._has_unsupported_numbers(text))


if __name__ == "__main__":
    unittest.main(verbosity=2)
