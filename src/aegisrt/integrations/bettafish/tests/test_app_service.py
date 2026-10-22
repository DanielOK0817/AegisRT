"""Exercise the real HTTP entry without external services or model requests."""
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
from app_service import EntryError, WorkflowService, install_aegisrt_entry, save_json, submit_http_request
from sample_inputs import freeze_sample_values


class EntryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.integration = Path(__file__).resolve().parents[1]
        for name in ('app.py', 'aegisrt', 'application.env'):
            (self.root / name).write_text('test', encoding='utf-8')
        env = {'BETTAFISH_AEGISRT_BINARY': str(self.root / 'aegisrt'),
               'BETTAFISH_AEGISRT_ENV_FILE': str(self.root / 'application.env'),
               'BETTAFISH_AEGISRT_RUN_ROOT': str(self.root / 'runs'),
               'BETTAFISH_AEGISRT_SAMPLE_ROOT': str(self.root / 'samples')}
        self.app = Flask(__name__)
        with patch.dict(os.environ, env):
            self.service = install_aegisrt_entry(self.app, self.root)
        self.service.launch = False
        self.app.add_url_rule('/api/search', view_func=lambda: submit_http_request(self.service), methods=['POST'])
        self.client = self.app.test_client()
        self.topic = '测试案例'
        self.comments = [{'comment_id': str(i), 'content': '真实样本内容' + str(i),
                          'source_url': 'https://example.org/video/1'} for i in (1, 2)]
        self.provenance = {'topic': self.topic, 'source_urls': ['https://example.org/video/1']}
        self.sample = {'topic': self.topic, 'comments': self.comments, 'provenance': self.provenance}

    def submit(self):
        result = self.client.post('/api/search', json={'query': self.topic, 'sample': self.sample})
        self.assertEqual(result.status_code, 202, result.json)
        return result.json['workflow_id']

    def finish(self, workflow_id):
        folder = self.service.job_dir(workflow_id)
        metadata = json.loads((folder / 'entry.json').read_text())
        metadata['status'] = 'FAILED'
        save_json(folder / 'entry.json', metadata)

    def report(self, workflow_id):
        folder = self.service.job_dir(workflow_id)
        role = folder / (workflow_id + '-report')
        role.mkdir()
        report = role / 'report.html'
        report.write_text('<html><p>Test report</p></html>')
        manifest = {'schema': 'bettafish.task-result/v1', 'workflow_id': workflow_id,
                    'task_id': workflow_id + '-report', 'role': 'report', 'mode': 'live',
                    'files': {'report': {'path': str(report), 'sha256': hashlib.sha256(report.read_bytes()).hexdigest()}}}
        save_json(role / 'result.json', manifest)
        save_json(folder / 'summary.json', {'workflow_id': workflow_id, 'status': 'completed'})
        return report, manifest

    def test_upload_freezes_fresh_sample_and_blocks_concurrent_submission(self):
        workflow = self.submit()
        folder = self.service.job_dir(workflow)
        self.assertTrue((folder / 'inputs' / 'manifest.json').is_file())
        self.assertNotIn('--resume-dir', self.service.command(workflow))
        self.assertIn('40,40,64,1,5', self.service.command(workflow))
        self.assertIn('20,20,32', self.service.command(workflow))
        self.assertNotIn('--fixed-topic-search', self.service.command(workflow))
        response = self.client.post('/api/search', json={'query': self.topic, 'sample': self.sample})
        self.assertEqual(response.status_code, 409)
        self.finish(workflow)
        self.assertNotEqual(workflow, self.submit())

    def test_new_request_budget_does_not_replace_a_completed_case_historical_limit(self):
        configuration = self.service.configuration()
        self.assertEqual(configuration['max_model_requests'], 223)
        self.assertEqual(configuration['report_max_model_requests'], 5)
        workflow = self.submit()
        folder = self.service.job_dir(workflow)
        metadata = json.loads((folder / 'entry.json').read_text())
        self.assertEqual(metadata['model_request_limit'], 223)
        metadata['model_request_limit'] = 22
        save_json(folder / 'entry.json', metadata)
        self.report(workflow)
        historical = self.service.status(workflow)
        self.assertEqual(historical['status'], 'SUCCEEDED')
        self.assertEqual(historical['model_requests']['limit'], 22)
        self.assertEqual(self.service.configuration()['max_model_requests'], 223)

    def test_catalog_and_selected_sample(self):
        freeze_sample_values(self.topic, self.comments, self.provenance, self.root / 'samples' / 'case1')
        self.assertEqual(self.service.catalog()[0]['comment_count'], 2)
        response = self.client.post('/api/search', json={'query': self.topic, 'sample_id': 'case1'})
        self.assertEqual(response.status_code, 202)

    def test_invalid_sample_and_topic_rejected_before_job_creation(self):
        for body in ({'query': 'different', 'sample': self.sample},
                     {'query': self.topic, 'sample_id': '../outside'},
                     {'query': self.topic, 'sample': self.sample, 'sample_id': 'case1'},
                     {'query': self.topic, 'sample': {**self.sample, 'comments': self.comments * 2}}):
            self.assertEqual(self.client.post('/api/search', json=body).status_code, 400)
        self.assertEqual(list(self.service.run_root.iterdir()), [])

    def test_http_size_origin_and_type(self):
        self.assertEqual(self.client.post('/api/search', json={}, headers={'Origin': 'https://evil.example'}).status_code, 403)
        self.assertEqual(self.client.post('/api/search', data='{}').status_code, 415)
        self.assertEqual(self.client.post('/api/search', data='x' * 1000001, content_type='application/json').status_code, 413)
        self.assertEqual(self.client.post('/api/search', data='{', content_type='application/json').status_code, 400)

    def test_status_is_derived_from_matching_tasks_and_actual_budget(self):
        workflow = self.submit()
        folder = self.service.job_dir(workflow)
        save_json(folder / 'task-snapshot.json', {'tasks': [
            {'task': {'id': workflow + '-query', 'agent_id': 'bettafish-query'}, 'status': 'SUCCEEDED'},
            {'task': {'id': workflow + '-query-feedback', 'agent_id': 'bettafish-query'}, 'status': 'RUNNING'},
            {'task': {'id': 'different-media', 'agent_id': 'bettafish-media'}, 'status': 'SUCCEEDED'}]})
        role = folder / (workflow + '-query')
        role.mkdir()
        save_json(role / 'model-budget.json', {'attempts': 2})
        feedback = folder / (workflow + '-query-feedback')
        feedback.mkdir()
        save_json(feedback / 'model-budget.json', {'attempts': 3})
        result = self.client.get('/api/aegisrt/workflows/' + workflow).json
        self.assertEqual(result['tasks'][0]['status'], 'SUCCEEDED')
        self.assertEqual(result['tasks'][1]['status'], 'WAITING')
        self.assertEqual(result['model_requests']['attempts'], 5)
        self.assertEqual(len(result['tasks']), 9)
        self.assertEqual(result['tasks'][4]['status'], 'RUNNING')
        self.assertIsNone(result['report_url'])
        save_json(folder / 'summary.json', {'workflow_id': 'different', 'status': 'error',
                  'model_usage': {'provider_attempts': 9}})
        self.assertEqual(self.service.status(workflow)['model_requests']['attempts'], 5)
        save_json(folder / 'summary.json', {'workflow_id': workflow, 'status': 'error',
                  'model_usage': {'provider_attempts': 9}})
        self.assertEqual(self.service.status(workflow)['model_requests']['attempts'], 9)

    def test_cancel_requests_owned_marker_and_interrupt_is_not_resume(self):
        workflow = self.submit()
        response = self.client.post('/api/aegisrt/workflows/' + workflow + '/cancel', json={})
        self.assertEqual(response.json['status'], 'CANCELING')
        self.assertTrue((self.service.job_dir(workflow) / 'cancel.requested').exists())
        self.service.launch = True
        self.assertEqual(self.service.status(workflow)['status'], 'CANCELED')
        self.finish(workflow)
        self.service.launch = False
        other = self.submit()
        self.service.launch = True
        self.assertEqual(self.service.status(other)['status'], 'INTERRUPTED')

    def test_report_verified_and_active_content_disabled(self):
        workflow = self.submit()
        url = '/api/aegisrt/workflows/' + workflow + '/report'
        self.assertEqual(self.client.get(url).status_code, 409)
        report, _manifest = self.report(workflow)
        response = self.client.get(url)
        self.assertEqual(response.status_code, 200)
        self.assertIn("script-src 'none'", response.headers['Content-Security-Policy'])
        self.assertEqual(self.service.status(workflow)['status'], 'SUCCEEDED')
        report.write_text('tampered')
        self.assertEqual(self.client.get(url).status_code, 409)
        self.assertEqual(self.service.status(workflow)['status'], 'FAILED')

    def test_report_rejects_foreign_workflow_and_path_escape(self):
        workflow = self.submit()
        report, manifest = self.report(workflow)
        manifest['workflow_id'] = 'other'
        save_json(report.parent / 'result.json', manifest)
        with self.assertRaises(EntryError):
            self.service.report_path(workflow)
        manifest['workflow_id'] = workflow
        outside = self.root / 'report.html'
        outside.write_bytes(report.read_bytes())
        manifest['files']['report']['path'] = str(outside)
        save_json(report.parent / 'result.json', manifest)
        with self.assertRaises(EntryError):
            self.service.report_path(workflow)


if __name__ == '__main__':
    unittest.main()
