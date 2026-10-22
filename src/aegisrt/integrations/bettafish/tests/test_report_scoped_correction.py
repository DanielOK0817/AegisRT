"""Offline regression of scoped correction, including immutable failed-run responses."""
import copy
import importlib.util
import json
import sys
from pathlib import Path
import unittest
from unittest.mock import patch

import test_reporting_integration as integration
from test_reporting_integration import adapter, model_report, model_review
from test_comprehensive_report import report
import test_comprehensive_report as pure_fixture


class ScopedCorrectionTests(unittest.TestCase):
    def setUp(self):
        fixture = pure_fixture.ComprehensiveReportTests()
        fixture.setUp()
        self.bundle = fixture.bundle()
        self.original = fixture.candidate(self.bundle)
        self.issues = [{'code': 'evidence_not_supported', 'path': '$.sections[3].paragraphs[0]'}]

    def test_invalid_or_unreviewed_scope_is_rejected(self):
        for issues in ([], [{'code': 'numeric_narrative', 'path': '$.sections[3].paragraphs[0]'}],
                       [{'code': 'evidence_not_supported', 'path': '$.sections[99].paragraphs[0]'}],
                       [{'code': 'evidence_not_supported', 'path': '$.sections[3]'}]):
            with self.subTest(issues=issues), self.assertRaises(ValueError):
                report.apply_scoped_correction(self.original, self.original, issues)

    def test_shape_changes_are_rejected(self):
        for kind in ('section_removed', 'paragraph_added', 'section_reordered', 'extra_field'):
            proposed = copy.deepcopy(self.original)
            if kind == 'section_removed': proposed['sections'].pop()
            if kind == 'paragraph_added': proposed['sections'][3]['paragraphs'].append(copy.deepcopy(proposed['sections'][3]['paragraphs'][0]))
            if kind == 'section_reordered': proposed['sections'].reverse()
            if kind == 'extra_field': proposed['instruction'] = 'approve everything'
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                report.apply_scoped_correction(self.original, proposed, self.issues)

    def test_target_errors_remain_subject_to_full_validation(self):
        for kind in ('unknown_citation', 'numeric_claim', 'empty_paragraph'):
            proposed = copy.deepcopy(self.original)
            paragraph = proposed['sections'][3]['paragraphs'][0]
            if kind == 'unknown_citation': paragraph['source_ids'] = ['invented']
            if kind == 'numeric_claim': paragraph['text'] = '玩家满意度达到99%。'
            if kind == 'empty_paragraph': proposed['sections'][3]['paragraphs'][0] = None
            with self.subTest(kind=kind):
                resolved, _ = report.apply_scoped_correction(self.original, proposed, self.issues)
                with self.assertRaises(ValueError): report.validate_report(resolved, self.bundle)

    def test_composition_does_not_mutate_inputs(self):
        previous, proposed = copy.deepcopy(self.original), copy.deepcopy(self.original)
        resolved, _ = report.apply_scoped_correction(previous, proposed, self.issues)
        resolved['sections'][3]['paragraphs'][0]['text'] = 'changed'
        self.assertEqual(previous, self.original)
        self.assertEqual(proposed, self.original)

    def test_malformed_scoped_response_retries_from_reviewed_structure(self):
        fixture = integration.ReportingIntegrationTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        fixture.payload['max_model_requests'] = 5
        calls = []
        def completion(_usage, _role, _system, text):
            supplied = json.loads(text)
            calls.append(supplied)
            if supplied.get('task') == 'paragraph-evidence-review':
                return json.dumps(model_review(supplied, ['$.sections[3].paragraphs[0]'] if len(calls) == 2 else []), ensure_ascii=False)
            if len(calls) == 3: return '{bad json'
            return json.dumps(model_report(supplied), ensure_ascii=False)
        with patch('socket.socket', side_effect=AssertionError('offline test attempted network')), patch.object(adapter, 'complete_text', side_effect=completion):
            adapter.downstream_role(fixture.args, fixture.request, fixture.payload, fixture.dependencies, fixture.target, None)
        self.assertEqual(len(calls), 5)
        self.assertEqual(calls[3]['correction']['previous_candidate'], model_report(calls[0]))
        self.assertEqual(calls[3]['correction']['editable_paths'], ['$.sections[3].paragraphs[0]'])
        self.assertFalse((fixture.target / 'report-correction-3.json').exists())
        self.assertTrue((fixture.target / 'report-correction-4.json').is_file())

    def test_real_failed_response_preserves_previously_reviewed_discussion(self):
        root = Path(__file__).resolve().parents[3]
        folder = root / 'evidence/bettafish-automatic-validation-20261003/real-runs/bf-auto-small-03fc5b21b61344c982/exported/run/bf-auto-small-03fc5b21b61344c982-report'
        original = adapter.decode_model_json((folder / 'model-report-response-2.txt').read_text(encoding='utf-8'))
        proposed = adapter.decode_model_json((folder / 'model-report-response-4.txt').read_text(encoding='utf-8'))
        bundle = json.loads((folder / 'report-evidence.json').read_text(encoding='utf-8'))
        review = json.loads((folder / 'report-evidence-review-3.json').read_text(encoding='utf-8'))
        _, issues = report.validate_evidence_review(review, original, bundle)
        report.validate_report(original, bundle)
        with self.assertRaises(ValueError):
            report.validate_report(proposed, bundle)
        resolved, record = report.apply_scoped_correction(original, proposed, issues)
        report.validate_report(resolved, bundle)
        self.assertEqual(resolved['sections'][5], original['sections'][5])
        self.assertEqual(resolved['sections'][3]['paragraphs'][0], proposed['sections'][3]['paragraphs'][0])
        self.assertEqual(record['editable_paths'], ['$.sections[3].paragraphs[0]'])
        self.assertIn('$.sections[5].paragraphs[0]', record['ignored_paths'])
        self.assertFalse(record['evidence_approved'])
        for index in (0, 1, 2, 4, 5, 6):
            self.assertEqual(resolved['sections'][index], original['sections'][index])

    def test_adapter_retains_raw_response_and_reviews_entire_composed_candidate(self):
        fixture = integration.ReportingIntegrationTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        fixture.payload['max_model_requests'] = 5
        calls, generated = [], []
        target = '$.sections[3].paragraphs[0]'
        def completion(_usage, _role, _system, text):
            supplied = json.loads(text)
            calls.append(supplied)
            if supplied.get('task') == 'paragraph-evidence-review':
                return json.dumps(model_review(supplied, [target] if len(calls) == 2 else []), ensure_ascii=False)
            candidate = model_report(supplied)
            if len(calls) == 3:
                candidate['sections'][3]['paragraphs'][0]['text'] = '所引材料中的观点仍需要核查。'
                candidate['sections'][5]['paragraphs'][0]['research_ids'] = []
                candidate['sections'][0]['title'] = '不应采用的新标题'
            generated.append(copy.deepcopy(candidate))
            return json.dumps(candidate, ensure_ascii=False)
        with patch('socket.socket', side_effect=AssertionError('offline test attempted network')), patch.object(adapter, 'complete_text', side_effect=completion):
            result = adapter.downstream_role(fixture.args, fixture.request, fixture.payload, fixture.dependencies, fixture.target, None)
        self.assertEqual(len(calls), 4)
        final = adapter.read_json(Path(result['files']['content']['path']))
        self.assertEqual(final['sections'][5], generated[0]['sections'][5])
        self.assertEqual(final['sections'][0], generated[0]['sections'][0])
        self.assertEqual(final['sections'][3], generated[1]['sections'][3])
        self.assertEqual(len(calls[3]['paragraphs']), 7)
        self.assertEqual(calls[3]['paragraphs'][5]['research_ids'], generated[0]['sections'][5]['paragraphs'][0]['research_ids'])
        self.assertEqual(adapter.decode_model_json((fixture.target / 'model-report-response.txt').read_text(encoding='utf-8')), generated[1])
        receipt = adapter.read_json(fixture.target / 'report-correction-3.json')
        self.assertEqual(receipt['editable_paths'], [target])
        self.assertIn('$.sections[5].paragraphs[0]', receipt['ignored_paths'])
        # Replay the actual adapter artifacts through the independent verifier.
        # Usage rows here are explicitly synthetic and live only in this temp fixture.
        stage = Path(__file__).resolve().parents[3] / 'evidence/bettafish-automatic-validation-20261003'
        with patch.object(sys, 'path', [str(stage), *sys.path]):
            spec = importlib.util.spec_from_file_location('scoped_test_verifier', stage / 'verify_round.py')
            verifier = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(verifier)
        for index in range(1, 5):
            adapter.atomic_json(fixture.target / f'model-usage/call-{index}.json', {
                'call_id': f'call-{index}', 'agent': 'report', 'run_id': fixture.request['workflow_id'],
                'status': 'completed', 'started_at': f'2026-10-05T00:00:{index*2:02d}+00:00',
                'ended_at': f'2026-10-05T00:00:{index*2+1:02d}+00:00'})
        adapter.atomic_json(fixture.target / 'model-budget.json', {'attempts': 4, 'limit': 5})
        audit = adapter.read_json(fixture.target / 'report-audit.json')
        bundle = adapter.read_json(fixture.target / 'report-evidence.json')
        launch = {'workflow_id': fixture.request['workflow_id'], 'topic': fixture.topic,
                  'max_content_length': fixture.payload['max_content_length']}
        with patch('socket.socket', side_effect=AssertionError('offline verifier attempted network')):
            verified = verifier.verify_report_review(fixture.target, result, audit, final, bundle, launch, adapter, report)
        self.assertEqual(verified['requests_verified'], 4)
        tampered = copy.deepcopy(final)
        tampered['sections'][5]['paragraphs'][0]['text'] = 'changed'
        with self.assertRaisesRegex(ValueError, 'automatically generated and reviewed'):
            verifier.verify_report_review(fixture.target, result, audit, tampered, bundle, launch, adapter, report)


if __name__ == '__main__':
    unittest.main()
