"""No-model checks for resource measurement and runtime identity boundaries."""
import base64
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

from flask import Flask

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from app_service import install_aegisrt_entry, save_json
from runtime_monitor import RuntimeMonitor


class RuntimeMonitorTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in ('app.py', 'binary', 'app.env'):
            (self.root / name).write_text('private-secret-value', encoding='utf-8')
        env = {'BETTAFISH_AEGISRT_BINARY': str(self.root / 'binary'),
               'BETTAFISH_AEGISRT_ENV_FILE': str(self.root / 'app.env'),
               'BETTAFISH_AEGISRT_RUN_ROOT': str(self.root / 'runs'),
               'BETTAFISH_AEGISRT_SAMPLE_ROOT': str(self.root / 'samples')}
        self.app = Flask(__name__)
        with patch.dict(os.environ, env):
            self.service = install_aegisrt_entry(self.app, self.root)
        self.service.launch = False
        self.workflow = 'monitor-test'
        self.folder = self.service.run_root / self.workflow
        self.folder.mkdir()
        self.metadata = {'workflow_id': self.workflow, 'query': '测试', 'status': 'FAILED',
                         'sample_count': 2, 'stage_ids': ['query', 'query-feedback', 'forum'],
                         'created_at': '2026-10-05T00:00:00Z', 'started_at': '2026-10-05T00:00:01Z',
                         'ended_at': '2026-10-05T00:01:01Z'}
        save_json(self.folder / 'entry.json', self.metadata)
        self.specs = []
        for stage in self.metadata['stage_ids']:
            self.specs.append({'task': {'id': self.workflow + '-' + stage, 'workflow_id': self.workflow,
                  'agent_id': 'bettafish-' + stage.split('-')[0],
                  'resources': {'cpu_milli': 500, 'memory_mib': 512, 'llm_slots': 1, 'token_budget': 16000},
                  'max_attempts': 2, 'dependencies': [self.workflow + '-query'] if stage != 'query' else [],
                  'goal': 'private-secret-value'}, 'status': 'SUCCEEDED' if stage == 'query' else 'FAILED'})
        save_json(self.folder / 'task-snapshot.json', {'tasks': self.specs})
        self.proc = self.root / 'proc'
        self.proc.mkdir()
        (self.proc / 'stat').write_text('cpu 100 0 0 900 0 0 0 0 0 0\n')
        (self.proc / 'meminfo').write_text('MemTotal: 4000 kB\nMemAvailable: 1500 kB\n')
        self.monitor = RuntimeMonitor(proc_root=self.proc)
        self.client = self.app.test_client()
        self.url = '/api/aegisrt/workflows/' + self.workflow + '/monitor'

    def checkpoint(self, context_foreign=False):
        control = self.folder / '.control'
        control.mkdir(exist_ok=True)
        runtime = {'format_version': 2, 'instance_id': 'a' * 64, 'graph': {'tasks': [
                  {'spec': row['task']} for row in self.specs]},
                  'attempts': [{'task_id': self.workflow + '-query', 'number': 2}],
                  'scheduler': {'policy_name': 'dag-drcfs', 'capacity': {'cpu_milli': 4000, 'memory_mib': 4096},
                                'allocated': {'cpu_milli': 0, 'memory_mib': 0}}}
        context = {'format_version': 2, 'runtime_instance_id': 'a' * 64, 'counter_identity': 'unicode-segment-v1',
                  'heads': [{'workflow_id': self.workflow, 'agent_id': 'bettafish-query', 'manifest_id': 'b' * 64}],
                  'manifests': [{'id': 'b' * 64, 'revision': 3, 'workflow_id': self.workflow,
                                 'agent_id': 'bettafish-query', 'shared_pages': [{'page_id': 'c' * 64, 'pin': True}]}],
                  'store': {'pages': [{'id': 'c' * 64, 'content_hash': 'd' * 64, 'scope': 'workflow',
                            'workflow_id': 'other' if context_foreign else self.workflow}],
                            'blobs': [{'content_hash': 'd' * 64, 'token_count': 17,
                                       'content': 'private-secret-value'}]}}
        self.envelope(control / 'runtime.state', runtime)
        self.envelope(control / 'runtime.state.context', context)
        return control, runtime, context

    @staticmethod
    def envelope(path, value):
        raw = json.dumps(value).encode()
        save_json(path, {'format': 'aegisrt-state', 'version': 1, 'payload': base64.b64encode(raw).decode(),
                         'checksum': hashlib.sha256(raw).hexdigest()})

    def event(self, **changes):
        event = {'workflow_id': self.workflow, 'task_id': self.workflow + '-query',
                 'attempt_id': self.workflow + '-query-attempt-1',
                 'payload': {'context': {'manifest_id': 'b' * 64, 'ref_count': 2, 'logical_tokens': 50},
                             'timing': {'run_ms': 1400, 'queue_ms': 32},
                             'output': {'artifact_count': 1, 'content_bytes': 230},
                             'private': 'private-secret-value'}}
        event.update(changes)
        save_json(self.folder / 'events.json', {'pages': [{'events': [event]}]})

    def test_historical_task_measurements_unknown_reservations_separate(self):
        self.checkpoint()
        body = self.monitor.workflow(self.service, self.workflow)
        task = body['tasks'][0]
        self.assertFalse(task['measurement']['available'])
        self.assertIsNone(task['measurement']['cpu_percent'])
        self.assertIsNone(task['measurement']['memory_rss_bytes'])
        self.assertEqual(task['resources']['cpu_milli'], 500)
        self.assertEqual(body['runtime']['capacity']['cpu_milli'], 4000)
        self.assertEqual(task['attempt_count'], 2)
        self.assertEqual(body['elapsed_seconds'], 60)

    def test_host_cpu_changes_are_sampled_and_memory_is_actual(self):
        first = self.monitor.workflow(self.service, self.workflow)
        self.assertIsNone(first['host']['cpu_percent'])
        self.assertEqual(first['host']['memory_total_bytes'], 4000 * 1024)
        self.assertEqual(first['host']['memory_used_bytes'], 2500 * 1024)
        (self.proc / 'stat').write_text('cpu 120 0 0 980 0 0 0 0 0 0\n')
        second = self.monitor.workflow(self.service, self.workflow)
        self.assertEqual(second['host']['cpu_percent'], 20)
        self.assertEqual(second['host']['cpu_basis'], 'all_cores')

    def test_task_cpu_delta_pid_reuse_and_one_core_basis(self):
        self.monitor.clock_ticks = 100
        process = {'rss': 100000, 'ticks': 10, 'start': 100}
        first = self.monitor._measurement([(12, process)], 1)
        self.assertIsNone(first['cpu_percent'])
        process['ticks'] = 160
        second = self.monitor._measurement([(12, process)], 2)
        self.assertEqual(second['cpu_percent'], 150)
        self.assertEqual(second['memory_rss_bytes'], 100000)
        self.assertEqual(second['cpu_basis'], 'one_core')
        process.update(start=101, ticks=1)
        self.assertIsNone(self.monitor._measurement([(12, process)], 3)['cpu_percent'])

    def test_process_binding_requires_owned_daemon_role_cwd_and_ancestry(self):
        rows = {}
        for pid, parent in ((100, 1), (200, 100), (201, 200), (300, 1), (400, 100)):
            directory = self.proc / str(pid)
            directory.mkdir()
            rows[pid] = {'ppid': parent, 'folder': directory, 'ticks': 5, 'start': 9, 'rss': 100}
            arguments = ([str(self.service.binary), '-state-file', str(self.folder / '.control/runtime.state'),
                          '-agents-file', str(self.folder / '.control/agents.json')] if pid == 100 else
                         [str(self.service.integration_dir / 'adapter.py'), '--role', 'query',
                          '--run-dir', str(self.service.run_root)])
            (directory / 'cmdline').write_bytes(b'\0'.join(item.encode() for item in arguments))
        mapping = {self.proc / '100/exe': self.service.binary}
        mapping.update({self.proc / f'{pid}/cwd': self.folder / (self.workflow + '-query') for pid in (200, 300)})
        mapping[self.proc / '400/cwd'] = self.root / 'other-workflow'
        resolve = Path.resolve

        def resolve_mapped(path, *args, **kwargs):
            return mapping[path] if path in mapping else resolve(path, *args, **kwargs)

        task = {'id': self.workflow + '-query', 'role': 'query', 'status': 'RUNNING'}
        with patch.object(Path, 'resolve', resolve_mapped):
            daemon = self.monitor._daemon(self.service, self.folder, rows)
            self.assertEqual(daemon, 100)
            bound = self.monitor._task_processes(self.service, self.folder, task, daemon, rows)
            self.assertEqual([pid for pid, _ in bound], [200, 201])
            task['status'] = 'SUCCEEDED'
            self.assertEqual(self.monitor._task_processes(self.service, self.folder, task, daemon, rows), [])
            task.update(status='RUNNING', role='media')
            self.assertEqual(self.monitor._task_processes(self.service, self.folder, task, daemon, rows), [])

    def test_context_heads_count_shared_role_once_without_page_bodies(self):
        self.checkpoint()
        body = self.monitor.workflow(self.service, self.workflow)
        self.assertEqual(body['tasks'][0]['context']['logical_tokens'], 17)
        self.assertEqual(body['tasks'][1]['context']['logical_tokens'], 17)
        self.assertEqual(body['context']['logical_tokens'], 17)
        self.assertEqual(body['context']['page_count'], 1)
        self.assertEqual(body['tasks'][0]['context']['shared_page_count'], 1)
        self.assertEqual(body['tasks'][0]['context']['pinned_tokens'], 17)
        serialized = json.dumps(body)
        self.assertNotIn('private-secret-value', serialized)
        self.assertNotIn(str(self.root), serialized)
        self.assertNotIn('goal', serialized)
        self.assertNotIn('content_hash', serialized)

    def test_foreign_context_or_runtime_identity_is_rejected(self):
        control, runtime, context = self.checkpoint(context_foreign=True)
        self.assertFalse(self.monitor.workflow(self.service, self.workflow)['context']['available'])
        context['runtime_instance_id'] = 'e' * 64
        self.envelope(control / 'runtime.state.context', context)
        self.assertFalse(self.monitor.workflow(self.service, self.workflow)['context']['available'])
        runtime['graph']['tasks'][0]['spec']['workflow_id'] = 'other'
        self.envelope(control / 'runtime.state', runtime)
        self.assertIsNone(self.monitor.workflow(self.service, self.workflow)['runtime']['capacity']['cpu_milli'])

    def test_checkpoint_checksum_corruption_is_not_presented(self):
        control, _, _ = self.checkpoint()
        value = json.loads((control / 'runtime.state.context').read_text())
        value['checksum'] = '0' * 64
        save_json(control / 'runtime.state.context', value)
        self.assertFalse(self.monitor.workflow(self.service, self.workflow)['context']['available'])

    def test_terminal_events_provide_captured_dispatch_and_output(self):
        self.event()
        task = self.monitor.workflow(self.service, self.workflow)['tasks'][0]
        self.assertEqual(task['context']['logical_tokens'], 50)
        self.assertEqual(task['context']['source'], 'attempt_dispatch')
        self.assertEqual(task['timing']['run_ms'], 1400)
        self.assertEqual(task['output']['content_bytes'], 230)
        self.assertEqual(task['attempt_count'], 1)
        self.assertFalse(task['measurement']['available'])

    def test_foreign_or_unbound_event_is_ignored(self):
        for changes in ({'workflow_id': 'other'}, {'task_id': 'other-query'}, {'attempt_id': '1'}):
            with self.subTest(changes=changes):
                self.event(**changes)
                task = self.monitor.workflow(self.service, self.workflow)['tasks'][0]
                self.assertFalse(task['context']['available'])
                self.assertEqual(task['attempt_count'], 0)

    def test_route_is_read_only_bounded_and_unknown_workflow_rejected(self):
        response = self.client.get(self.url)
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json['schema'], 'aegisrt.presentation-monitor/v1')
        self.assertEqual(response.headers['Cache-Control'], 'no-store')
        self.assertEqual(self.client.post(self.url, json={}).status_code, 405)
        self.assertEqual(self.client.get('/api/aegisrt/workflows/missing/monitor').status_code, 404)
        self.assertNotIn('private-secret-value', response.get_data(as_text=True))

    def test_foreign_task_specs_do_not_expose_fields_or_dependencies(self):
        self.specs[0]['task']['workflow_id'] = 'other'
        save_json(self.folder / 'task-snapshot.json', {'tasks': self.specs})
        task = self.monitor.workflow(self.service, self.workflow)['tasks'][0]
        self.assertIsNone(task['resources']['cpu_milli'])
        self.assertIsNone(task['agent_id'])
        self.assertEqual(task['dependencies'], [])


if __name__ == '__main__':
    unittest.main()
