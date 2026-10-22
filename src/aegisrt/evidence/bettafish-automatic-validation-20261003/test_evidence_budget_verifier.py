"""Offline schema-bound rebuild guards; no real case or provider is modified."""
import types
import unittest
from pathlib import Path
from unittest.mock import Mock, patch
import workflow_common as common
import verify_round


SOURCE = Path(__file__).resolve().parents[2] / 'integrations/bettafish'
fixture = common.module('offline_evidence_budget_fixture', SOURCE / 'tests/test_comprehensive_report.py')
reporting = fixture.report


class EvidenceBudgetVerifierTests(unittest.TestCase):
    def setUp(self):
        self.source = fixture.ComprehensiveReportTests()
        self.source.setUp()
        for role, state in self.source.states.items():
            state['paragraphs'][0]['research']['search_history'][0]['content'] = (
                role + '原始来源摘录保留意见，完整原文不可扩大供给。') * 4000

    def rebuild(self, module=reporting, limit=60000):
        return verify_round.rebuild_report_evidence(module, self.source.states, self.source.reports,
            self.source.comments, self.source.forum, limit)

    def test_current_v3_rebuild_matches_the_actual_reserved_pool_instead_of_full_input_limit(self):
        self.assertEqual(reporting.REVIEW_INPUT_SCHEMA, 'bettafish.evidence-review-input/v3')
        reserved = reporting.model_evidence_budget(60000)
        self.assertEqual(reserved, 36000)
        expected = self.source.bundle(max_content_length=reserved)
        with patch('socket.socket', side_effect=AssertionError('offline rebuild attempted network')):
            actual = self.rebuild()
        self.assertEqual(actual['coverage']['max_content_length'], reserved)
        self.assertLessEqual(actual['coverage']['model_input_characters'], reserved)
        self.assertEqual(actual, expected)
        for role in reporting.RESEARCH_ROLES:
            supplied = [row for row in actual['model_input']['research']
                        if row['role'] == role and row['kind'] == 'report']
            self.assertEqual(len(supplied), 1)
            self.assertTrue(supplied[0]['excerpt_complete'])

    def test_v1_actual_frozen_module_keeps_its_original_full_limit(self):
        source = Path(__file__).resolve().parent / 'revisions/code-f4904588121bb841/integration/comprehensive_report.py'
        self.assertEqual(common.sha(source), 'd6a205c8f41a9048dcb4aa9338e5d628c2952ba380a76ca57259ed868b449b6c')
        old = common.module('offline_frozen_v1_evidence_budget', source)
        self.assertEqual(old.REVIEW_INPUT_SCHEMA, 'bettafish.evidence-review-input/v1')
        self.assertFalse(hasattr(old, 'model_evidence_budget'))
        actual = self.rebuild(old)
        expected = old.build_evidence(self.source.states, self.source.reports, self.source.comments,
                                      self.source.forum, 60000)
        self.assertEqual(actual, expected)
        self.assertEqual(actual['coverage']['max_content_length'], 60000)

    def test_v2_missing_budget_helper_never_falls_back_to_v1(self):
        build = Mock(return_value={'unexpected_full_pool': True})
        missing = types.SimpleNamespace(REVIEW_INPUT_SCHEMA='bettafish.evidence-review-input/v2', build_evidence=build)
        with self.assertRaisesRegex(ValueError, 'model_evidence_budget'):
            self.rebuild(missing)
        build.assert_not_called()

    def test_v2_noncallable_budget_helper_is_rejected_before_rebuild(self):
        build = Mock()
        broken = types.SimpleNamespace(REVIEW_INPUT_SCHEMA='bettafish.evidence-review-input/v2',
                                        model_evidence_budget=None, build_evidence=build)
        with self.assertRaisesRegex(ValueError, 'model_evidence_budget'):
            self.rebuild(broken)
        build.assert_not_called()

    def test_unknown_or_missing_review_schema_is_not_legacy_v1(self):
        for schema in (None, 'bettafish.evidence-review-input/v4'):
            build = Mock()
            unknown = types.SimpleNamespace(REVIEW_INPUT_SCHEMA=schema, build_evidence=build)
            with self.subTest(schema=schema), self.assertRaisesRegex(ValueError, 'schema'):
                self.rebuild(unknown)
            build.assert_not_called()

    def test_v3_rebuild_requires_the_frozen_callable_budget_helper(self):
        for helper in (None, 7):
            with self.subTest(helper=helper):
                build = Mock()
                missing = types.SimpleNamespace(REVIEW_INPUT_SCHEMA='bettafish.evidence-review-input/v3',
                    model_evidence_budget=helper, build_evidence=build)
                with self.assertRaisesRegex(ValueError, 'model_evidence_budget'):
                    self.rebuild(missing)
                build.assert_not_called()

    def test_v2_calls_the_frozen_helper_once_and_passes_its_exact_budget(self):
        budget = Mock(return_value=1500)
        build = Mock(return_value={'pool': 'bounded offline marker'})
        frozen = types.SimpleNamespace(REVIEW_INPUT_SCHEMA='bettafish.evidence-review-input/v2',
                                        model_evidence_budget=budget, build_evidence=build)
        self.assertEqual(self.rebuild(frozen, 3000), {'pool': 'bounded offline marker'})
        budget.assert_called_once_with(3000)
        self.assertEqual(build.call_args.args[-1], 1500)

    def test_v2_budget_helper_validation_error_is_preserved(self):
        budget = Mock(side_effect=ValueError('complete report input budget must be at least2000'))
        build = Mock()
        frozen = types.SimpleNamespace(REVIEW_INPUT_SCHEMA='bettafish.evidence-review-input/v2',
                                        model_evidence_budget=budget, build_evidence=build)
        with self.assertRaisesRegex(ValueError, 'complete report input budget'):
            self.rebuild(frozen, 1999)
        build.assert_not_called()


if __name__ == '__main__':
    unittest.main(verbosity=2)
