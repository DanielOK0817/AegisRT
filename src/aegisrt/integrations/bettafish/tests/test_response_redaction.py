"""Preserve non-secret configuration words in archived model responses."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("response_redaction_adapter", Path(__file__).resolve().parents[1] / "adapter.py")
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


class ResponseRedactionTests(unittest.TestCase):
    def test_tokenizer_flag_is_preserved_in_response_and_not_registered_as_a_secret(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(adapter.os.environ, {}, clear=True):
            root = Path(directory)
            env_file = root / "private.env"
            env_file.write_text("TOKENIZERS_PARALLELISM=false\nQUERY_ENGINE_API_KEY=unit-test-private-key\n", encoding="utf-8")
            secrets = adapter.load_environment(env_file)
            self.assertEqual(secrets, ["unit-test-private-key"])
            raw = json.dumps({"paragraph_latest_state": "The false flag is ordinary source text."})
            audit = {}
            adapter.archive_research_summary(raw, root / "response.txt", audit)
            self.assertEqual((root / "response.txt").read_text(encoding="utf-8"), raw)
            self.assertFalse(audit["summary_responses"][0]["sensitive_values_redacted"])

    def test_real_credential_is_redacted_while_raw_hash_and_strict_schema_are_retained(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(adapter.os.environ, {
                "TOKENIZERS_PARALLELISM": "false", "QUERY_ENGINE_API_KEY": "unit-test-private-key"}, clear=True):
            root = Path(directory)
            raw = json.dumps({"paragraph_latest_state": "false unit-test-private-key",
                              "search_scope": {"date_constraints_relaxed": False}})
            audit = {}
            with self.assertRaisesRegex(adapter.AdapterError, "invalid research summary fields"):
                adapter.archive_research_summary(raw, root / "response.txt", audit)
            safe = (root / "response.txt").read_text(encoding="utf-8")
            self.assertNotIn("unit-test-private-key", safe)
            self.assertFalse(json.loads(safe)["search_scope"]["date_constraints_relaxed"])
            self.assertIn("false [redacted]", safe)
            record = audit["summary_responses"][0]
            self.assertEqual(record["raw_response_sha256"], hashlib.sha256(raw.encode("utf-8")).hexdigest())
            self.assertTrue(record["sensitive_values_redacted"])
            self.assertEqual(record["validation"], "rejected")


if __name__ == "__main__":
    unittest.main()
