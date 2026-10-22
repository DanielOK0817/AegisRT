"""Offline contract and adversarial checks for comprehensive reporting."""
import copy
import html
import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch

_SPEC = importlib.util.spec_from_file_location(
    "comprehensive_report_under_test", Path(__file__).resolve().parents[1] / "comprehensive_report.py")
report = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(report)


class ComprehensiveReportTests(unittest.TestCase):
    def setUp(self):
        self.states = {}
        self.reports = {}
        for role in report.RESEARCH_ROLES:
            self.states[role] = {"query": "测试话题", "is_completed": True, "paragraphs": [
                {"title": role + "研究角度", "research": {"is_completed": True,
                 "latest_summary": role + "材料描述不同的关注点，仍需核查原始出处。",
                 "search_history": [{"url": "https://example.com/" + role,
                                     "title": role + "来源资料",
                                     "content": role + "原始摘录：2024年有相关讨论，数字仅属来源表述。"}]}}]}
            self.reports[role] = role + "完整研究正文：材料中的主张与评论需要分别说明，原始数字2024予以保留。"
        self.comments = {
            "C001": {"comment_id": "original-a", "content": "我在2024年看到过这类讨论。", "nickname": "甲",
                     "like_count": 3, "source_url": "https://example.com/video?a=1"},
            "C002": {"comment_id": "original-b", "content": "部分说法仍须核查原始依据。", "nickname": "乙",
                     "like_count": 4, "source_url": "https://example.com/video?a=2"},
        }
        self.forum = "主持讨论原文：不同材料互有分歧，应回到原始出处，并保留未证实说明。"

    def bundle(self, **options):
        return report.build_evidence(self.states, self.reports, self.comments, self.forum, **options)

    @staticmethod
    def candidate(bundle):
        research_ids = [key for key in bundle["included_research_ids"]
                        if bundle["research"][key]["kind"] == "report"]
        source_ids = bundle["included_source_ids"][:2]
        refs = {
            "summary": ([], [], research_ids),
            "background": (source_ids, [], research_ids[:1]),
            "research": ([], [], research_ids),
            "controversies": (source_ids, [], research_ids[:1]),
            "comments": ([], bundle["included_comment_ids"][:2], []),
            "discussion": ([], [], [bundle["forum_id"]]),
            "limitations": ([], [], [bundle["forum_id"]]),
        }
        return {"schema": report.REPORT_SCHEMA, "sections": [
            {"key": key, "title": title, "paragraphs": [{
                "text": "给定材料呈现不同关注点，相关主张仍需核查原始出处。",
                "source_ids": refs[key][0], "comment_ids": refs[key][1], "research_ids": refs[key][2]}]}
            for key, title in report.SECTION_TITLES.items()]}

    def test_full_sections_and_each_evidence_role_are_rendered_without_network(self):
        with patch("socket.socket", side_effect=AssertionError("reporting attempted network access")):
            bundle = self.bundle()
            candidate = self.candidate(bundle)
            validated = report.validate_report(candidate, bundle)
            page = report.render_report("测试话题", candidate, bundle, "test-run", "live")
        self.assertEqual(validated, candidate)
        self.assertIsNot(validated, candidate)
        self.assertEqual(len(candidate["sections"]), 7)
        for title in report.SECTION_TITLES.values():
            self.assertIn("<h2>" + title + "</h2>", page)
        for value in self.reports.values():
            self.assertIn(html.escape(value), page)
        self.assertIn(html.escape(self.forum), page)
        self.assertIn("冻结原始评论2条", page)
        self.assertIn("来自1个来源页面", page)
        self.assertIn("未独立核实", page)
        self.assertFalse(bundle["coverage"]["model_fact_check_performed"])

    def test_stable_ids_do_not_depend_on_role_dictionary_order(self):
        first = self.bundle()
        second = report.build_evidence(dict(reversed(list(self.states.items()))),
                                       dict(reversed(list(self.reports.items()))), self.comments, self.forum)
        self.assertEqual(first["sources"], second["sources"])
        self.assertEqual(first["research"], second["research"])
        self.assertEqual(first["model_input"], second["model_input"])

    def test_duplicate_external_records_merge_roles_without_double_counting(self):
        self.states["media"]["paragraphs"][0]["research"]["search_history"] = copy.deepcopy(
            self.states["query"]["paragraphs"][0]["research"]["search_history"])
        bundle = self.bundle()
        self.assertEqual(len(bundle["sources"]), 1)
        self.assertEqual(next(iter(bundle["sources"].values()))["roles"], ["query", "media"])
        report.validate_report(self.candidate(bundle), bundle)

    def test_budget_preserves_originals_and_discloses_unread_material(self):
        for index in range(3, 103):
            self.comments[f"C{index:03d}"] = {"comment_id": "original-" + str(index),
                "content": "需要核查原始来源的评论正文。" * 8,
                "source_url": "https://example.com/video"}
        before = copy.deepcopy(self.comments)
        bundle = self.bundle(max_content_length=3500)
        text = json.dumps(bundle["model_input"], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        self.assertLessEqual(len(text), 3500)
        self.assertEqual(len(text), bundle["coverage"]["model_input_characters"])
        self.assertEqual(bundle["comments"], before)
        self.assertGreater(bundle["coverage"]["omitted_comment_count"], 0)
        self.assertEqual(bundle["coverage"]["model_comment_count"], len(bundle["included_comment_ids"]))
        supplied_roles = {row["role"] for row in bundle["model_input"]["research"]}
        self.assertEqual(supplied_roles, {"query", "media", "insight", "forum"})
        supplied_sources = {role for row in bundle["model_input"]["sources"] for role in row["roles"]}
        self.assertEqual(supplied_sources, {"query", "media"})
        self.comments["C001"]["content"] = "输入对象后来被修改"
        self.assertEqual(bundle["comments"], before)
        page = report.render_report("测试", self.candidate(bundle), bundle, "run", "live")
        self.assertIn(html.escape(before["C102"]["content"]), page)

    def test_unknown_citations_are_rejected_for_every_kind(self):
        bundle = self.bundle()
        for field in ("source_ids", "comment_ids", "research_ids"):
            with self.subTest(field=field):
                candidate = self.candidate(bundle)
                candidate["sections"][0]["paragraphs"][0][field] = ["unknown"]
                with self.assertRaisesRegex(report.ReportValidationError, "unknown"):
                    report.validate_report(candidate, bundle)

    def test_known_but_unread_comment_citation_is_rejected(self):
        for index in range(3, 30):
            self.comments[f"C{index:03d}"] = {"comment_id": str(index), "content": "未读长评论" * 300,
                                             "source_url": "https://example.com/comment"}
        bundle = self.bundle(max_content_length=3500)
        omitted = next(key for key in bundle["comments"] if key not in bundle["included_comment_ids"])
        candidate = self.candidate(bundle)
        candidate["sections"][4]["paragraphs"][0]["comment_ids"] = [omitted]
        with self.assertRaisesRegex(report.ReportValidationError, "not supplied"):
            report.validate_report(candidate, bundle)

    def test_known_but_unread_source_citation_is_rejected(self):
        for role in ("query", "media"):
            self.states[role]["paragraphs"][0]["research"]["search_history"] += [
                {"url": f"https://example.com/{role}/{index}", "title": "额外来源", "content": "原始材料" * 100}
                for index in range(30)]
        bundle = self.bundle(max_content_length=3500)
        omitted = next(key for key in bundle["sources"] if key not in bundle["included_source_ids"])
        candidate = self.candidate(bundle)
        candidate["sections"][1]["paragraphs"][0]["source_ids"] = [omitted]
        with self.assertRaisesRegex(report.ReportValidationError, "not supplied"):
            report.validate_report(candidate, bundle)

    def test_missing_sections_and_role_coverage_are_rejected(self):
        bundle = self.bundle()
        cases = []
        missing_section = self.candidate(bundle)
        missing_section["sections"].pop()
        cases.append((missing_section, "seven"))
        missing_research = self.candidate(bundle)
        missing_research["sections"][2]["paragraphs"][0]["research_ids"].pop()
        cases.append((missing_research, "Query, Media and Insight"))
        missing_forum = self.candidate(bundle)
        missing_forum["sections"][5]["paragraphs"][0]["research_ids"] = bundle["included_research_ids"][:1]
        cases.append((missing_forum, "Forum"))
        missing_source = self.candidate(bundle)
        for section in missing_source["sections"]:
            for paragraph in section["paragraphs"]:
                paragraph["source_ids"] = bundle["included_source_ids"][:1] if paragraph["source_ids"] else []
        cases.append((missing_source, "Query and Media"))
        for candidate, message in cases:
            with self.subTest(message=message):
                with self.assertRaisesRegex(report.ReportValidationError, message):
                    report.validate_report(candidate, bundle)

    def test_background_and_controversy_cannot_rely_only_on_agent_narrative(self):
        bundle = self.bundle()
        for index in (1, 3):
            with self.subTest(index=index):
                candidate = self.candidate(bundle)
                candidate["sections"][index]["paragraphs"][0]["source_ids"] = []
                with self.assertRaisesRegex(report.ReportValidationError, "external sources"):
                    report.validate_report(candidate, bundle)

    def test_statistics_dates_and_unsupported_claims_are_rejected(self):
        bundle = self.bundle()
        for text in ("覆盖50%用户", "发生于2024年", "九成用户赞同", "有一百条评论", "事件确定属实"):
            with self.subTest(text=text):
                candidate = self.candidate(bundle)
                candidate["sections"][0]["paragraphs"][0]["text"] = text
                with self.assertRaises(report.ReportValidationError):
                    report.validate_report(candidate, bundle)

    def test_real_missing_citation_response_reports_every_affected_paragraph(self):
        # Exact uncited summary from the first archived real-model response.
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][0]["paragraphs"] = [{
            "text": "《黑神话：悟空》自发售以来，凭借其深厚的文化底蕴、出色的美术设计与战斗系统，在全球范围内引发了广泛关注与热烈讨论。舆情整体呈现出对游戏视听表现与文化输出的高度认可，但同时，关于关卡设计缺陷与多平台技术优化问题的争议也贯穿始终。本报告综合多方信源与研究视角，对该游戏的综合体验反馈进行深度剖析。",
            "source_ids": [], "comment_ids": [], "research_ids": []}]
        candidate["sections"][6]["paragraphs"] = [
            {"text": "本报告的结论严格受限于给定的检索材料与历史评论样本。",
             "source_ids": [], "comment_ids": [], "research_ids": []},
            {"text": "此外，部分关于游戏后续更新计划、销量数据以及特定平台性能表现的报道来源于外部媒体或未经官方最终确认的传闻，存在信息滞后或不准确的风险。",
             "source_ids": [], "comment_ids": [], "research_ids": []}]
        before = copy.deepcopy(candidate)
        with self.assertRaises(report.ReportValidationError) as captured:
            report.validate_report(candidate, bundle)
        issues = captured.exception.issues
        self.assertEqual({row["path"] for row in issues if row["code"] == "missing_citations"}, {
            "$.sections[0].paragraphs[0]", "$.sections[6].paragraphs[0]", "$.sections[6].paragraphs[1]"})
        self.assertEqual(captured.exception.as_dict()["issues"], issues)
        for path in ("$.sections[0].paragraphs[0]", "$.sections[6].paragraphs[1]"):
            self.assertIn(path, str(captured.exception))
        self.assertEqual(candidate, before)

    def test_diagnostics_reject_unknown_and_numeric_claims_in_one_correction(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][0]["paragraphs"][0].update({
            "text": "覆盖50%用户", "source_ids": ["unknown-source"]})
        with self.assertRaises(report.ReportValidationError) as captured:
            report.validate_report(candidate, bundle)
        issues = captured.exception.issues
        self.assertIn("numeric_narrative", {row["code"] for row in issues})
        self.assertIn("unknown_citation", {row["code"] for row in issues})
        unknown = next(row for row in issues if row["code"] == "unknown_citation")
        self.assertEqual(unknown["path"], "$.sections[0].paragraphs[0].source_ids[0]")
        self.assertIn("unknown-source", unknown["message"])

    def test_source_metadata_cannot_crowd_out_forum_and_substantive_research(self):
        for role in ("query", "media"):
            self.states[role]["paragraphs"][0]["research"]["search_history"] += [
                {"url": f"https://example.com/{role}/{index}", "title": role + "外部来源",
                 "content": "网页检索材料中的历史表述仍需核验。" * 600}
                for index in range(60)]
        for role in report.RESEARCH_ROLES:
            self.reports[role] = (role + "研究正文：仅归纳历史材料。") * 130 + "末尾指出官方公告缺失。"
        self.forum = "主持讨论：来源存在时点差异，不能据此确认现状。" * 85 + "结尾核查建议：商业数据尚缺官方审计背书。"
        before = copy.deepcopy((self.states, self.reports, self.comments, self.forum))
        bundle = self.bundle(max_content_length=60000)
        supplied = {row["id"]: row for row in bundle["model_input"]["research"]}
        self.assertEqual(supplied[bundle["forum_id"]]["text"], self.forum)
        self.assertTrue(supplied[bundle["forum_id"]]["excerpt_complete"])
        for row in bundle["research"].values():
            if row["kind"] == "report":
                self.assertEqual(supplied[row["id"]]["text"], row["content"])
                self.assertTrue(supplied[row["id"]]["excerpt_complete"])
        self.assertGreater(bundle["coverage"]["omitted_source_count"], 0)
        self.assertLessEqual(bundle["coverage"]["model_input_characters"], 60000)
        self.assertEqual((self.states, self.reports, self.comments, self.forum), before)

    def test_literal_qualitative_classification_phrases_do_not_claim_sample_statistics(self):
        bundle = self.bundle()
        for text in (
                "舆论争议主要集中在技术表现与内容设计两个层面。",
                "样本中的讨论主要集中在机制体验瑕疵与文化认同两个维度。",
                "下文从体验、设计与证据三个方面讨论给定材料。",
                "材料呈现若干关注点，但不推算总体比例。"):
            with self.subTest(text=text):
                candidate = self.candidate(bundle)
                candidate["sections"][3]["paragraphs"][0]["text"] = text
                report.validate_report(candidate, bundle)
                self.assertIn(text, report.render_report("测试", candidate, bundle, "run", "live"))

    def test_classification_exception_cannot_allow_real_numeric_claims(self):
        bundle = self.bundle()
        for text in (
                "两个平台都出现相关讨论。",
                "两个人都表示赞同。",
                "样本包含两个来源。",
                "评论由两个人提供，分三个方面讨论。",
                "两个平台的材料分为两个层面。",
                "从两个层面看，样本覆盖50%用户。",
                "两个层面的评论共有一百条。",
                "从三个方面看，讨论始于2024年。",
                "两个层面的相关金额为两万元。",
                "从三个方面看，涉及两个月的观察。",
                "二十两个层面均获得关注。",
                "二十两个维度均获得关注。",
                "两个维度覆盖一百条评论。",
                "报道附带了一份清单。"):
            with self.subTest(text=text):
                candidate = self.candidate(bundle)
                candidate["sections"][0]["paragraphs"][0]["text"] = text
                with self.assertRaisesRegex(report.ReportValidationError, "numeric"):
                    report.validate_report(candidate, bundle)

    def test_qualitative_condition_and_letter_metaphor_are_not_statistics(self):
        bundle = self.bundle()
        for text in (
                "媒体研究引用的专业实测与资讯站声称之间缺乏同一条件下的独立对比实测。",
                "部分评论将其视作开发团队给中国玩家的一份情书。",
                "评论将其视为给玩家的一份情书，仍应回到原始出处核查。"):
            with self.subTest(text=text):
                candidate = self.candidate(bundle)
                candidate["sections"][3]["paragraphs"][0]["text"] = text
                candidate["sections"][3]["paragraphs"][0]["research_ids"] = candidate["sections"][2]["paragraphs"][0]["research_ids"]
                report.validate_report(candidate, bundle)
                self.assertIn(text, report.render_report("测试", candidate, bundle, "run", "live"))

    def test_qualitative_number_exceptions_preserve_nearby_numeric_guards(self):
        bundle = self.bundle()
        for text in (
                "同一条件下，一百条评论表示支持。",
                "同一条评论出现在两个来源中。",
                "我们收到一份情书。",
                "我们收到十一份情书。",
                "评论将其视作十一份情书。",
                "评论将其视作一百一份情书。",
                "评论将其视作一份情书，覆盖50%用户。",
                "评论将其视作2024年给玩家的一份情书。",
                "两份评论将其视作一份情书。"):
            with self.subTest(text=text):
                candidate = self.candidate(bundle)
                candidate["sections"][3]["paragraphs"][0]["text"] = text
                with self.assertRaisesRegex(report.ReportValidationError, "numeric"):
                    report.validate_report(candidate, bundle)

    def test_qualitative_classification_does_not_relax_citation_boundaries(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][3]["paragraphs"][0].update({
            "text": "舆论争议主要集中在技术表现与内容设计两个层面。",
            "source_ids": ["unknown-source"]})
        with self.assertRaisesRegex(report.ReportValidationError, "unknown"):
            report.validate_report(candidate, bundle)

    def test_direct_local_negations_do_not_assert_verified_facts(self):
        bundle = self.bundle()
        for text in (
                "不能据此认为事实确凿，仍需核查原始材料。",
                "本报告不声称确定属实，相关主张仍须核查。",
                "现有材料不足以证明事件确定属实。",
                "没有证据表明事实确凿，需要回到原始出处核查。",
                "相关说法并非已被证实，研究意见仍须核查。",
                "尚未达到确定属实的程度，仍需补充原始依据。",
                "不能认定事实确凿，也无法证明确定属实。",
                "本报告不宣称全面事实核验已通过。"):
            with self.subTest(text=text):
                candidate = self.candidate(bundle)
                candidate["sections"][0]["paragraphs"][0]["text"] = text
                report.validate_report(candidate, bundle)

    def test_affirmative_claims_remain_rejected_with_unrelated_or_inverted_negations(self):
        bundle = self.bundle()
        for text in (
                "给定材料事实确凿，可以据此作出结论。",
                "相关说法确定属实。",
                "不能忽略来源，但事件事实确凿。",
                "不声称事实确凿，但确定属实。",
                "不是传闻，而是事实确凿。",
                "不是不声称确定属实。",
                "并非无法证明事实确凿。",
                "不能否认事实确凿。",
                "不否认确定属实。",
                "不但事实确凿，还具有完整依据。",
                "没有证据表明来源不可靠，因此事实确凿。",
                "本报告不声称全面事实核验已通过。事件确定属实。"):
            with self.subTest(text=text):
                candidate = self.candidate(bundle)
                candidate["sections"][0]["paragraphs"][0]["text"] = text
                with self.assertRaisesRegex(report.ReportValidationError, "unverified facts"):
                    report.validate_report(candidate, bundle)

    def test_section_title_cannot_assert_verified_facts(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][0]["title"] = "事件确定属实"
        with self.assertRaisesRegex(report.ReportValidationError, "unverified facts"):
            report.validate_report(candidate, bundle)

    def test_negated_wording_does_not_relax_unknown_citation_rejection(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][0]["paragraphs"][0].update({
            "text": "本报告不声称确定属实。", "source_ids": ["unknown-source"]})
        with self.assertRaisesRegex(report.ReportValidationError, "unknown"):
            report.validate_report(candidate, bundle)

    def test_model_cannot_supply_unbound_quotes_counts_or_html_fields(self):
        bundle = self.bundle()
        for key, value in (("quote", "伪造引文"), ("count", 99), ("html", "<script>alert(1)</script>")):
            with self.subTest(key=key):
                candidate = self.candidate(bundle)
                candidate["sections"][0]["paragraphs"][0][key] = value
                with self.assertRaisesRegex(report.ReportValidationError, "paragraph fields"):
                    report.validate_report(candidate, bundle)

    def test_html_scripts_and_unsafe_metadata_are_always_text(self):
        script = '<script>alert("danger")</script>'
        self.comments["C001"]["content"] = script
        self.comments["C001"]["nickname"] = '<img src=x onerror="alert(9)">'
        self.states["query"]["paragraphs"][0]["research"]["search_history"][0]["title"] = script
        self.states["query"]["paragraphs"][0]["research"]["search_history"][0]["content"] = script
        self.forum = script
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][0]["paragraphs"][0]["text"] = '<script>alert("danger")</script>'
        page = report.render_report(script, candidate, bundle, script, "live", {"sampling": script})
        self.assertNotIn("<script", page)
        self.assertNotIn("<img", page)
        self.assertIn(html.escape(script, quote=True), page)
        self.assertIn("Content-Security-Policy", page)
        self.assertIn("base-uri 'none'", page)

    def test_original_numeric_text_is_bound_and_never_rewritten(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        page = report.render_report("测试", candidate, bundle, "run", "live")
        self.assertIn(html.escape(self.comments["C001"]["content"]), page)
        self.assertIn("采集时点赞3", page)
        self.assertIn('data-comment-id="C001"', page)
        self.assertIn("2024年有相关讨论", page)
        self.assertEqual(bundle["model_input"]["comments"][0]["content"], self.comments["C001"]["content"])

    def test_generated_chinese_spacing_is_cleaned_without_changing_originals(self):
        self.comments["C001"]["content"] = "我引用 Query Agent 原文，保留 source ID 的空格。"
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][0]["title"] = "中文 Query Agent 研究"
        candidate["sections"][0]["paragraphs"][0]["text"] = "结合 Query Agent 研究，但仍需核对来源。"
        page = report.render_report("中文 AegisRT 话题", candidate, bundle, "test-run", "live")
        self.assertIn("中文Query Agent研究", page)
        self.assertIn("结合Query Agent研究", page)
        self.assertIn("中文AegisRT话题", page)
        self.assertIn(html.escape(self.comments["C001"]["content"]), page)

    def test_mutated_excerpt_cannot_be_rendered_as_original(self):
        bundle = self.bundle()
        for kind, field in (("comments", "content"), ("sources", "excerpt"), ("research", "text")):
            with self.subTest(kind=kind):
                altered = copy.deepcopy(bundle)
                altered["model_input"][kind][0][field] = "被替换的伪造摘录"
                with self.assertRaisesRegex(report.ReportValidationError, "differs from original"):
                    report.validate_report(self.candidate(altered), altered)

    def test_unusable_external_urls_and_duplicate_comment_ids_are_rejected(self):
        self.states["query"]["paragraphs"][0]["research"]["search_history"][0]["url"] = "javascript:alert(1)"
        with self.assertRaisesRegex(report.ReportValidationError, "no usable"):
            self.bundle()
        self.states["query"]["paragraphs"][0]["research"]["search_history"][0]["url"] = "https://example.com/query"
        self.comments["C002"]["comment_id"] = "original-a"
        with self.assertRaisesRegex(report.ReportValidationError, "duplicate"):
            self.bundle()

    def test_title_only_source_is_explicitly_not_original_fact_evidence(self):
        for role in ("query", "media"):
            self.states[role]["paragraphs"][0]["research"]["search_history"][0].pop("content")
        bundle = self.bundle()
        page = report.render_report("测试", self.candidate(bundle), bundle, "run", "replay")
        self.assertIn("没有保存正文，标题只作来源线索", page)
        self.assertIn("未调用模型", page)
        self.assertIn("离线材料说明", page)
        self.assertNotIn("模型综合归纳：", page)

    def test_budget_too_small_for_real_coverage_fails_instead_of_silent_omission(self):
        with self.assertRaisesRegex(report.ReportValidationError, "budget"):
            self.bundle(max_content_length=1000)
        self.comments["C001"]["content"] = "超长原始评论" * 4000
        self.comments["C002"]["content"] = "另条超长评论" * 4000
        with self.assertRaisesRegex(report.ReportValidationError, "two complete original comments"):
            self.bundle()

    @staticmethod
    def supported_review(candidate):
        return {"checks": [
            {"path": paragraph["path"], "supported": True,
             "reason": "该段明确归因给定材料，所引摘录支持其有限概述。",
             "evidence_ids": (paragraph["source_ids"] + paragraph["comment_ids"] + paragraph["research_ids"])[:1],
             "sentence_checks": [{"index": sentence["index"], "supported": True,
                                  "reason": "所引摘录支持该句对有限材料的归因。",
                                  "evidence_ids": (paragraph["source_ids"] + paragraph["comment_ids"] + paragraph["research_ids"])[:1]}
                                 for sentence in paragraph["sentences"]]}
            for paragraph in report._review_paragraphs(candidate)]}

    def test_evidence_review_input_contains_unique_complete_supplied_pool(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        before = copy.deepcopy((candidate, bundle))
        with patch("socket.socket", side_effect=AssertionError("review attempted network access")):
            review_input = report.build_review_input(candidate, bundle)
        self.assertEqual(len(review_input["paragraphs"]), 7)
        self.assertFalse(review_input["scope"]["original_source_truth_verified"])
        self.assertFalse(review_input["scope"]["cross_paragraph_evidence_allowed"])
        for field, kind in (("source_ids", "sources"), ("comment_ids", "comments"), ("research_ids", "research")):
            expected = {row["id"] for row in bundle["model_input"][kind]}
            selected = review_input["evidence"][kind]
            self.assertEqual({row["id"] for row in selected}, expected)
            self.assertEqual(len(selected), len(expected))
            for row in selected:
                self.assertEqual(row, next(original for original in bundle["model_input"][kind] if original["id"] == row["id"]))
        self.assertEqual((candidate, bundle), before)

    def test_real_wrong_paragraph_evidence_is_an_issue_without_editorial_rewrite(self):
        # Minimal actual small-round failure: Insight says planning is missing,
        # while the Report paragraph claims that planning was confirmed.
        self.reports["insight"] = "样本中完全缺失游戏官方公告、主创团队当事人表述及权威媒体报道。对于DLC排期，本轮材料未补充任何新信源，真实性均未证实，仍无法解答。"
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        insight_id = next(row["id"] for row in bundle["model_input"]["research"] if row["role"] == "insight" and row["kind"] == "report")
        candidate["sections"][2]["paragraphs"].append({
            "text": "分析确认开发团队已选择优先研发新作，相关传闻被官方声明所取代。",
            "source_ids": [], "comment_ids": [], "research_ids": [insight_id]})
        response = self.supported_review(candidate)
        response["checks"][3].update(supported=False, reason="所引Insight摘录明确说官方规划资料缺失，不能支持已确认优先研发新作的结论。")
        response["checks"][3]["sentence_checks"][0].update(
            supported=False, reason=response["checks"][3]["reason"])
        before = copy.deepcopy((response, candidate, bundle))
        validated, issues = report.validate_evidence_review(response, candidate, bundle)
        self.assertEqual(validated, response)
        self.assertEqual(issues, [{"code": "evidence_not_supported", "path": "$.sections[2].paragraphs[1]",
                                  "sentence_index": 0, "sentence_text": candidate["sections"][2]["paragraphs"][1]["text"],
                                  "message": response["checks"][3]["reason"], "evidence_ids": [insight_id]}])
        self.assertEqual((response, candidate, bundle), before)

    def test_review_requires_every_paragraph_once_and_strict_boolean(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        valid = self.supported_review(candidate)
        for mutation in ("missing", "duplicate", "unknown_path", "string_boolean", "missing_reason", "empty_false_reason", "extra_quote"):
            with self.subTest(mutation=mutation):
                response = copy.deepcopy(valid)
                if mutation == "missing":
                    response["checks"].pop()
                elif mutation == "duplicate":
                    response["checks"][-1] = copy.deepcopy(response["checks"][0])
                elif mutation == "unknown_path":
                    response["checks"][0]["path"] = "$.sections[8].paragraphs[0]"
                elif mutation == "string_boolean":
                    response["checks"][0]["supported"] = "true"
                elif mutation == "missing_reason":
                    response["checks"][0].pop("reason")
                elif mutation == "empty_false_reason":
                    response["checks"][0].update(supported=False, reason="  ")
                else:
                    response["checks"][0]["quote"] = "模型伪造原文"
                with self.assertRaises(report.ReportValidationError) as captured:
                    report.validate_evidence_review(response, candidate, bundle)
                self.assertTrue(captured.exception.issues)
        for value in (0, 1, None):
            with self.subTest(non_boolean=value):
                response = copy.deepcopy(valid)
                response["checks"][0]["supported"] = value
                with self.assertRaises(report.ReportValidationError) as captured:
                    report.validate_evidence_review(response, candidate, bundle)
                self.assertIn("invalid_support_boolean", {row["code"] for row in captured.exception.issues})
        response = copy.deepcopy(valid)
        response["quote"] = "不能用模型引文替代原摘录"
        with self.assertRaises(report.ReportValidationError):
            report.validate_evidence_review(response, candidate, bundle)

    def test_review_cannot_cite_unknown_unsupplied_or_other_paragraph_evidence(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        valid = self.supported_review(candidate)
        foreign_source = bundle["included_source_ids"][0]
        for ids in (["unknown"], [], [foreign_source], [valid["checks"][0]["evidence_ids"][0]] * 2):
            with self.subTest(ids=ids):
                response = copy.deepcopy(valid)
                response["checks"][0]["evidence_ids"] = ids
                with self.assertRaises(report.ReportValidationError):
                    report.validate_evidence_review(response, candidate, bundle)
        altered = copy.deepcopy(bundle)
        altered["model_input"]["research"][0]["text"] = "伪造原摘录"
        with self.assertRaises(report.ReportValidationError):
            report.build_review_input(candidate, altered)

        for role in ("query", "media"):
            self.states[role]["paragraphs"][0]["research"]["search_history"] += [
                {"url": f"https://example.com/unread/{role}/{index}", "title": "未供给来源", "content": "未供给原文" * 100}
                for index in range(30)]
        bundle = self.bundle(max_content_length=3500)
        candidate = self.candidate(bundle)
        response = self.supported_review(candidate)
        omitted = next(key for key in bundle["sources"] if key not in bundle["included_source_ids"])
        response["checks"][1]["evidence_ids"] = [omitted]
        with self.assertRaises(report.ReportValidationError) as captured:
            report.validate_evidence_review(response, candidate, bundle)
        self.assertIn("foreign_review_evidence", {row["code"] for row in captured.exception.issues})

    def test_review_does_not_expand_title_only_or_truncated_evidence(self):
        self.states["query"]["paragraphs"][0]["research"]["search_history"][0]["content"] = ""
        self.states["media"]["paragraphs"][0]["research"]["search_history"][0]["content"] = "可见摘录。" + "未提供原文。" * 4000
        bundle = self.bundle(max_content_length=3500)
        candidate = self.candidate(bundle)
        review_input = report.build_review_input(candidate, bundle)
        for row in review_input["evidence"]["sources"]:
            supplied = next(value for value in bundle["model_input"]["sources"] if value["id"] == row["id"])
            self.assertEqual(row["excerpt"], supplied["excerpt"])
        self.assertTrue(any(not row["excerpt"] for row in review_input["evidence"]["sources"]))
        self.assertTrue(any(not row["excerpt_complete"] for row in review_input["evidence"]["sources"]))
        self.assertTrue(review_input["scope"]["source_titles_are_clues_only"])
        self.assertTrue(review_input["scope"]["research_and_forum_are_summaries_or_advice"])

    def test_review_budget_includes_paragraphs_and_metadata_without_truncating_evidence(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        full = report.build_review_input(candidate, bundle)
        characters = len(json.dumps(full, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
        self.assertEqual(report.build_review_input(candidate, bundle, max_content_length=characters), full)
        with self.assertRaises(report.ReportValidationError) as captured:
            report.build_review_input(candidate, bundle, max_content_length=characters - 1)
        self.assertEqual(captured.exception.code, "review_input_budget")
        validated, issues = report.validate_evidence_review(self.supported_review(candidate), candidate, bundle)
        self.assertEqual(len(validated["checks"]), 7)
        self.assertEqual(issues, [])

    def test_review_paths_with_nested_paragraphs_are_exhaustive_and_order_independent(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][0]["paragraphs"].append(copy.deepcopy(candidate["sections"][0]["paragraphs"][0]))
        response = self.supported_review(candidate)
        response["checks"].reverse()
        validated, issues = report.validate_evidence_review(response, candidate, bundle)
        self.assertEqual(validated, response)
        self.assertEqual(len(validated["checks"]), 8)
        self.assertEqual(issues, [])
        response["checks"] = [row for row in response["checks"] if row["path"] != "$.sections[0].paragraphs[1]"]
        with self.assertRaises(report.ReportValidationError) as captured:
            report.validate_evidence_review(response, candidate, bundle)
        self.assertEqual(captured.exception.issues, [{"code": "missing_review_check", "path": "$.sections[0].paragraphs[1]",
                                                    "message": "every report paragraph requires its own complete support check"}])

    def test_sentence_review_input_preserves_exact_punctuation_quotes_and_whitespace(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        text = '所引研究仅描述本样本。 另一句保留范围；English statement. "Quoted sentence!"\n结尾无标点'
        candidate["sections"][0]["paragraphs"][0]["text"] = text
        review_input = report.build_review_input(candidate, bundle)
        sentences = review_input["paragraphs"][0]["sentences"]
        self.assertEqual(sentences, [
            {"index": 0, "text": "所引研究仅描述本样本。 "},
            {"index": 1, "text": "另一句保留范围；"},
            {"index": 2, "text": 'English statement. '},
            {"index": 3, "text": '"Quoted sentence!"\n'},
            {"index": 4, "text": "结尾无标点"},
        ])
        self.assertEqual("".join(row["text"] for row in sentences), text)
        self.assertTrue(review_input["scope"]["sentence_checks_required"])

    def test_legacy_paragraph_only_review_cannot_skip_sentence_checks(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        response = self.supported_review(candidate)
        for row in response["checks"]:
            row.pop("sentence_checks", None)
        with self.assertRaises(report.ReportValidationError) as captured:
            report.validate_evidence_review(response, candidate, bundle)
        self.assertIn("invalid_review_fields", {row["code"] for row in captured.exception.issues})

    def test_review_full_supplied_pool_contains_uncited_material_for_contradiction_only(self):
        self.states["query"]["paragraphs"][0]["research"]["search_history"].append({
            "url": "https://example.com/uncited", "title": "未引用对照材料", "content": "材料已有媒体报道，不等于独立核验。"})
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        review_input = report.build_review_input(candidate, bundle)
        for kind in ("sources", "research", "comments"):
            self.assertEqual(review_input["evidence"][kind], bundle["model_input"][kind])
        self.assertTrue(review_input["scope"]["uncited_evidence_for_contradiction_only"])

    def test_false_sentence_cannot_be_hidden_by_supported_paragraph(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][0]["paragraphs"][0]["text"] = "所引研究仅归纳当前样本。另有具体结论仍须核对。"
        response = self.supported_review(candidate)
        row = response["checks"][0]
        row["sentence_checks"] = [
            {"index": 0, "supported": True, "reason": "该句保留研究的局部样本范围。", "evidence_ids": row["evidence_ids"]},
            {"index": 1, "supported": False, "reason": "本段所引摘录没有讨论该句的具体结论。", "evidence_ids": row["evidence_ids"]},
        ]
        with self.assertRaises(report.ReportValidationError) as captured:
            report.validate_evidence_review(response, candidate, bundle)
        self.assertIn("inconsistent_review_support", {row["code"] for row in captured.exception.issues})

    def test_each_sentence_requires_exact_fields_unique_index_reason_and_strict_boolean(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][0]["paragraphs"][0]["text"] = "所引研究仅归纳当前样本。该范围不能外推。"
        valid = self.supported_review(candidate)
        expected_codes = {"missing": "missing_review_sentence", "duplicate": "duplicate_review_sentence",
                          "unknown": "unknown_review_sentence", "bool_index": "unknown_review_sentence",
                          "empty_reason": "invalid_review_reason", "missing_reason": "invalid_sentence_review_fields",
                          "string_boolean": "invalid_support_boolean", "numeric_boolean": "invalid_support_boolean",
                          "extra_quote": "invalid_sentence_review_fields", "not_array": "invalid_sentence_checks"}
        for mutation, expected_code in expected_codes.items():
            with self.subTest(mutation=mutation):
                response = copy.deepcopy(valid)
                checks = response["checks"][0]["sentence_checks"]
                if mutation == "missing":
                    checks.pop()
                elif mutation == "duplicate":
                    checks[1] = copy.deepcopy(checks[0])
                elif mutation == "unknown":
                    checks[0]["index"] = 7
                elif mutation == "bool_index":
                    checks[0]["index"] = False
                elif mutation == "empty_reason":
                    checks[0]["reason"] = "  "
                elif mutation == "missing_reason":
                    checks[0].pop("reason")
                elif mutation == "string_boolean":
                    checks[0]["supported"] = "true"
                elif mutation == "numeric_boolean":
                    checks[0]["supported"] = 1
                elif mutation == "extra_quote":
                    checks[0]["quote"] = "模型替代原文"
                else:
                    response["checks"][0]["sentence_checks"] = {}
                with self.assertRaises(report.ReportValidationError) as captured:
                    report.validate_evidence_review(response, candidate, bundle)
                self.assertIn(expected_code, {row["code"] for row in captured.exception.issues})

    def test_full_pool_does_not_allow_uncited_ids_to_support_a_sentence(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        response = self.supported_review(candidate)
        allowed = response["checks"][0]["evidence_ids"]
        uncited = bundle["included_source_ids"][0]
        self.assertNotIn(uncited, allowed)
        self.assertIn(uncited, {row["id"] for row in report.build_review_input(candidate, bundle)["evidence"]["sources"]})
        for ids in ([uncited], ["unknown"], [], [allowed[0], allowed[0]]):
            with self.subTest(ids=ids):
                altered = copy.deepcopy(response)
                altered["checks"][0]["sentence_checks"][0]["evidence_ids"] = ids
                with self.assertRaises(report.ReportValidationError):
                    report.validate_evidence_review(altered, candidate, bundle)

    def test_unsupported_sentences_return_exact_indexes_without_rewriting_or_reusing_prior_true(self):
        bundle = self.bundle()
        candidate = self.candidate(bundle)
        candidate["sections"][0]["paragraphs"][0]["text"] = "所引研究仅归纳当前样本。另有具体结论仍须核对。"
        prior = self.supported_review(candidate)
        prior_validated, prior_issues = report.validate_evidence_review(prior, candidate, bundle)
        self.assertEqual(prior_issues, [])
        response = copy.deepcopy(prior_validated)
        row = response["checks"][0]
        row.update(supported=False, reason="逐句检查发现该段具体结论未被本段引用支持。")
        for index, sentence in enumerate(row["sentence_checks"]):
            sentence.update(supported=False, reason="本段摘录不能支持该句对范围的具体概括。")
        before = copy.deepcopy((response, candidate, bundle))
        _, issues = report.validate_evidence_review(response, candidate, bundle)
        self.assertEqual([(issue["path"], issue["sentence_index"]) for issue in issues],
                         [("$.sections[0].paragraphs[0]", 0), ("$.sections[0].paragraphs[0]", 1)])
        self.assertEqual([issue["sentence_text"] for issue in issues],
                         ["所引研究仅归纳当前样本。", "另有具体结论仍须核对。"])
        self.assertEqual((response, candidate, bundle), before)
        row["sentence_checks"].reverse()
        _, reversed_issues = report.validate_evidence_review(response, candidate, bundle)
        self.assertEqual({issue["sentence_index"] for issue in reversed_issues}, {0, 1})

    def test_review_sentences_keep_decimal_domain_and_trailing_space_exact(self):
        # Partitioning is lexical, and does not change decimal/domain tokens.
        text = 'Version 1.5 at example.com remains one sentence. 尾句。  '
        self.assertEqual(report._review_sentences(text), [
            {"index": 0, "text": 'Version 1.5 at example.com remains one sentence. '},
            {"index": 1, "text": '尾句。  '},
        ])


if __name__ == "__main__":
    unittest.main()
