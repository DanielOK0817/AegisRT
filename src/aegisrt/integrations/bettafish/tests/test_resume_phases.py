"""A checkpoint for initial research must never bypass the feedback tasks."""
import argparse
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location('resume_phase_adapter', Path(__file__).resolve().parents[1] / 'adapter.py')
adapter = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(adapter)


class ResumePhaseTests(unittest.TestCase):
    def test_initial_research_checkpoint_is_not_loaded_by_feedback(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            checkpoint = root / 'query.json'
            adapter.atomic_json(checkpoint, {'workflow_id': 'previous', 'role': 'query', 'mode': 'live',
                'state': {'query': 'topic', 'paragraphs': [{'research': {'is_completed': True,
                    'search_history': [{'url': 'https://example.org'}], 'latest_summary': 'supported summary'}}]}})
            adapter.atomic_json(root / 'manifest.json', {'topic': 'topic', 'mode': 'live',
                'configuration_sha256': 'config', 'source_workflow_id': 'previous',
                'roles': {'query': {'path': checkpoint.name, 'sha256': adapter.digest_file(checkpoint)}}})
            args = argparse.Namespace(mode='live', role='query', resume_dir=folder)
            payload = {'topic': 'topic', 'max_paragraphs': 2, 'research_phase': 'initial'}
            self.assertIsNotNone(adapter.load_resume_seed(args, payload, 'config'))
            payload['research_phase'] = 'refinement'
            self.assertIsNone(adapter.load_resume_seed(args, payload, 'config'))
            # An explicitly frozen feedback checkpoint is selected by its own stage.
            manifest = adapter.read_json(root / 'manifest.json')
            manifest['roles']['query-feedback'] = manifest['roles']['query']
            adapter.atomic_json(root / 'manifest.json', manifest)
            self.assertIsNotNone(adapter.load_resume_seed(args, payload, 'config'))

    def test_final_discussion_does_not_load_initial_discussion_checkpoint(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            adapter.atomic_json(root / 'manifest.json', {'topic': 'topic', 'mode': 'live',
                'configuration_sha256': 'config', 'roles': {'forum': {'path': 'missing.json', 'sha256': '0' * 64}}})
            args = argparse.Namespace(mode='live', role='forum', resume_dir=folder)
            payload = {'topic': 'topic', 'discussion_round': 2}
            self.assertIsNone(adapter.load_resume_seed(args, payload, 'config'))
            with self.assertRaises(adapter.AdapterError):
                adapter.load_resume_seed(args, {**payload, 'discussion_round': 1}, 'config')


if __name__ == '__main__':
    unittest.main()
