import sys,json,unittest
from pathlib import Path
from unittest.mock import patch
B=Path(__file__).resolve().parent;sys.path[:0]=[str(B.parent),str(B)]
import test_reporting_integration as fixture

class ReportBudgetTests(unittest.TestCase):
    setUp = fixture.ReportingIntegrationTests.setUp
    def run_scripted(self,limit,reject_reviews):
        self.payload['max_model_requests']=limit;calls=[];reviews=0
        def complete(_usage,_role,_system,text):
            nonlocal reviews
            supplied=json.loads(text);calls.append(supplied)
            if supplied.get('task')=='paragraph-evidence-review':
                reviews+=1
                return json.dumps(fixture.model_review(supplied,[supplied['paragraphs'][0]['path']] if reviews<=reject_reviews else []),ensure_ascii=False)
            return fixture.model_response(supplied)
        with patch.object(fixture.adapter,'complete_text',side_effect=complete):
            try:result=fixture.adapter.downstream_role(self.args,self.request,self.payload,self.dependencies,self.target,None)
            except fixture.adapter.AdapterError:result=None
        return result,calls
    def test_fourth_candidate_requires_eighth_review(self):
        result,calls=self.run_scripted(8,3)
        self.assertIsNotNone(result);self.assertEqual(len(calls),8)
        self.assertEqual(calls[-1]['task'],'paragraph-evidence-review')
    def test_odd_remaining_budget_never_generates_unreviewable_candidate(self):
        result,calls=self.run_scripted(5,99)
        self.assertIsNone(result);self.assertEqual(len(calls),4);self.assertFalse((self.target/'report.html').exists())

if __name__=='__main__':unittest.main()
