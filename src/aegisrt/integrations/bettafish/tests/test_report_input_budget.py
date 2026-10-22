"""Complete request envelopes must obey the existing content limit offline."""
import importlib.util
import json
import os
from pathlib import Path
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
helper_spec = importlib.util.spec_from_file_location("report_budget_fixture", ROOT / "tests/test_reporting_integration.py")
fixture = importlib.util.module_from_spec(helper_spec)
helper_spec.loader.exec_module(fixture)
adapter_path = Path(os.environ.get("BETTAFISH_REPORT_ADAPTER_BASELINE", str(ROOT / "adapter.py")))
spec = importlib.util.spec_from_file_location("report_input_adapter_under_test", adapter_path)
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)
report_spec = importlib.util.spec_from_file_location("report_envelope_contract_under_test", ROOT / "comprehensive_report.py")
reporting = importlib.util.module_from_spec(report_spec)
report_spec.loader.exec_module(reporting)


class ReportInputBudgetTests(unittest.TestCase):
    def setUp(self):
        self.case = fixture.ReportingIntegrationTests()
        self.case.setUp()
        self.addCleanup(self.case.doCleanups)
        self.network = patch("socket.socket", side_effect=AssertionError("Offline input budget test attempted network access"))
        self.network.start()
        self.addCleanup(self.network.stop)
        self.case.payload["max_content_length"] = 60000
        self.case.payload["max_model_requests"] = 5
        for role in ("query", "media"):
            state_path = Path(self.case.dependencies[role]["files"]["state"]["path"])
            state = adapter.read_json(state_path)
            state["research_state"]["paragraphs"][0]["research"]["search_history"] += [
                {"url": "https://example.org/" + role + "/" + str(index),
                 "title": role + "历史来源", "content": "历史摘录保留来源归因，不能据此推断整体表现。" * 90}
                for index in range(80)]
            adapter.atomic_json(state_path, state)
            self.case.dependencies[role]["files"]["state"] = adapter.file_record(state_path)

    @staticmethod
    def response(value):
        if value.get("task") != "paragraph-evidence-review":
            candidate = fixture.model_report(value)
            for section in candidate["sections"]:
                for paragraph in section["paragraphs"]:
                    paragraph["source_ids"] = paragraph["source_ids"][:12]
            return json.dumps(candidate, ensure_ascii=False)
        checks = []
        for row in value["paragraphs"]:
            ids = row["source_ids"] + row["comment_ids"] + row["research_ids"]
            check = {"path": row["path"], "supported": True, "reason": "Offline supported fixture.", "evidence_ids": ids[:1]}
            if "sentences" in row:
                check["sentence_checks"] = [{"index": sentence["index"], "supported": True,
                    "reason": "Offline supported sentence fixture.", "evidence_ids": ids[:1]}
                    for sentence in row["sentences"]]
            checks.append(check)
        return json.dumps({"checks": checks}, ensure_ascii=False)

    def run_report(self, completion):
        with patch.object(adapter, "complete_text", side_effect=completion):
            return adapter.downstream_role(self.case.args, self.case.request, self.case.payload,
                                           self.case.dependencies, self.case.target, None)

    def test_dense_evidence_reserves_complete_generation_and_review_envelopes(self):
        captured = []
        def completion(_usage, _role, _system, text):
            captured.append(json.loads(text))
            self.assertLessEqual(len(text), self.case.payload["max_content_length"])
            return self.response(captured[-1])
        result = self.run_report(completion)
        self.assertEqual(len(captured), 2)
        audit = adapter.read_json(Path(result["files"]["audit"]["path"]))
        self.assertGreater(audit["coverage"]["omitted_source_count"], 0)
        self.assertEqual(len(audit["sources"]), 162)
        for kind in ("sources", "research", "comments"):
            self.assertEqual(captured[1]["evidence"][kind], captured[0][kind])

    def test_bounded_correction_retains_frozen_pool_within_complete_input_limit(self):
        captured = []
        def completion(_usage, _role, _system, text):
            value = json.loads(text)
            captured.append(value)
            if len(captured) == 1:
                # Large malformed provider response exercises the existing exact
                # retained raw response and explicit bounded correction excerpt.
                return json.dumps({"unexpected": "x" * 23000})
            self.assertLessEqual(len(text), self.case.payload["max_content_length"])
            return self.response(value)
        self.run_report(completion)
        self.assertEqual(len(captured), 3)
        correction = captured[1]["correction"]
        self.assertTrue(correction["previous_response_truncated"])
        self.assertEqual(len(correction["previous_response_excerpt"]), 20000)
        for kind in ("sources", "research", "comments"):
            self.assertEqual(captured[1][kind], captured[0][kind])
            self.assertEqual(captured[2]["evidence"][kind], captured[0][kind])
        raw = (self.case.target / "model-report-response-1.txt").read_text(encoding="utf-8")
        self.assertEqual(json.loads(raw)["unexpected"], "x" * 23000)

    def test_near_limit_review_bounds_feedback_without_changing_full_evidence_or_prose(self):
        captured, lengths, rejected = [], [], []
        limit = self.case.payload['max_content_length']
        self.case.payload['max_model_requests'] = 3

        def completion(_usage, _role, _system, text):
            value = json.loads(text)
            captured.append(value)
            lengths.append(len(text))
            self.assertLessEqual(len(text), limit)
            if value.get('task') != 'paragraph-evidence-review':
                candidate = json.loads(self.response(value))
                bundle = adapter.read_json(self.case.target / 'report-evidence.json')
                # Grow legal qualitative prose until the complete first review
                # leaves only a small margin for correction feedback. Each
                # appended character occurs in both text and its exact sentence.
                def envelope_length():
                    return len(adapter.canonical({'task': 'paragraph-evidence-review',
                        'topic': self.case.payload['topic'],
                        **reporting.build_review_input(candidate, bundle, limit,
                                                       sample_scope=value['sample_scope'])}))
                for section in candidate['sections']:
                    for paragraph in section['paragraphs']:
                        available = min(1800 - len(paragraph['text']), (59000 - envelope_length()) // 2)
                        paragraph['text'] = '材料保留来源范围' * (max(0, available) // 8) + paragraph['text']
                        remainder = (59000 - envelope_length()) // 2
                        paragraph['text'] = '材' * max(0, min(1800 - len(paragraph['text']), remainder, 7)) + paragraph['text']
                self.assertGreater(envelope_length(), 58980)
                self.assertLessEqual(envelope_length(), 59000)
                reporting.validate_report(candidate, bundle)
                return json.dumps(candidate, ensure_ascii=False)
            response = json.loads(self.response(value))
            if len(captured) == 2:
                for row in response['checks']:
                    reason = '本段未引用（' + row['evidence_ids'][0] + '），需要重新核对实际引用。'
                    row['supported'] = False
                    row['reason'] = reason
                    for sentence in row['sentence_checks']:
                        sentence['supported'] = False
                        sentence['reason'] = reason
                rejected.append(response)
            return json.dumps(response, ensure_ascii=False)

        result = self.run_report(completion)
        self.assertEqual(len(captured), 3)
        self.assertEqual([value.get('task') for value in captured],
                         [None, 'paragraph-evidence-review', 'paragraph-evidence-review'])
        self.assertGreater(lengths[1], 58980)
        self.assertLessEqual(max(lengths), limit)
        for key in ('evidence', 'paragraphs', 'sample_scope', 'schema', 'scope'):
            self.assertEqual(captured[1][key], captured[2][key], key)
        self.assertEqual(len(captured[2]['paragraphs']), 7)
        self.assertIn('format_correction', captured[2])
        archived_review = adapter.decode_model_json(
            (self.case.target / 'model-report-review-response-2.txt').read_text(encoding='utf-8'))
        self.assertEqual(archived_review, rejected[0])
        attempts = adapter.read_json(self.case.target / 'report-validation-attempts.json')
        self.assertEqual([row['status'] for row in attempts],
                         ['candidate_validated', 'review_rejected', 'passed'])
        issues = attempts[1]['issues']
        self.assertEqual(len(issues), 14)
        self.assertTrue(all(row['code'] == 'contradictory_missing_citation' for row in issues))
        unbounded_feedback = {'validation_error': attempts[1]['reason'], 'issues': issues,
                              'instruction': captured[2]['format_correction']['instruction']}
        self.assertGreater(lengths[1] + len(adapter.canonical(unbounded_feedback)), limit)
        audit = adapter.read_json(Path(result['files']['audit']['path']))
        self.assertEqual(audit['validation_attempts'][1]['issues'], issues)
        self.assertTrue((self.case.target / 'report.html').is_file())

    def test_full_core_reports_are_protected_when_they_fit_reserved_pool(self):
        spec = importlib.util.spec_from_file_location("reserved_core_report_under_test",
                                                     adapter_path.with_name("comprehensive_report.py"))
        reporting = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(reporting)
        states = {role: adapter.read_json(Path(self.case.dependencies[role]["files"]["state"]["path"]))["research_state"]
                  for role in adapter.RESEARCH_ROLES}
        reports = {role: "完整研究内容须保留给定材料的范围。" * 380 for role in adapter.RESEARCH_ROLES}
        comments = adapter.normalize_comments(adapter.read_json(Path(self.case.dependencies["insight"]["files"]["comments"]["path"])))
        forum = "主持意见保留核查建议及研究范围。" * 260
        bundle = reporting.build_evidence(states, reports, comments, forum, 36000)
        core = {row["role"]: row for row in bundle["model_input"]["research"]
                if row["kind"] == "report" or row["role"] == "forum"}
        for role, original in {**reports, "forum": forum}.items():
            self.assertTrue(core[role]["excerpt_complete"], role)
            self.assertEqual(core[role]["text"], original)
        self.assertLessEqual(bundle["coverage"]["model_input_characters"], 36000)

    def test_core_reports_that_cannot_fit_stop_before_any_model_request(self):
        for role in adapter.RESEARCH_ROLES:
            path = Path(self.case.dependencies[role]["files"]["report"]["path"])
            path.write_text("完整研究内容须保留给定材料的范围。" * 1500, encoding="utf-8")
            self.case.dependencies[role]["files"]["report"] = adapter.file_record(path)
        with patch.object(adapter, "complete_text", side_effect=AssertionError("Unusable core input must not consume requests")) as provider:
            with self.assertRaisesRegex(adapter.AdapterError, "complete core research"):
                adapter.downstream_role(self.case.args, self.case.request, self.case.payload,
                                       self.case.dependencies, self.case.target, None)
        provider.assert_not_called()
        diagnostic = adapter.read_json(self.case.target / "report-input-budget-error.json")
        self.assertIs(diagnostic["provider_request_sent"], False)
        self.assertTrue(diagnostic["incomplete_core_research_ids"])
        self.assertFalse((self.case.target / "report.html").exists())


if __name__ == "__main__":
    unittest.main(verbosity=2)
