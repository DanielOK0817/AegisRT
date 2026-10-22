"""Failed disk sync must not publish a new report or runtime observation file."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from test_full_workflow import adapter, runner


class DurableOutputTests(unittest.TestCase):
    def test_disk_sync_failure_preserves_previous_valid_json(self):
        for writer in (adapter.atomic_json, runner.save_json):
            with self.subTest(writer=writer.__name__), tempfile.TemporaryDirectory() as directory:
                path = Path(directory)/'snapshot.json'
                path.write_text('{"status":"old"}', encoding='utf-8')
                with patch('os.fsync', side_effect=OSError('disk sync failed')):
                    with self.assertRaises(OSError):
                        writer(path, {'status':'new'})
                self.assertEqual(json.loads(path.read_text()), {'status':'old'})
                self.assertEqual([p.name for p in Path(directory).iterdir()], ['snapshot.json'])

    def test_successful_replace_syncs_file_before_publishing(self):
        for writer in (adapter.atomic_json, runner.save_json):
            with self.subTest(writer=writer.__name__), tempfile.TemporaryDirectory() as directory:
                path=Path(directory)/'nested/snapshot.json'
                with patch('os.fsync') as sync:
                    writer(path, {'status':'complete'})
                self.assertGreaterEqual(sync.call_count, 1)
                self.assertEqual(json.loads(path.read_text()), {'status':'complete'})


if __name__=='__main__':unittest.main()
