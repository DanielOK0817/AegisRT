import json
import os
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace as NS
from unittest.mock import patch
from concurrent.futures import ThreadPoolExecutor
import uuid

from utils.llm_usage import recorded_completion
from utils.research_run import claim_run


class FakeStream:
    def __init__(self, chunks, error=None):
        self.chunks, self.error, self.closed = chunks, error, False

    def __iter__(self):
        yield from self.chunks
        if self.error:
            raise self.error

    def close(self):
        self.closed = True


class UsageTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.env = patch.dict(os.environ, LLM_USAGE_DIR=self.tmp.name, BETTAFISH_RUN_ID='test-run')
        self.env.start()
        self.addCleanup(self.env.stop)
        self.addCleanup(self.tmp.cleanup)

    def client(self, response=None, error=None):
        def create(**kwargs):
            self.kwargs = kwargs
            if error:
                raise error
            return response
        return NS(base_url='https://example.invalid/v1', chat=NS(completions=NS(create=create)))

    def rows(self):
        return [json.loads(p.read_text()) for p in Path(self.tmp.name).glob('*.json')]

    def test_nonstream_preserves_result_and_never_records_prompt(self):
        response = NS(model='test', usage=NS(prompt_tokens=20, completion_tokens=5,
            total_tokens=25, prompt_tokens_details=NS(cached_tokens=7)))
        actual = recorded_completion(self.client(response), 'query', model='test',
            messages=[{'role':'user', 'content':'PRIVATE-PROMPT'}])
        self.assertIs(actual, response)
        row = self.rows()[0]
        self.assertEqual((row['input_tokens'], row['cache_hit_tokens'], row['cache_miss_tokens']), (20,7,13))
        self.assertNotIn('PRIVATE-PROMPT', json.dumps(row))
        self.assertEqual(row['run_id'], 'test-run')

    def test_stream_usage_only_terminal_chunk(self):
        chunks = [NS(model='test', choices=[1], usage=None),
                  NS(model='test', choices=[], usage=NS(prompt_tokens=10, completion_tokens=3,total_tokens=13))]
        stream = FakeStream(chunks)
        self.assertEqual(list(recorded_completion(self.client(stream), 'media', model='test', stream=True)), chunks)
        self.assertTrue(self.kwargs['stream_options']['include_usage'])
        self.assertTrue(stream.closed)
        self.assertEqual(self.rows()[0]['total_tokens'],13)

    def test_missing_usage_is_unknown_not_zero(self):
        recorded_completion(self.client(NS(model='test', usage=None)), 'forum', model='test')
        row=self.rows()[0]
        self.assertFalse(row['usage_present'])
        self.assertIsNone(row['total_tokens'])

    def test_failed_attempt_is_separate_and_redacts_exception(self):
        for _ in range(2):
            with self.assertRaises(RuntimeError):
                recorded_completion(self.client(error=RuntimeError('SECRET')), 'report', model='test')
        self.assertEqual(len(self.rows()), 2)
        self.assertNotIn('SECRET', json.dumps(self.rows()))
        self.assertTrue(all(x['status']=='error' for x in self.rows()))

    def test_partial_stream_failure_keeps_unknown_usage(self):
        stream=FakeStream([NS(model='test',usage=None)], RuntimeError('SECRET'))
        with self.assertRaises(RuntimeError):
            list(recorded_completion(self.client(stream),'query',model='test',stream=True))
        self.assertTrue(stream.closed)
        self.assertEqual(self.rows()[0]['status'],'error')
        self.assertIsNone(self.rows()[0]['total_tokens'])

    def test_cancelled_stream(self):
        stream=FakeStream([NS(model='test',usage=None), NS(model='test',usage=None)])
        result=recorded_completion(self.client(stream),'query',model='test',stream=True)
        next(result)
        result.close()
        self.assertTrue(stream.closed)
        self.assertEqual(self.rows()[0]['status'],'cancelled')

    def test_provider_total_mismatch_is_preserved(self):
        recorded_completion(self.client(NS(model='test',usage=NS(prompt_tokens=8,
            completion_tokens=4,total_tokens=71))), 'insight',model='test')
        row=self.rows()[0]
        self.assertEqual(row['total_tokens'],71)
        self.assertFalse(row['token_sum_matches'])

    def test_duplicate_dispatch_across_threads_is_rejected(self):
        run_id=str(uuid.uuid4())
        with patch.dict(os.environ,RESEARCH_RUN_DIR=self.tmp.name):
            with ThreadPoolExecutor(max_workers=8) as pool:
                accepted=list(pool.map(lambda _:claim_run(run_id,'query'),range(20)))
            self.assertEqual(sum(accepted),1)
            self.assertFalse(claim_run(run_id,'query'))
            self.assertTrue(claim_run(run_id,'media'))
            self.assertTrue(claim_run(str(uuid.uuid4()),'query'))
            self.assertFalse(claim_run(None,'query'))
            self.assertFalse(claim_run('../../bad','query'))

    def test_run_id_on_installed_legacy_streamlit(self):
        from utils.research_run import streamlit_run_id
        legacy = NS(experimental_get_query_params=lambda: {'run_id':['run-123']})
        self.assertEqual(streamlit_run_id(legacy), 'run-123')
        self.assertIsNone(streamlit_run_id(NS(experimental_get_query_params=lambda: {})))

    def test_run_id_on_modern_streamlit(self):
        from utils.research_run import streamlit_run_id
        self.assertEqual(streamlit_run_id(NS(query_params={'run_id':'run-123'})), 'run-123')


if __name__ == '__main__':
    unittest.main()
