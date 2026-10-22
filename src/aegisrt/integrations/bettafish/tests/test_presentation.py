"""No-model tests for the presentation output boundary."""
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
from app_service import install_aegisrt_entry, save_json, submit_http_request
from presentation_api import entry_page


class PresentationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in ('app.py', 'binary', 'app.env'):
            (self.root / name).write_text('test', encoding='utf-8')
        env = {'BETTAFISH_AEGISRT_BINARY': str(self.root/'binary'),
               'BETTAFISH_AEGISRT_ENV_FILE': str(self.root/'app.env'),
               'BETTAFISH_AEGISRT_RUN_ROOT': str(self.root/'runs'),
               'BETTAFISH_AEGISRT_SAMPLE_ROOT': str(self.root/'samples')}
        self.app = Flask(__name__)
        with patch.dict(os.environ, env):
            self.service = install_aegisrt_entry(self.app, self.root)
        self.service.launch = False
        self.app.add_url_rule('/', view_func=entry_page)
        self.app.add_url_rule('/api/search', view_func=lambda: submit_http_request(self.service), methods=['POST'])
        self.client = self.app.test_client()
        sample = {'topic':'接口测试样本', 'comments':[{'comment_id':str(i),'content':'测试内容','source_url':'https://example.org/test'} for i in (1,2)],
                  'provenance':{'topic':'接口测试样本'}}
        response = self.client.post('/api/search', json={'query':sample['topic'],'sample':sample})
        self.assertEqual(response.status_code, 202)
        self.workflow = response.json['workflow_id']
        self.folder = self.service.job_dir(self.workflow)
        self.url = '/api/aegisrt/workflows/'+self.workflow+'/content'

    def checkpoint(self, text='研究正文', workflow=None):
        directory = self.folder/(self.workflow+'-query')
        directory.mkdir(exist_ok=True)
        value = {'workflow_id':workflow or self.workflow,'task_id':self.workflow+'-query','role':'query','mode':'live',
                 'state':{'paragraphs':[{'title':'研究角度','research':{'latest_summary':text}}]}}
        save_json(directory/'checkpoint.json', value)
        return directory

    def test_checkpoint_updates_and_no_private_fields(self):
        directory = self.checkpoint()
        (directory/'agent.log').write_text('private credentials must never be read')
        body=self.client.get(self.url).json
        self.assertEqual(body['blocks'][0]['text'],'研究正文')
        self.checkpoint('更新后的研究正文')
        self.assertEqual(self.client.get(self.url).json['blocks'][0]['text'],'更新后的研究正文')
        self.assertNotIn('private credentials',json.dumps(body))

    def test_identity_and_partial_json_are_not_presented(self):
        directory=self.checkpoint(workflow='other')
        self.assertEqual(self.client.get(self.url).json['blocks'],[])
        (directory/'checkpoint.json').write_text('{"partial":')
        self.assertEqual(self.client.get(self.url).json['blocks'],[])

    def test_known_secrets_bearer_and_query_tokens_are_redacted(self):
        (self.root/'app.env').write_text('TEST_API_KEY=test-secret-12345\n',encoding='utf-8')
        self.checkpoint('test-secret-12345 Bearer abcdef https://example.org/?token=private')
        text=self.client.get(self.url).json['blocks'][0]['text']
        for secret in ('test-secret-12345','abcdef','private'):
            self.assertNotIn(secret,text)
        self.assertIn('[redacted]',text)

    def test_forum_hash_and_identity_required(self):
        directory=self.folder/(self.workflow+'-forum')
        directory.mkdir()
        report=directory/'forum.md'
        report.write_text('讨论正文',encoding='utf-8')
        manifest={'schema':'bettafish.task-result/v1','workflow_id':self.workflow,'task_id':self.workflow+'-forum','role':'forum','mode':'live',
                  'files':{'report':{'path':str(report),'sha256':hashlib.sha256(report.read_bytes()).hexdigest()}}}
        save_json(directory/'result.json',manifest)
        self.assertEqual(self.client.get(self.url).json['blocks'][0]['text'],'讨论正文')
        report.write_text('changed')
        self.assertEqual(self.client.get(self.url).json['blocks'],[])

    def test_path_escape_is_rejected(self):
        directory=self.folder/(self.workflow+'-forum');directory.mkdir()
        outside=self.root/'forum.md';outside.write_text('outside')
        manifest={'schema':'bettafish.task-result/v1','workflow_id':self.workflow,'task_id':self.workflow+'-forum','role':'forum','mode':'live',
                  'files':{'report':{'path':str(outside),'sha256':hashlib.sha256(outside.read_bytes()).hexdigest()}}}
        save_json(directory/'result.json',manifest)
        self.assertEqual(self.client.get(self.url).json['blocks'],[])
        self.assertEqual(self.client.get('/api/aegisrt/workflows/invalid/content').status_code,404)

    def test_failure_keeps_content_and_does_not_publish_report(self):
        self.checkpoint()
        meta=json.loads((self.folder/'entry.json').read_text(encoding='utf-8'));meta['status']='FAILED';save_json(self.folder/'entry.json',meta)
        response=self.client.get(self.url)
        self.assertEqual(response.json['blocks'][0]['text'],'研究正文')
        self.assertIsNone(response.json['report_url'])
        self.assertEqual(response.headers['Cache-Control'],'no-store')

    def failure(self, reason='model request budget exhausted', workflow=None):
        directory=self.folder/(self.workflow+'-media-feedback')
        directory.mkdir(exist_ok=True)
        save_json(directory/'state.json',{'workflow_id':workflow or self.workflow,
                  'task_id':self.workflow+'-media-feedback','role':'media','mode':'live',
                  'status':'failed','error_reason':reason,'error_type':'ModelCallStopped',
                  'model_requests':{'attempts':4,'limit':4,'private':'private credentials'}})
        meta=json.loads((self.folder/'entry.json').read_text(encoding='utf-8'));meta['status']='FAILED';save_json(self.folder/'entry.json',meta)
        save_json(self.folder/'task-snapshot.json',{'tasks':[
            {'task':{'id':self.workflow+'-media-feedback'},'status':'FAILED'}]})
        return directory

    def test_failed_task_has_classified_budget_reason_only(self):
        directory=self.failure()
        (directory/'agent.log').write_text('private credentials')
        body=self.client.get(self.url).json
        failure=body['task_failures'][0]
        self.assertEqual(failure['stage'],'media-feedback')
        self.assertEqual(failure['code'],'model_budget_exhausted')
        self.assertEqual(failure['model_requests'],{'attempts':4,'limit':4})
        self.assertNotIn('private credentials',json.dumps(body))
        self.assertNotIn(str(directory),json.dumps(body))

    def test_failure_identity_and_exception_text_are_not_exposed(self):
        self.failure(workflow='other')
        self.assertEqual(self.client.get(self.url).json['task_failures'],[])
        self.failure(reason='credential=private-secret /private/path/provider.log')
        body=self.client.get(self.url).json
        self.assertEqual(body['task_failures'][0]['code'],'task_failed')
        self.assertNotIn('private-secret',json.dumps(body))
        self.assertNotIn('/private/path',json.dumps(body))

    def test_failure_requires_failed_runtime_task(self):
        self.failure()
        save_json(self.folder/'task-snapshot.json',{'tasks':[
            {'task':{'id':self.workflow+'-media-feedback'},'status':'SUCCEEDED'}]})
        self.assertEqual(self.client.get(self.url).json['task_failures'],[])

    def test_required_research_reservation_is_not_reported_as_exhausted(self):
        self.failure(reason='model request budget exhausted: capacity reserved for required research steps')
        failure=self.client.get(self.url).json['task_failures'][0]
        self.assertEqual(failure['code'],'research_capacity_insufficient')
        self.assertIn('恢复容量不足',failure['message'])
        self.assertNotIn('额度已用尽',failure['message'])

    def provider_failure(self):
        directory=self.failure(reason='provider request failed')
        state=json.loads((directory/'state.json').read_text(encoding='utf-8'))
        state['model_requests']={'attempts':4,'limit':5,'provider_attempts':[
            {'attempt':4,'agent':'media','status':'error','retryable':True,'delivered_chunks':0,
             'failure_stage':'request','retry_blocked_reason':'reserved_for_required_research_calls',
             'remaining_capacity':1,'required_calls_after':1,
             'private':'private-secret /private/path/provider.log'}]}
        save_json(directory/'state.json',state)
        return directory,state

    def test_verified_retry_guard_has_fixed_reason_without_private_metadata(self):
        self.provider_failure()
        body=self.client.get(self.url).json
        failure=body['task_failures'][0]
        self.assertEqual(failure['code'],'recovery_capacity_insufficient')
        self.assertIn('模型服务请求未完成',failure['message'])
        self.assertIn('恢复容量不足',failure['message'])
        self.assertEqual(failure['model_requests'],{'attempts':4,'limit':5})
        self.assertNotIn('private-secret',json.dumps(body))
        self.assertNotIn('/private/path',json.dumps(body))
        self.assertNotIn('provider_attempts',json.dumps(body))

    def test_normal_or_unverified_provider_failure_is_not_budget_failure(self):
        cases=[{}, {'retry_blocked_reason':'other'}, {'agent':'query'}, {'attempt':3},
               {'status':'completed'}, {'remaining_capacity':2}, {'required_calls_after':False},
               {'remaining_capacity':'private-secret'}, {'delivered_chunks':1}]
        for override in cases:
            with self.subTest(override=override):
                directory,state=self.provider_failure()
                record=state['model_requests']['provider_attempts'][0]
                if override:
                    record.update(override)
                else:
                    record.pop('retry_blocked_reason')
                save_json(directory/'state.json',state)
                body=self.client.get(self.url).json
                self.assertEqual(body['task_failures'][0]['code'],'provider_failed')
                self.assertNotIn('恢复容量不足',body['task_failures'][0]['message'])
                self.assertNotIn('private-secret',json.dumps(body))

    def test_ui_asset_allowlist_and_no_simulation_controls(self):
        with self.client.get('/') as response:
            body=response.get_data(as_text=True)
        self.assertIn('AegisRT',body)
        self.assertIn('/api/aegisrt/ui/runtime.js',body)
        self.assertIn('/api/aegisrt/ui/runtime.css',body)
        self.assertNotIn('模拟',body)
        self.assertNotIn('id="timeline"',body)
        for name in ('runtime.js','runtime.css'):
            with self.client.get('/api/aegisrt/ui/'+name) as response:
                self.assertEqual(response.status_code,200)
        for name in ('style.css', 'live.js', 'lucide.min.js'):
            self.assertEqual(self.client.get('/api/aegisrt/ui/'+name).status_code,404)
        self.assertEqual(self.client.get('/api/aegisrt/ui/app.env').status_code,404)
        self.assertEqual(self.client.get('/api/aegisrt/ui/App.tsx').status_code,404)

    def test_history_and_bounds(self):
        self.checkpoint('x'*25000)
        block=self.client.get(self.url).json['blocks'][0]
        self.assertEqual(len(block['text']),18000)
        self.assertTrue(block['truncated'])
        rows=self.client.get('/api/aegisrt/workflows').json['workflows']
        self.assertEqual(rows[0]['workflow_id'],self.workflow)


if __name__=='__main__':
    unittest.main()
