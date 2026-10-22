import argparse
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import report_recovery as recovery
from runner import save_json, digest


class RecoveryTests(unittest.TestCase):
    def test_configuration_migration_preserves_task_statuses_and_context_identity(self):
        import base64, hashlib
        payload = {'config_fingerprint': 'a'*64, 'instance_id': 'b'*64,
                   'graph': {'tasks': [{'id': 'report', 'status': 'FAILED'}]}, 'generation': 12}
        raw = json.dumps(payload).encode()
        source = {'payload': base64.b64encode(raw).decode(), 'checksum': hashlib.sha256(raw).hexdigest()}
        migrated = recovery.rebind_configuration(source, 'c'*64)
        actual = base64.b64decode(migrated['payload'])
        self.assertEqual(json.loads(actual), {**payload, 'config_fingerprint': 'c'*64})
        self.assertEqual(hashlib.sha256(actual).hexdigest(), migrated['checksum'])
        source['checksum'] = '0'*64
        with self.assertRaises(ValueError): recovery.rebind_configuration(source, 'c'*64)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'bf-auto-small-123456789012345678'
        self.folder = self.root / recovery.DIRECTORY
        self.folder.mkdir(parents=True)
        self.wf = self.root.name
        rows = [{'task': {'id': self.wf+'-'+s, 'workflow_id': self.wf}, 'status': 'SUCCEEDED'} for s in recovery.STAGES]
        rows.append({'task': {'id': self.wf+'-report', 'workflow_id': self.wf}, 'status': 'FAILED'})
        save_json(self.root/'task-snapshot.json', {'tasks': rows})
        self.auth = {'schema': 'bettafish.report-recovery/v1', 'workflow_id': self.wf,
                     'task_id': self.wf+recovery.SUFFIX, 'original_task_id': self.wf+'-report',
                     'max_model_requests': 5, 'prior_aggregate_requests': 584, 'aggregate_limit': 589,
                     'original_statuses': recovery.original_states(self.root),
                     'original_sha256': {'task-snapshot.json': digest(self.root/'task-snapshot.json')},
                     'integration_dir': str(Path(recovery.__file__).parent), 'code_sha256': {}}
        save_json(self.folder/'authorization.json', self.auth)

    def test_recovery_keeps_original_failed_and_exposes_actual_new_task(self):
        save_json(self.folder/'task-snapshot.json', {'tasks': [{'task': {'id': self.auth['task_id'], 'workflow_id': self.wf}, 'status': 'RUNNING'}]})
        value = recovery.view(self.root)
        self.assertEqual(value['status'], 'RUNNING')
        self.assertEqual(value['original_status'], 'FAILED')
        self.assertFalse(value['fresh_from_zero'])
        self.assertEqual(recovery.original_states(self.root)[self.wf+'-report'], 'FAILED')

    def test_cannot_expand_budget_or_recover_nonreport(self):
        args = argparse.Namespace(run_dir=str(self.root.parent), role='report', mode='live', recovery_of=self.wf+'-report')
        request = {'workflow_id': self.wf, 'task_id': self.auth['task_id']}
        self.assertEqual(recovery.validate_adapter_recovery(args, request, {'max_model_requests': 5})['reused_tasks'], 8)
        with self.assertRaises(ValueError):
            recovery.validate_adapter_recovery(args, request, {'max_model_requests': 6})
        args.role = 'query'
        with self.assertRaises(ValueError):
            recovery.validate_adapter_recovery(args, request, {'max_model_requests': 5})

    def test_old_evidence_tampering_blocks_recovery(self):
        with (self.root/'task-snapshot.json').open('a') as stream:
            stream.write(' ')
        with self.assertRaisesRegex(ValueError, 'evidence changed'):
            recovery.check_originals(self.root, self.auth)

    def test_missing_independent_or_browser_receipt_keeps_report_closed(self):
        with self.assertRaises(FileNotFoundError):
            recovery.report_path(self.root)
        save_json(self.folder/'verification.json', {'status': 'passed'})
        save_json(self.folder/'browser-qa.json', {'status': 'passed'})
        with self.assertRaises(ValueError):
            recovery.report_path(self.root)

    def test_recovery_start_error_does_not_show_waiting_forever(self):
        save_json(self.folder/'summary.json', {'status': 'error'})
        self.assertEqual(recovery.view(self.root)['status'], 'FAILED')

    def test_crashed_node_does_not_leave_stale_running_status(self):
        save_json(self.folder/'task-snapshot.json', {'tasks': [{'task': {'id': self.auth['task_id'], 'workflow_id': self.wf}, 'status': 'RUNNING'}]})
        save_json(self.folder/'interruption.json', {'workflow_id':self.wf,'task_id':self.auth['task_id'],
                  'reason':'execution_node_interrupted','automatic_restart':False})
        view = recovery.view(self.root)
        self.assertEqual(view['status'], 'FAILED')
        self.assertTrue(view['interrupted'])
        original = json.loads((self.folder/'task-snapshot.json').read_text(encoding='utf-8'))
        self.assertEqual(original['tasks'][0]['status'], 'RUNNING')

    def test_known_interruption_survives_an_empty_snapshot_without_rewriting_it(self):
        path = self.folder/'task-snapshot.json'
        path.write_bytes(b'')
        save_json(self.folder/'interruption.json', {'workflow_id':self.wf,'task_id':self.auth['task_id'],
                  'reason':'execution_node_interrupted','automatic_restart':False})
        self.assertEqual(recovery.view(self.root)['status'], 'FAILED')
        self.assertEqual(path.read_bytes(), b'')

    def test_report_notice_is_explicit_about_reused_results(self):
        page = recovery.recovery_notice('<main>report</main>')
        self.assertIn(recovery.NOTICE, page)

    def test_pending_recovery_cannot_start_processes(self):
        self.auth['execution_authorized'] = False
        save_json(self.folder/'authorization.json', self.auth)
        with patch.object(recovery.subprocess, 'Popen') as process:
            with self.assertRaisesRegex(ValueError, 'explicitly authorized'):
                recovery.run(self.root, 'unused')
        process.assert_not_called()
        self.assertFalse((self.folder/'start-once.lock').exists())

    def test_second_recovery_is_selected_only_by_operator_pointer(self):
        original = (self.folder/'authorization.json').read_bytes()
        second = self.root/'.report-recovery-2'
        auth = {**self.auth, 'task_id':self.wf+'-report-recovery-2',
                'prior_aggregate_requests':586, 'aggregate_limit':591}
        save_json(second/'authorization.json', auth)
        self.assertEqual(recovery.record(self.root)['task_id'],self.auth['task_id'])
        save_json(self.root/'report-recovery-index.json',{'directory':second.name})
        self.assertEqual(recovery.record(self.root)['task_id'],auth['task_id'])
        self.assertEqual((self.folder/'authorization.json').read_bytes(),original)
        self.assertEqual(recovery.record(self.root, self.folder.name)['task_id'], self.auth['task_id'])

    def test_recovery_cannot_exceed_aggregate_ceiling(self):
        self.auth.update(prior_aggregate_requests=592,aggregate_limit=597)
        save_json(self.folder/'authorization.json',self.auth)
        with self.assertRaises(ValueError): recovery.record(self.root)

    def test_recovery_pointer_cannot_escape_run(self):
        save_json(self.root/'report-recovery-index.json',{'directory':'../outside'})
        with self.assertRaises(ValueError): recovery.folder_for(self.root)

    def test_prepare_second_recovery_keeps_display_and_requires_later_authorization(self):
        launch = self.root.parent/'launch.json'
        save_json(launch,{'workflow_id':self.wf,'aggregate_limit':596})
        save_json(self.root/'summary.json',{'status':'error','cleanup':{'daemon_stopped':True,'tasks_terminal':True}})
        save_json(self.root/(self.wf+'-report')/'state.json',{'status':'failed'})
        control = self.root/'.control'
        save_json(control/'runtime.state',{})
        save_json(control/'runtime.state.context',{})
        save_json(control/'agents.json',{'agents':[{'id':'bettafish-report','args':['old-adapter.py']}]})
        save_json(self.root/'submitted-tasks.json',{'tasks':[{'id':self.wf+'-report',
                  'goal':json.dumps({'max_model_requests':5,'model_timeout_seconds':300,'max_output_tokens':32000})}]})
        def migrate(root,binary,name):
            self.assertEqual(name,'.report-recovery-2')
            save_json(root/name/'configuration-migration.json',{'model_calls':0})
        with patch.object(recovery,'migrate_configuration',side_effect=migrate):
            result = recovery.prepare(self.root,launch,name='.report-recovery-2',prior_aggregate=586)
        auth = recovery.record(self.root,'.report-recovery-2')
        self.assertFalse(auth['execution_authorized'])
        self.assertEqual(auth['aggregate_limit'],591)
        self.assertEqual(result['model_calls'],0)
        self.assertEqual(recovery.record(self.root)['task_id'],self.auth['task_id'])


if __name__ == '__main__':
    unittest.main()
