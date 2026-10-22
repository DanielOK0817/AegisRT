"""The usual Flask entry must expose real nine-task runs without rewriting them."""
import hashlib
import json
import unittest
import test_app_service as entry_tests
from app_service import WorkflowService, save_json
from observed_workflows import load_registry, STAGES


class ObservedWorkflowTests(unittest.TestCase):
    def setUp(self):
        entry_tests.EntryTests.setUp(self)
        self.workflow = 'bf-auto-small-' + 'a' * 18
        self.stage = self.root / 'automatic'
        self.run = self.stage / 'private/runs' / self.workflow
        self.run.mkdir(parents=True)
        self.revision = 'code-' + 'b' * 16
        self.frozen = self.stage / 'revisions' / self.revision / 'integration'
        self.frozen.mkdir(parents=True)
        (self.frozen / 'adapter.py').write_text('test adapter', encoding='utf-8')
        self.save(self.stage / 'stage-manifest.json', {'revisions': {self.revision: [
            {'path': 'integration/adapter.py', 'sha256': hashlib.sha256((self.frozen / 'adapter.py').read_bytes()).hexdigest()}]}})
        self.save(self.stage / 'private/rounds' / (self.workflow + '.json'), {
            'workflow_id': self.workflow, 'task_count': 9, 'topic': '九任务实际记录',
            'code_revision': self.revision, 'sample_count': 6, 'max_model_requests': 58})
        self.save(self.run / 'summary.json', {'workflow_id': self.workflow, 'status': 'error',
            'model_usage': {'provider_attempts': 44}})
        self.save(self.run / 'task-snapshot.json', {'tasks': [
            {'task': {'id': self.workflow + '-' + stage, 'workflow_id': self.workflow,
                      'agent_id': 'bettafish-' + stage.split('-')[0]},
             'status': 'FAILED' if stage == 'report' else 'SUCCEEDED'} for stage in STAGES]})
        self.registry = self.root / 'observed-runs.json'
        self.save(self.registry, {'schema': 'aegisrt.observed-workflows/v1',
            'default_workflow_id': self.workflow, 'workflows': [{'workflow_id': self.workflow,
            'stage_dir': str(self.stage), 'qa_receipt': str(self.root / 'qa.json')}]})
        self.service.observed, self.service.default_workflow_id = load_registry(
            self.registry, env_file=self.service.env_file, binary=self.service.binary)

    def save(self, path, value):
        path.parent.mkdir(parents=True, exist_ok=True)
        save_json(path, value)

    def test_main_history_status_content_and_monitor_expose_nine_task_run_readonly(self):
        before = {str(p): p.read_bytes() for p in self.stage.rglob('*') if p.is_file()}
        old = self.service.run_root / 'old-five'
        self.save(old / 'entry.json', {'workflow_id': 'old-five', 'query': '旧五任务', 'status': 'FAILED', 'sample_count': 6})
        history = self.client.get('/api/aegisrt/workflows').json['workflows']
        self.assertEqual([(r['workflow_id'], r['task_count']) for r in history], [(self.workflow, 9), ('old-five', 5)])
        self.assertEqual(self.service.configuration()['default_workflow_id'], self.workflow)
        prefix = '/api/aegisrt/workflows/' + self.workflow
        state = self.client.get(prefix).json
        self.assertEqual(len(state['tasks']), 9)
        self.assertEqual(sum(t['status']=='SUCCEEDED' for t in state['tasks']), 8)
        self.assertEqual(state['model_requests']['attempts'], 44)
        self.assertTrue(state['readonly'])
        self.assertEqual(self.client.get(prefix+'/content').status_code, 200)
        self.assertEqual(len(self.client.get(prefix+'/monitor').json['tasks']), 9)
        self.assertEqual(self.client.get(prefix+'/report').status_code, 409)
        self.assertEqual(self.client.post(prefix+'/cancel', json={}).status_code, 409)
        self.assertEqual(before, {str(p): p.read_bytes() for p in self.stage.rglob('*') if p.is_file()})
        self.assertFalse((self.run / 'entry.json').exists())

    def test_constructor_restores_registry_and_binds_actual_adapter(self):
        service = WorkflowService(source_dir=self.service.source_dir, integration_dir=self.service.integration_dir,
            binary=self.service.binary, env_file=self.service.env_file, run_root=self.service.run_root,
            sample_root=self.service.sample_root, launch=False)
        self.assertEqual(service.reader(self.workflow).integration_dir, self.frozen.resolve())
        self.assertEqual(service.reader(self.workflow).run_root, self.run.parent)
        self.assertIs(service.reader('old-five'), service)

    def test_tampered_adapter_cannot_be_registered(self):
        (self.frozen / 'adapter.py').write_text('changed', encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'identity'):
            load_registry(self.registry, env_file=self.service.env_file, binary=self.service.binary)

    def test_recovery_maps_report_to_real_new_attempt_and_stays_gated(self):
        import report_recovery as recovery
        folder = self.run / recovery.DIRECTORY
        folder.mkdir()
        task_id = self.workflow + recovery.SUFFIX
        self.save(folder / 'authorization.json', {'schema': 'bettafish.report-recovery/v1',
            'workflow_id': self.workflow, 'task_id': task_id, 'original_task_id': self.workflow+'-report',
            'max_model_requests': 5, 'prior_aggregate_requests': 584, 'aggregate_limit': 589,
            'original_statuses': recovery.original_states(self.run), 'integration_dir': str(self.frozen), 'code_sha256': {}})
        snapshot = json.loads((self.run/'task-snapshot.json').read_text(encoding='utf-8'))
        snapshot['tasks'].append({'task': {'id': task_id, 'workflow_id': self.workflow, 'agent_id': 'bettafish-report'}, 'status': 'RUNNING'})
        self.save(folder / 'task-snapshot.json', snapshot)
        self.save(folder / 'submitted-tasks.json', {'tasks': [{'id':task_id,'workflow_id':self.workflow,
            'agent_id':'bettafish-report','dependencies':[self.workflow+'-'+stage for stage in
                ('query-feedback','media-feedback','insight-feedback','forum-final')]}]})
        prefix = '/api/aegisrt/workflows/' + self.workflow
        state = self.client.get(prefix).json
        self.assertEqual(state['status'], 'RUNNING')
        self.assertEqual(state['tasks'][-1]['task_id'], task_id)
        self.assertEqual(state['recovery']['original_status'], 'FAILED')
        monitored = self.client.get(prefix+'/monitor').json
        self.assertEqual(len(monitored['tasks']), 9)
        self.assertEqual(monitored['tasks'][-1]['id'], task_id)
        self.assertEqual(monitored['tasks'][-1]['status'], 'RUNNING')
        self.assertEqual(self.client.get(prefix+'/report').status_code, 409)
        (folder/'task-snapshot.json').write_bytes(b'')
        self.save(folder/'interruption.json', {'workflow_id':self.workflow,'task_id':task_id,
                  'reason':'execution_node_interrupted','automatic_restart':False})
        self.assertEqual(self.client.get(prefix).json['status'], 'FAILED')
        self.assertEqual(self.client.get(prefix+'/content').status_code, 200)
        self.assertEqual(self.client.get(prefix+'/monitor').status_code, 200)
        self.assertEqual(self.client.get(prefix+'/monitor').json['tasks'][-1]['dependencies'],
                         ['query-feedback','media-feedback','insight-feedback','forum-final'])
        self.assertEqual((folder/'task-snapshot.json').read_bytes(), b'')

if __name__ == '__main__': unittest.main()
