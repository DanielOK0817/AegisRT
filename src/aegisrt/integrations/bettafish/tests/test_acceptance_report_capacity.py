"""Independent acceptance distinguishes rejected SDK output from network errors."""
import copy
import importlib.util
from pathlib import Path
import sys
import unittest

STAGE = Path(__file__).resolve().parents[3] / 'evidence/bettafish-automatic-validation-20261003'
sys.path.insert(0, str(STAGE))
spec = importlib.util.spec_from_file_location('report_capacity_verifier', STAGE / 'verify_round.py')
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


class ReportCapacityAcceptanceTests(unittest.TestCase):
    def test_truncated_sdk_completion_requires_matching_rejected_physical_attempt(self):
        attempt = {'attempt': 2, 'status': 'provider_error', 'error_type': 'ModelOutputLimitExceeded', 'retryable': True}
        call = {'call_id': 'actual', 'status': 'completed'}
        row = {'attempt': 2, 'usage_call_id': 'actual', 'agent': 'report', 'status': 'error',
               'failure_stage': 'response', 'error_type': 'ModelOutputLimitExceeded', 'retryable': True}
        self.assertTrue(verifier.report_provider_outcome(attempt, call, {'provider_attempts': [row]}))
        for key, value in [('usage_call_id', 'other'), ('retryable', False), ('agent', 'query'),
                           ('status', 'completed'), ('failure_stage', 'request')]:
            changed = {**row, key: value}
            with self.subTest(key=key), self.assertRaises(ValueError):
                verifier.report_provider_outcome(attempt, call, {'provider_attempts': [changed]})
        with self.assertRaises(ValueError):
            verifier.report_provider_outcome(attempt, call, {})

    def test_network_failure_does_not_become_accepted_sdk_output(self):
        attempt = {'attempt': 2, 'status': 'provider_error', 'error_type': 'APIConnectionError'}
        self.assertFalse(verifier.report_provider_outcome(attempt, {'status': 'error'}, {}))
        with self.assertRaises(ValueError):
            verifier.report_provider_outcome(attempt, {'status': 'completed'}, {})


if __name__ == '__main__':
    unittest.main()
