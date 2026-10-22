"""Offline regressions for the actual twelfth-round review contradictions."""
import copy
import json
from pathlib import Path
import unittest
from unittest.mock import patch
import test_comprehensive_report as fixture
import test_reporting_integration as integration

report = fixture.report
adapter = integration.adapter


class ReviewContractTests(unittest.TestCase):
    def setUp(self):
        helper = fixture.ComprehensiveReportTests()
        helper.setUp()
        self.bundle = helper.bundle()
        self.candidate = helper.candidate(self.bundle)

    def review(self):
        return integration.model_review(report.build_review_input(self.candidate, self.bundle))

    def test_original_false_missing_citation_is_rejected_as_invalid_review(self):
        root = Path(__file__).resolve().parents[3]
        wf = 'bf-auto-small-f89c61f9d2d0480a8c'
        folder = root / 'evidence/bettafish-automatic-validation-20261003/real-runs' / wf / 'exported/run' / (wf+'-report')
        read = lambda name: json.loads((folder/name).read_text(encoding='utf-8'))
        candidate = adapter.decode_model_json((folder/'model-report-response-4.txt').read_text(encoding='utf-8'))
        with self.assertRaises(report.ReportValidationError) as error:
            report.validate_evidence_review(read('report-evidence-review-5.json'), candidate, read('report-evidence.json'))
        self.assertIn('contradictory_missing_citation', {row['code'] for row in error.exception.issues})

    def test_scope_and_citation_index_are_supplied_without_shortening(self):
        scope = {'sample_source':'file', 'comment_date_min':'2024-08-16', 'comment_date_max':'2024-08-17',
                 'historical_relative_to_collection':True, 'note':'A historical nonrepresentative sample.'}
        supplied = report.build_review_input(self.candidate, self.bundle, sample_scope=scope)
        self.assertEqual(supplied['sample_scope'], scope)
        self.assertTrue(supplied['scope']['sample_scope_for_sampling_limits_only'])
        for row in supplied['paragraphs']:
            self.assertEqual([item['id'] for item in row['citation_index']], row['source_ids']+row['comment_ids']+row['research_ids'])
        size = len(adapter.canonical(supplied))
        with self.assertRaises(report.ReportValidationError):
            report.build_review_input(self.candidate, self.bundle, size-1, sample_scope=scope)
        scope['note'] = 'changed'
        self.assertNotEqual(supplied['sample_scope']['note'], scope['note'])

    def test_actual_missing_citation_remains_unsupported(self):
        review = self.review()
        ids = set(sum((self.candidate['sections'][0]['paragraphs'][0][key] for key in ('source_ids','research_ids','comment_ids')), []))
        missing = next(row['id'] for row in self.bundle['model_input']['research'] if row['id'] not in ids)
        row = review['checks'][0]
        row['supported'] = row['sentence_checks'][0]['supported'] = False
        row['reason'] = row['sentence_checks'][0]['reason'] = '本段未引用研究报告（'+missing+'），缺乏支持。'
        _, issues = report.validate_evidence_review(review, self.candidate, self.bundle)
        self.assertTrue(issues)
        self.assertTrue(all(issue['code']=='evidence_not_supported' for issue in issues))

    def test_present_citation_with_insufficient_support_is_not_misclassified(self):
        for reason in ('该段已引用{ID}，但材料不能支持这个结论。', '本段并非未引用{ID}，而是摘录未覆盖该事实。'):
            review = self.review(); row = review['checks'][0]
            reason = reason.format(ID=row['evidence_ids'][0])
            row['supported'] = row['sentence_checks'][0]['supported'] = False
            row['reason'] = row['sentence_checks'][0]['reason'] = reason
            _, issues = report.validate_evidence_review(review,self.candidate,self.bundle)
            self.assertTrue(issues)

    def test_literal_guard_keeps_separate_missing_and_present_ids_distinct(self):
        self.assertEqual(report.contradictory_missing_citations('本段漏引S2，但已引用S1不足以支持。', {'S1'}), [])
        self.assertEqual(report.contradictory_missing_citations('本段未引用其他资料，但S1确已列出。', {'S1'}), [])
        self.assertEqual(report.contradictory_missing_citations('本段未引用S10。', {'S1'}), [])
        self.assertEqual(report.contradictory_missing_citations('本段并未引用研究报告（R1）。', {'R1'}), ['R1'])
        self.assertEqual(report.contradictory_missing_citations('Missing citation: R1.', {'R1'}), ['R1'])
        self.assertEqual(report.contradictory_missing_citations('Missing citation: R1; recheck.', {'R1'}), ['R1'])

    def test_content_or_excluded_scope_is_not_a_missing_identity_claim(self):
        reasons = (
            '本段未引用R1以外的材料，R1本身已经列出。',
            '本段未引用（R1）之外的材料，无法支持扩大后的结论。',
            '本段未引用R1中的具体建议，现有内容不足以支持。',
            'Missing citation for R1\'s specific recommendation; the cited summary does not support it.',
            '本段不是说未引用R1，而是该摘录不能支持所写的细节。',
        )
        for reason in reasons:
            with self.subTest(reason=reason):
                self.assertEqual(report.contradictory_missing_citations(reason, {'R1'}), [])

    def test_legitimate_content_rejection_keeps_bounded_correction_path(self):
        review = self.review()
        row = review['checks'][0]
        identity = row['evidence_ids'][0]
        row['supported'] = row['sentence_checks'][0]['supported'] = False
        row['reason'] = row['sentence_checks'][0]['reason'] = (
            '本段未引用' + identity + '中的具体建议，现有引用不足以支持所写内容。')
        _, issues = report.validate_evidence_review(review, self.candidate, self.bundle)
        self.assertEqual(len(issues), 1)
        self.assertEqual(issues[0]['code'], 'evidence_not_supported')
        self.assertEqual(issues[0]['path'], '$.sections[0].paragraphs[0]')

    def test_missing_identity_list_checks_every_explicit_id(self):
        self.assertEqual(report.contradictory_missing_citations(
            '本段未引用R2、R1，研究范围不足。', {'R1'}), ['R1'])

    def test_content_rejection_is_corrected_then_entire_report_is_reviewed(self):
        f = integration.ReportingIntegrationTests()
        f.setUp()
        self.addCleanup(f.doCleanups)
        f.payload['max_model_requests'] = 4
        calls = []
        target = '$.sections[0].paragraphs[0]'
        def completion(_usage, _role, _system, text):
            supplied = json.loads(text)
            calls.append(supplied)
            if supplied.get('task') == 'paragraph-evidence-review':
                review = integration.model_review(supplied)
                if len(calls) == 2:
                    row = review['checks'][0]
                    row['supported'] = row['sentence_checks'][0]['supported'] = False
                    row['reason'] = row['sentence_checks'][0]['reason'] = (
                        '本段未引用' + row['evidence_ids'][0] + '中的具体建议，现有引用不足以支持该表述。')
                return json.dumps(review, ensure_ascii=False)
            candidate = integration.model_report(supplied)
            if len(calls) == 3:
                candidate['sections'][0]['paragraphs'][0]['text'] = '所引材料中的观点仍需要核查。'
            return json.dumps(candidate, ensure_ascii=False)
        with patch('socket.socket', side_effect=AssertionError('offline test attempted network')), \
             patch.object(adapter, 'complete_text', side_effect=completion):
            result = adapter.downstream_role(f.args, f.request, f.payload, f.dependencies, f.target, None)
        self.assertEqual([row.get('task') for row in calls],
                         [None, 'paragraph-evidence-review', None, 'paragraph-evidence-review'])
        self.assertEqual(calls[2]['correction']['editable_paths'], [target])
        self.assertEqual(len(calls[3]['paragraphs']), 7)
        self.assertEqual(calls[3]['paragraphs'][0]['text'], '所引材料中的观点仍需要核查。')
        audit = adapter.read_json(Path(result['files']['audit']['path']))
        self.assertEqual([row['status'] for row in audit['validation_attempts']],
                         ['candidate_validated', 'evidence_rejected', 'candidate_validated', 'passed'])

    def test_continuous_contradictory_reviews_exhaust_budget_without_publication(self):
        f = integration.ReportingIntegrationTests(); f.setUp(); self.addCleanup(f.doCleanups)
        f.payload['max_model_requests'] = 3
        calls=[]
        def completion(_usage,_role,_system,text):
            supplied=json.loads(text);calls.append(supplied)
            if supplied.get('task')!='paragraph-evidence-review': return json.dumps(integration.model_report(supplied),ensure_ascii=False)
            review=integration.model_review(supplied);row=review['checks'][0]
            row['supported']=row['sentence_checks'][0]['supported']=False
            row['reason']=row['sentence_checks'][0]['reason']='本段并未引用（'+row['evidence_ids'][0]+'）。'
            return json.dumps(review,ensure_ascii=False)
        with patch('socket.socket',side_effect=AssertionError('offline test attempted network')), patch.object(adapter,'complete_text',side_effect=completion):
            with self.assertRaisesRegex(adapter.AdapterError,'within its request budget'):
                adapter.downstream_role(f.args,f.request,f.payload,f.dependencies,f.target,None)
        self.assertEqual(len(calls),3)
        self.assertFalse((f.target/'report.html').exists())
        self.assertFalse((f.target/'report-evidence-review.json').exists())

    def test_contradiction_requests_review_again_without_rewriting_report(self):
        f = integration.ReportingIntegrationTests(); f.setUp(); self.addCleanup(f.doCleanups)
        f.payload['max_model_requests'] = 3
        scope = {'sample_source':'file','historical_relative_to_collection':True,'comment_date_min':'2024-08-16'}
        calls=[]
        def completion(_usage,_role,_system,text):
            supplied=json.loads(text);calls.append(supplied)
            if supplied.get('task')!='paragraph-evidence-review': return json.dumps(integration.model_report(supplied),ensure_ascii=False)
            review=integration.model_review(supplied)
            if len(calls)==2:
                row=review['checks'][0]; row['supported']=row['sentence_checks'][0]['supported']=False
                row['reason']=row['sentence_checks'][0]['reason']='本段并未引用来源（'+row['evidence_ids'][0]+'），缺少支持。'
            return json.dumps(review,ensure_ascii=False)
        with patch('socket.socket',side_effect=AssertionError('offline test attempted network')), \
             patch.object(adapter,'file_sample_report_scope',return_value=scope), \
             patch.object(adapter,'complete_text',side_effect=completion):
            adapter.downstream_role(f.args,f.request,f.payload,f.dependencies,f.target,None)
        self.assertEqual([row.get('task') for row in calls],[None,'paragraph-evidence-review','paragraph-evidence-review'])
        self.assertEqual(calls[1]['sample_scope'],calls[0]['sample_scope'])
        self.assertEqual(calls[1]['paragraphs'],calls[2]['paragraphs'])
        self.assertEqual(calls[2]['format_correction']['issues'][0]['code'],'contradictory_missing_citation')


if __name__=='__main__': unittest.main()
