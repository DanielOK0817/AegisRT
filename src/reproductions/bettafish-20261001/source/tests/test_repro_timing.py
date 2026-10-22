import json, os, sys, tempfile, unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
from utils.research_run import with_research_run
from utils.llm_usage import current_run_id

class TimingTests(unittest.TestCase):
 def test_status_and_context(self):
  for result in (True, False, None):
   with tempfile.TemporaryDirectory() as folder, patch.dict(os.environ, RESEARCH_RUN_DIR=folder, BETTAFISH_RUN_ID='benchmark'), patch.dict(sys.modules, streamlit=SimpleNamespace(query_params={'run_id':'browser'})):
    @with_research_run
    def run():
     self.assertEqual(current_run_id.get(),'browser')
     return result
    self.assertIs(run(),result)
    self.assertIsNone(current_run_id.get())
    row=json.loads(next(Path(folder).glob('*.json')).read_text())
    self.assertEqual(row['run_id'],'benchmark')
    self.assertEqual(row['status'], 'completed' if result is True else 'error' if result is False else 'finished')
    self.assertGreaterEqual(row['elapsed_seconds'],0)
 def test_exception(self):
  with tempfile.TemporaryDirectory() as folder, patch.dict(os.environ, RESEARCH_RUN_DIR=folder), patch.dict(sys.modules, streamlit=SimpleNamespace(query_params={'run_id':'browser'})):
   @with_research_run
   def run(): raise ValueError('test')
   with self.assertRaises(ValueError): run()
   self.assertIsNone(current_run_id.get())
   self.assertEqual(json.loads(next(Path(folder).glob('*.json')).read_text())['status'],'error')
