"""Truncated report responses remain rejected but may use existing call capacity."""
import json
from contextlib import contextmanager
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import test_reporting_integration as reporting_tests
adapter = reporting_tests.adapter
model_response = reporting_tests.model_response

class ReportOutputRecoveryTests(unittest.TestCase):
    setUp = reporting_tests.ReportingIntegrationTests.setUp

    def provider(self, usage, role, system, text):
        return usage.recorded_completion(SimpleNamespace(max_retries=0), role, messages=[{'role':'user','content':text}]).choices[0].message.content

    def test_only_report_length_is_eligible_and_partial_output_is_never_accepted(self):
        for role, reason, retry in [('report','length',True),('query','length',False),('report','content_filter',False)]:
            budget=adapter.ModelBudget(self.root/f'{role}-{reason}.json','input',5,300)
            response=SimpleNamespace(choices=[SimpleNamespace(finish_reason=reason,message=SimpleNamespace(content='partial'))])
            with self.subTest(role=role,reason=reason), self.assertRaises(adapter.ModelCallStopped) as failure:
                budget.wrap(lambda *a,**kw:response)(SimpleNamespace(max_retries=0),role)
            self.assertEqual(failure.exception.retryable,retry)
            if retry:self.assertEqual(failure.exception.error_type,'ModelOutputLimitExceeded')
            self.assertEqual(budget.data['attempts'],1)

    def test_review_length_recovers_with_same_candidate_and_full_coverage(self):
        self.payload['max_model_requests']=5
        calls=[]
        def original(client,role,**kwargs):
            value=json.loads(kwargs['messages'][0]['content']);calls.append(value)
            return SimpleNamespace(choices=[SimpleNamespace(finish_reason='length' if len(calls)==2 else 'stop',message=SimpleNamespace(content='partial' if len(calls)==2 else model_response(value)))])
        budget=adapter.ModelBudget(self.target/'budget.json','input',5,300)
        usage=SimpleNamespace(recorded_completion=budget.wrap(original))
        with patch.object(adapter,'complete_text',side_effect=self.provider):
            result=adapter.downstream_role(self.args,self.request,self.payload,self.dependencies,self.target,usage)
        self.assertEqual(len(calls),3)
        self.assertEqual(calls[1]['paragraphs'],calls[2]['paragraphs'])
        self.assertEqual(calls[1]['evidence'],calls[2]['evidence'])
        self.assertIn('format_correction',calls[2])
        self.assertFalse((self.target/'model-report-review-response-2.txt').exists())
        self.assertTrue(Path(result['files']['report']['path']).is_file())

    def test_request_deadline_can_retry_same_review_inside_report_budget(self):
        self.payload['max_model_requests']=3
        calls=[];guards=[]
        class APITimeoutError(Exception):pass
        @contextmanager
        def deadline(seconds,**options):
            guards.append(options.get('request_scope',False))
            if len(guards)==2:
                error=adapter.ModelRequestDeadlineExceeded if options.get('request_scope') else adapter.ModelDeadlineExceeded
                try:raise error('synthetic deadline')
                except TimeoutError as exc:raise APITimeoutError('synthetic SDK wrapper') from exc
            yield lambda:None
        def original(client,role,**kwargs):
            value=json.loads(kwargs['messages'][0]['content']);calls.append(value)
            return SimpleNamespace(choices=[SimpleNamespace(finish_reason='stop',message=SimpleNamespace(content=model_response(value)))])
        budget=adapter.ModelBudget(self.target/'budget.json','input',3,300)
        usage=SimpleNamespace(recorded_completion=budget.wrap(original))
        with patch.object(adapter,'complete_text',side_effect=self.provider),patch.object(adapter,'model_deadline',deadline):
            result=adapter.downstream_role(self.args,self.request,self.payload,self.dependencies,self.target,usage)
        self.assertEqual(budget.data['attempts'],3)
        self.assertEqual(budget.data['provider_attempts'][1]['deadline_origin'],'request')
        self.assertEqual(budget.data['provider_retries'],[],'Report loop owns retry; no hidden provider retry')
        first=adapter.read_json(self.target/'report-review-input-2.json')
        second=adapter.read_json(self.target/'report-review-input-3.json')
        self.assertEqual(first,second)
        self.assertTrue(Path(result['files']['report']['path']).is_file())

    def test_enclosing_deadline_still_stops_report_without_retry(self):
        self.payload['max_model_requests']=3
        @contextmanager
        def deadline(seconds,**options):
            raise adapter.ModelDeadlineExceeded('synthetic enclosing deadline')
            yield
        budget=adapter.ModelBudget(self.target/'budget.json','input',3,300)
        usage=SimpleNamespace(recorded_completion=budget.wrap(lambda *args,**kwargs:None))
        with patch.object(adapter,'complete_text',side_effect=self.provider),patch.object(adapter,'model_deadline',deadline):
            with self.assertRaises(adapter.ModelCallStopped) as caught:
                adapter.downstream_role(self.args,self.request,self.payload,self.dependencies,self.target,usage)
        self.assertFalse(caught.exception.retryable)
        self.assertEqual(budget.data['attempts'],1)
        self.assertFalse((self.target/'report.html').exists())

    def test_repeated_truncation_preserves_review_slot_without_report(self):
        self.payload['max_model_requests']=5
        def original(client,role,**kwargs):
            return SimpleNamespace(choices=[SimpleNamespace(finish_reason='length',message=SimpleNamespace(content='partial'))])
        budget=adapter.ModelBudget(self.target/'budget.json','input',5,300)
        usage=SimpleNamespace(recorded_completion=budget.wrap(original))
        with patch.object(adapter,'complete_text',side_effect=self.provider), self.assertRaises(adapter.AdapterError):
            adapter.downstream_role(self.args,self.request,self.payload,self.dependencies,self.target,usage)
        self.assertEqual(budget.data['attempts'],4)
        self.assertFalse((self.target/'report.html').exists())

if __name__=='__main__':unittest.main()
