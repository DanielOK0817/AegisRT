"""Offline provider-attempt fixtures; no models, services or real workflows."""
import copy
import hashlib
import json
import os
from pathlib import Path
import unittest
from types import SimpleNamespace
from unittest.mock import Mock, patch
import test_decision_verifier as decision_fixtures
import test_structure_verifier as structure_fixtures
import verify_round
import workflow_common as common


class ProviderRetryVerifierTests(unittest.TestCase):
    def setUp(self):
        self.case = structure_fixtures.StructureVerifierTests(methodName='runTest')
        self.case.setUp()
        self.addCleanup(self.case.doCleanups)
        self.adapter = structure_fixtures.adapter
        self.case.launch.update(max_output_tokens=16000)

    def error_metadata(self, error_type):
        if getattr(self.adapter, 'PROVIDER_RETRY_SCHEMA', None) not in ('bettafish.provider-retry/v2', 'bettafish.provider-retry/v3'):
            return {}
        return {'exception_chain_types': [error_type], 'exception_chain_truncated': False,
                'deadline_origin': 'enclosing' if error_type == 'ModelDeadlineExceeded' else None,
                'cancellation_in_chain': error_type in ('KeyboardInterrupt', 'SystemExit', 'GeneratorExit', 'CancelledError')}

    def outline_retry(self):
        task, audit = self.case.task, self.case.state['research_audit']
        return self.trace(self.case, audit['structure_requests'])

    def trace(self, case, inputs, retry_index=0):
        case.launch.setdefault('max_output_tokens', 16000)
        task, attempts = case.task, []
        physical = [(record, failed) for index, record in enumerate(inputs)
                    for failed in ([True, False] if index == retry_index else [False])]
        failed_ordinal = retry_ordinal = None
        attempts = []
        for ordinal, (input_record, failed) in enumerate(physical, 1):
            supplied = common.read(input_record['path'])
            kwargs = {'model': 'offline-model', 'messages': [
                {'role': 'system', 'content': supplied['system']},
                {'role': 'user', 'content': supplied['user']}],
                'timeout': 119 if getattr(self.adapter, 'PROVIDER_RETRY_SCHEMA', None) in ('bettafish.provider-retry/v2', 'bettafish.provider-retry/v3') else 120,
                'max_tokens': 16000, 'stream': False}
            digest = hashlib.sha256(self.adapter.canonical(kwargs).encode('utf-8')).hexdigest()
            request = task / 'model-requests' / f'request-{ordinal:03d}.json'
            request.parent.mkdir(parents=True, exist_ok=True)
            request.write_text(self.adapter.canonical(kwargs), encoding='utf-8')
            usage = {'call_id': f'call-{ordinal}', 'agent': 'query', 'run_id': case.launch['workflow_id'],
                'status': 'error' if failed else 'completed',
                'stream': False,
                'started_at': f'2026-10-03T00:00:{ordinal * 2:02d}+00:00',
                'ended_at': f'2026-10-03T00:00:{ordinal * 2 + 1:02d}+00:00',
                'usage_present': not failed, 'total_tokens': None if failed else 23}
            if failed:
                usage.update(error_type='APITimeoutError', http_status=None)
                failed_ordinal, retry_ordinal = ordinal, ordinal + 1
            common.write(task / 'model-usage' / (usage['call_id'] + '.json'), usage)
            attempts.append({'attempt': ordinal, 'agent': 'query', 'request_sha256': digest,
                'request_record': self.adapter.file_record(request), 'input_record': copy.deepcopy(input_record),
                'usage_call_id': usage['call_id'], 'status': usage['status'], 'stream': False,
                'delivered_chunks': 0, 'retryable': failed, 'absolute_timeout_seconds': 120,
                **({'error_type': 'APITimeoutError', 'http_status': None, 'failure_stage': 'request',
                    **self.error_metadata('APITimeoutError')} if failed else {})})
            if getattr(self.adapter, 'PROVIDER_RETRY_SCHEMA', None) == 'bettafish.provider-retry/v3':
                name = Path(input_record['path']).name
                if name.startswith('structure-'):
                    step = 'structure'
                elif name.startswith('summary-'):
                    step = 'initial-summary' if name.endswith('-initial.json') else 'reflection-summary'
                else:
                    supplied_data = json.loads(supplied['user'])
                    step = ('reflection-decision' if supplied_data.get('paragraph_latest_state') or
                            case.state['research_audit'].get('phase') == 'refinement' else 'initial-decision')
                attempts[-1].update(required_calls_after=4 if step == 'structure' else 0, research_step=step)
        first = attempts[failed_ordinal - 1]
        budget = {'attempts': len(attempts), 'failed_attempts': 1, 'limit': 40, 'agents': {'query': len(attempts)},
            'request_timeout_seconds': 120, 'request_hash_scope': 'recorded_completion_kwargs', 'retry_transient': True,
            'provider_attempts': attempts,
            'provider_retries': [{'failed_attempt': failed_ordinal, 'retry_attempt': retry_ordinal, 'agent': 'query',
                'request_sha256': first['request_sha256'], 'input_record': copy.deepcopy(first['input_record']),
                'failed_usage_call_id': first['usage_call_id'], 'retry_usage_call_id': attempts[retry_ordinal - 1]['usage_call_id']}]}
        if getattr(self.adapter, 'PROVIDER_RETRY_SCHEMA', None) in ('bettafish.provider-retry/v2', 'bettafish.provider-retry/v3'):
            budget['retry_policy_schema'] = self.adapter.PROVIDER_RETRY_SCHEMA
        common.write(task / 'model-budget.json', budget)
        return budget

    def save(self, budget):
        common.write(self.case.task / 'model-budget.json', budget)

    def rewrite_kwargs(self, budget, index, mutate):
        record = budget['provider_attempts'][index]
        path = common.inside(self.case.task, record['request_record']['path'])
        value = common.read(path)
        mutate(value)
        path.write_text(self.adapter.canonical(value), encoding='utf-8')
        record.update(request_record=self.adapter.file_record(path), request_sha256=common.sha(path))

    def test_timeout_then_bad_json_still_has_only_one_format_correction(self):
        self.case.prepare(first_rejected=True)
        self.outline_retry()
        result = self.case.verify(source_adapter=self.adapter)
        self.assertEqual((result['provider_attempts_verified'], result['transient_retries_verified'],
                          result['structure_requests_verified'], result['format_corrections']), (3, 1, 2, 1))

    def test_reflection_decision_and_summary_inputs_keep_their_real_usage_mapping(self):
        case = decision_fixtures.DecisionVerifierTests(methodName='runTest')
        case.setUp()
        self.addCleanup(case.doCleanups)
        case.launch.update(workflow_id='offline-decisions', max_output_tokens=16000)
        audit, ordered = case.state['research_audit'], []
        audit['summary_requests'] = []
        for index, decision in enumerate(audit['decision_requests']):
            ordered.append(decision)
            data = common.read(decision['path'])
            user = __import__('json').loads(data['user'])
            kind = 'initial' if index % 2 == 0 else 'reflection'
            path = case.task / 'model-inputs' / f'summary-{index + 1:03d}-{kind}.json'
            common.write(path, {'system': 'Role summary ' + kind, 'user': __import__('json').dumps(user, ensure_ascii=False)})
            record = self.adapter.file_record(path)
            audit['summary_requests'].append(record)
            ordered.append(record)
        self.trace(case, ordered, retry_index=2)
        result = case.verify(source_adapter=self.adapter)
        self.assertEqual(result['logical_decisions_verified'], 4)
        self.assertEqual((result['provider_attempts_verified'], result['transient_retries_verified']), (9, 1))
        self.assertFalse(result['actual_sdk_messages_hash_recorded'])

    def test_missing_and_duplicate_real_usage_mappings_are_rejected(self):
        budget = self.outline_retry()
        budget['provider_attempts'][1]['usage_call_id'] = 'call-1'
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'unique real usage'):
            self.case.verify(source_adapter=self.adapter)
        budget['provider_attempts'][1]['usage_call_id'] = 'call-2'
        self.save(budget)
        (self.case.task / 'model-usage/call-1.json').unlink()
        with self.assertRaisesRegex(ValueError, 'own usage record'):
            self.case.verify(source_adapter=self.adapter)

    def test_rehashed_retry_kwargs_cannot_change_temperature(self):
        budget = self.outline_retry()
        self.rewrite_kwargs(budget, 1, lambda data: data.update(temperature=0.5))
        budget['provider_retries'][0]['request_sha256'] = budget['provider_attempts'][1]['request_sha256']
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'physical kwargs'):
            self.case.verify(source_adapter=self.adapter)

    def test_request_hash_and_actual_node_input_cannot_be_removed_or_reassigned(self):
        for mutation, message in (
                (lambda row: row.update(request_sha256='0' * 64), 'kwargs hash'),
                (lambda row: row.update(input_record=None), 'cannot be omitted'),
                (lambda row: row.update(input_record={**row['input_record'], 'sha256': '0' * 64}), 'checksum'),
                (lambda row: row.update(agent='report'), 'Unknown provider agent')):
            with self.subTest(message=message):
                self.case.prepare()
                budget = self.outline_retry()
                mutation(budget['provider_attempts'][1])
                self.save(budget)
                with self.assertRaisesRegex((ValueError, self.adapter.AdapterError), message):
                    self.case.verify(source_adapter=self.adapter)

    def test_duplicate_retry_and_missing_failure_counts_are_rejected(self):
        budget = self.outline_retry()
        budget['provider_retries'].append(copy.deepcopy(budget['provider_retries'][0]))
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'duplicated'):
            self.case.verify(source_adapter=self.adapter)
        budget['provider_retries'].pop()
        budget['failed_attempts'] = 0
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'failed physical attempts'):
            self.case.verify(source_adapter=self.adapter)

    def test_new_source_success_gate_cannot_fall_back_after_trace_fields_are_deleted(self):
        workflow, stage, folder = 'offline-gate', self.case.root / 'stage', self.case.task
        expected = [workflow + '-' + suffix for suffix in common.STAGES]
        goals = {'max_model_requests': 40}
        adapter = SimpleNamespace(SCHEMA='offline-schema', RESEARCH_ROLES=('query', 'media', 'insight'),
            model_input_context=True, validate_request=lambda request: goals)
        registry = {'agents': [{'args': [str(stage / 'revisions/offline-code/integration/adapter.py')]} for _ in range(5)]}
        def read_fixture(path):
            path = Path(path)
            if path.name == 'task-snapshot.json':
                return {'tasks': [{'task': {'id': identifier}, 'status': 'SUCCEEDED'} for identifier in expected]}
            if path.name == 'submitted-tasks.json':
                return {'tasks': [{'id': identifier, 'dependencies': [], 'max_attempts': 1, 'goal': {}} for identifier in expected]}
            if path.name == 'events.json':
                return {'pages': [{'events': [{'id': identifier, 'task_id': identifier, 'type': 'run.succeeded',
                    'attempt_id': identifier + '-attempt-1'} for identifier in expected]}]}
            if path.name == 'agents.json':
                return registry
            if path.name == 'result.json':
                return {'schema': adapter.SCHEMA, 'workflow_id': workflow, 'task_id': path.parent.name,
                        'mode': 'live', 'files': {}}
            if path.name == 'state.json':
                return {'status': 'completed', 'parameters': goals}
            if path.name == 'model-budget.json':
                return {'limit': 40, 'attempts': 1, 'retry_transient': True}
            raise AssertionError('Unexpected fixture read: ' + str(path))
        with patch.object(verify_round, 'read', side_effect=read_fixture):
            with self.assertRaisesRegex(ValueError, 'omitted its physical provider trace'):
                verify_round.verify_success(stage, folder, {'workflow_id': workflow, 'code_revision': 'offline-code'},
                                           {}, adapter, None)

    def test_real_recorded_completion_and_budget_bind_fake_client_retry_without_network(self):
        usage = common.module('offline_provider_retry_usage', structure_fixtures.ROOT.parents[2] /
            'reproductions/bettafish-20261001/source/utils/llm_usage.py')
        for path in (self.case.task / 'model-usage').glob('*.json'):
            path.unlink()  # This test owns an isolated TemporaryDirectory only.
        record = self.case.state['research_audit']['structure_requests'][0]
        supplied = common.read(record['path'])
        response = SimpleNamespace(choices=[SimpleNamespace(finish_reason='stop')], model='offline-model',
            usage=SimpleNamespace(prompt_tokens=3, completion_tokens=5, total_tokens=8))
        create = Mock(side_effect=[TimeoutError('offline timeout'), response])
        client = SimpleNamespace(chat=SimpleNamespace(completions=SimpleNamespace(create=create)), max_retries=2,
                                 base_url='https://offline.invalid/v1')
        budget = self.adapter.ModelBudget(self.case.task / 'model-budget.json', 'offline-input', 40, 120, 16000,
                                          retry_transient=True)
        with patch.dict(os.environ, {'LLM_USAGE_DIR': str(self.case.task / 'model-usage'),
                                    'BETTAFISH_RUN_ID': self.case.launch['workflow_id']}), \
                patch('socket.socket', side_effect=AssertionError('Offline fixture opened a socket')):
            with self.adapter.model_call_reservation(4, 'structure'), self.adapter.model_input_context(record):
                actual = budget.wrap(usage.recorded_completion)(client, 'query', model='offline-model',
                    messages=[{'role': 'system', 'content': supplied['system']},
                              {'role': 'user', 'content': supplied['user']}], stream=False)
            result = self.case.verify(source_adapter=self.adapter)
        self.assertIs(actual, response)
        self.assertEqual((create.call_count, client.max_retries), (2, 0))
        self.assertEqual((result['provider_attempts_verified'], result['transient_retries_verified'],
                          result['failed_provider_attempts_preserved'], result['calls_missing_usage_preserved']), (2, 1, 1, 1))

    def test_non_transient_and_local_deadline_failures_cannot_be_relabeled_retryable(self):
        for error, status, retryable in (('AuthenticationError', 401, True), ('APIConnectionError', 401, True), ('ModelDeadlineExceeded', None, True),
                                        ('APITimeoutError', None, False)):
            with self.subTest(error=error, retryable=retryable):
                self.case.prepare()
                budget = self.outline_retry()
                budget['provider_attempts'][0].update(error_type=error, http_status=status, retryable=retryable,
                                                     **self.error_metadata(error))
                path = self.case.task / 'model-usage/call-1.json'
                value = common.read(path)
                value.update(error_type=error, http_status=status)
                common.write(path, value)
                self.save(budget)
                with self.assertRaisesRegex(ValueError, 'allowed transient failure'):
                    self.case.verify(source_adapter=self.adapter)

    def test_stream_retry_is_allowed_only_before_any_delivered_chunk(self):
        budget = self.outline_retry()
        for index in (0, 1):
            self.rewrite_kwargs(budget, index, lambda data: data.update(stream=True))
            budget['provider_attempts'][index]['stream'] = True
            path = self.case.task / 'model-usage' / f'call-{index + 1}.json'
            value = common.read(path)
            value['stream'] = True
            common.write(path, value)
        budget['provider_attempts'][0]['failure_stage'] = 'stream'
        budget['provider_attempts'][1]['delivered_chunks'] = 2
        budget['provider_retries'][0]['request_sha256'] = budget['provider_attempts'][0]['request_sha256']
        self.save(budget)
        self.assertEqual(self.case.verify(source_adapter=self.adapter)['transient_retries_verified'], 1)
        budget['provider_attempts'][0]['delivered_chunks'] = 1
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'before any delivered chunk'):
            self.case.verify(source_adapter=self.adapter)

    def test_sdk_completed_cleanup_failure_keeps_failed_budget_and_never_passes(self):
        budget = self.outline_retry()
        budget['provider_attempts'][0].update(error_type='RuntimeError', failure_stage='cleanup', retryable=False,
                                            **self.error_metadata('RuntimeError'))
        budget['provider_retries'] = []
        path = self.case.task / 'model-usage/call-1.json'
        value = common.read(path)
        value.update(status='completed', usage_present=True, total_tokens=7)
        value.pop('error_type')
        value.pop('http_status')
        common.write(path, value)
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'unrecovered provider error'):
            self.case.verify(source_adapter=self.adapter)
        budget['failed_attempts'] = 0
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'failed physical attempts'):
            self.case.verify(source_adapter=self.adapter)

    def test_cancelled_or_started_attempt_cannot_be_successfully_verified(self):
        for status in ('cancelled', 'started'):
            with self.subTest(status=status):
                budget = self.outline_retry()
                budget['provider_attempts'][1]['status'] = status
                if status == 'cancelled':
                    budget['provider_attempts'][1].update(error_type='GeneratorExit', **self.error_metadata('GeneratorExit'))
                self.save(budget)
                with self.assertRaisesRegex(ValueError, 'cancelled physical request'):
                    self.case.verify(source_adapter=self.adapter)

    def test_a_third_physical_attempt_for_one_logical_input_is_rejected(self):
        budget = self.outline_retry()
        second = budget['provider_attempts'][1]
        second.update(status='error', retryable=True, error_type='APITimeoutError', http_status=None, failure_stage='request',
                      **self.error_metadata('APITimeoutError'))
        usage = common.read(self.case.task / 'model-usage/call-2.json')
        usage.update(status='error', error_type='APITimeoutError', http_status=None, usage_present=False, total_tokens=None)
        common.write(self.case.task / 'model-usage/call-2.json', usage)
        third = copy.deepcopy(second)
        third.update(attempt=3, usage_call_id='call-3', status='completed')
        request = self.case.task / 'model-requests/request-003.json'
        request.write_bytes(common.inside(self.case.task, second['request_record']['path']).read_bytes())
        third['request_record'] = self.adapter.file_record(request)
        budget['provider_attempts'].append(third)
        common.write(self.case.task / 'model-usage/call-3.json', {
            **usage, 'call_id': 'call-3', 'status': 'completed', 'usage_present': True, 'total_tokens': 23,
            'started_at': '2026-10-03T00:00:06+00:00', 'ended_at': '2026-10-03T00:00:07+00:00'})
        budget.update(attempts=3, failed_attempts=2, agents={'query': 3})
        budget['provider_retries'].append({**budget['provider_retries'][0], 'failed_attempt': 2, 'retry_attempt': 3,
            'failed_usage_call_id': 'call-2', 'retry_usage_call_id': 'call-3'})
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'repeated retry'):
            self.case.verify(source_adapter=self.adapter)

    def test_transient_outline_retry_keeps_one_node_and_both_usage_records(self):
        self.outline_retry()
        with patch('socket.socket', side_effect=AssertionError('Offline fixture opened a socket')):
            result = self.case.verify(source_adapter=self.adapter)
        self.assertEqual(result['structure_requests_verified'], 1)
        self.assertEqual(result['provider_attempts_verified'], 2)
        self.assertEqual(result['transient_retries_verified'], 1)
        self.assertEqual(result['failed_provider_attempts_preserved'], 1)
        self.assertFalse(result['actual_sdk_messages_hash_recorded'])

    def test_v3_reservation_metadata_and_retry_capacity_are_verified(self):
        self.outline_retry()
        result = self.case.verify(source_adapter=self.adapter)
        self.assertEqual(result['retry_policy_schema_verified'], 'bettafish.provider-retry/v3')
        self.assertTrue(result['required_capacity_metadata_verified'])
        self.assertFalse(result['required_step_counts_independently_reconstructed'])

    def test_v3_rejects_missing_or_ill_typed_required_capacity(self):
        for value in (None, True, -1, 0.5, '4'):
            with self.subTest(value=value):
                budget = self.outline_retry()
                if value is None:
                    budget['provider_attempts'][0].pop('required_calls_after')
                else:
                    budget['provider_attempts'][0]['required_calls_after'] = value
                self.save(budget)
                with self.assertRaisesRegex(ValueError, 'required research capacity'):
                    self.case.verify(source_adapter=self.adapter)

    def test_v3_rejects_attempt_that_spent_reserved_later_capacity(self):
        budget = self.outline_retry()
        for row in budget['provider_attempts']:
            row['required_calls_after'] = 39
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'required research capacity'):
            self.case.verify(source_adapter=self.adapter)

    def test_v3_reservation_step_must_match_the_archived_node_input(self):
        budget = self.outline_retry()
        for row in budget['provider_attempts']:
            row['research_step'] = 'initial-summary'
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'reservation step.*node input'):
            self.case.verify(source_adapter=self.adapter)

    def test_v3_retry_cannot_change_reservation_or_bypass_a_block(self):
        for mutation in ({'required_calls_after': 3}, {'retry_blocked_reason': 'reserved_for_required_research_calls'}):
            with self.subTest(mutation=mutation):
                budget = self.outline_retry()
                budget['provider_attempts'][1].update(mutation)
                self.save(budget)
                with self.assertRaisesRegex(ValueError, 'retry.*reservation'):
                    self.case.verify(source_adapter=self.adapter)

    def test_v3_success_cannot_hide_a_blocked_required_request(self):
        budget = self.outline_retry()
        budget['blocked_requests'] = [{'reason': 'reserved_for_required_research_calls'}]
        self.save(budget)
        with self.assertRaisesRegex(ValueError, 'blocked required request'):
            self.case.verify(source_adapter=self.adapter)

    def test_v2_frozen_policy_still_verifies_without_v3_reservation_fields(self):
        capabilities = {name: getattr(self.adapter, name) for name in
                        ('canonical', 'checked_record', 'RESEARCH_ROLES')}
        legacy_v2 = SimpleNamespace(**capabilities, PROVIDER_RETRY_SCHEMA='bettafish.provider-retry/v2')
        budget = self.outline_retry()
        budget['retry_policy_schema'] = legacy_v2.PROVIDER_RETRY_SCHEMA
        for row in budget['provider_attempts']:
            row.pop('required_calls_after')
            row.pop('research_step')
        self.save(budget)
        result, completed = verify_round._research_provider_trace(
            self.case.task, 'query', self.case.state, self.case.launch, legacy_v2)
        self.assertEqual(result['retry_policy_schema_verified'], 'bettafish.provider-retry/v2')
        self.assertNotIn('required_capacity_metadata_verified', result)
        self.assertEqual(len(completed), 1)


if __name__ == '__main__':
    unittest.main(verbosity=2)
