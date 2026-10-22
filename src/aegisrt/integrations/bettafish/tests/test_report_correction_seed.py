import copy
import json
from pathlib import Path
import unittest
from unittest.mock import patch

import test_reporting_integration as fixtures
from test_reporting_integration import adapter, model_report, model_response
import report_seed


class CorrectionSeedTests(unittest.TestCase):
    def setUp(self):
        fixtures.ReportingIntegrationTests.setUp(self)
        self.source_task=self.request['workflow_id']+'-report-recovery-1'
        self.run=self.root/self.request['workflow_id']
        self.source=self.run/self.source_task
        self.source.mkdir(parents=True)
        def failed_candidate(_usage,_role,_system,text):
            value=model_report(json.loads(text))
            value['sections'][5]['paragraphs'][0]['text']='查询研究与媒体研究建议核查来源。'
            return json.dumps(value,ensure_ascii=False)
        with patch.object(adapter,'complete_text',side_effect=failed_candidate), self.assertRaises(adapter.AdapterError):
            adapter.downstream_role(self.args,self.request,self.payload,self.dependencies,self.source,None)
        adapter.atomic_json(self.source/'state.json',{'workflow_id':self.run.name,'task_id':self.source_task,
            'role':'report','mode':'live','topic':self.topic,'status':'running'})
        adapter.atomic_json(self.source/'model-budget.json',{'limit':5,'attempts':2})
        adapter.atomic_json(self.run/'.report-recovery-1/interruption.json',{'task_id':self.source_task,
            'reason':'execution_node_interrupted','automatic_restart':False})
        self.seed=report_seed.load_seed(self.run,self.source_task,self.topic,self.run.name+'-report-recovery-2')

    def test_saved_candidate_becomes_correction_input_then_requires_new_review(self):
        before={p.name:p.read_bytes() for p in self.source.iterdir() if p.is_file()}
        self.args.report_correction_seed=self.seed
        captured=[]
        def completion(_usage,_role,_system,text):
            value=json.loads(text);captured.append(value)
            return model_response(value)
        with patch.object(adapter,'complete_text',side_effect=completion):
            result=adapter.downstream_role(self.args,self.request,self.payload,self.dependencies,self.target,None)
        self.assertEqual(len(captured),2)
        self.assertEqual(captured[1]['task'],'paragraph-evidence-review')
        self.assertIn('previous_candidate',captured[0]['correction'])
        self.assertIn('research_attribution_missing',{r['code'] for r in captured[0]['correction']['issues']})
        self.assertEqual(before,{p.name:p.read_bytes() for p in self.source.iterdir() if p.is_file()})
        self.assertTrue(result['comprehensive_report'])

    def test_modified_saved_candidate_is_rejected_before_model_call(self):
        self.args.report_correction_seed=self.seed
        record=self.seed['source_files'][f"model-report-response-{self.seed['response_attempt']}.txt"]
        with Path(record['path']).open('a',encoding='utf-8') as stream:stream.write(' ')
        with patch.object(adapter,'complete_text') as provider, self.assertRaisesRegex(ValueError,'bytes changed'):
            adapter.downstream_role(self.args,self.request,self.payload,self.dependencies,self.target,None)
        provider.assert_not_called()

    def test_running_report_without_confirmed_interruption_cannot_seed(self):
        (self.run/'.report-recovery-1/interruption.json').unlink()
        with self.assertRaises(FileNotFoundError):
            report_seed.load_seed(self.run,self.source_task,self.topic,self.run.name+'-report-recovery-2')

    def test_different_topic_and_workflow_are_rejected(self):
        with self.assertRaisesRegex(ValueError,'identity'):
            report_seed.load_seed(self.run,self.source_task,'different topic',self.run.name+'-report-recovery-2')
        with self.assertRaises(ValueError):
            report_seed.load_seed(self.run,'other-report',self.topic,self.run.name+'-report-recovery-2')


if __name__=='__main__':unittest.main()
