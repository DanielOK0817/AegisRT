"""Offline outline evidence fixtures; these are not real workflow passes."""
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import verify_round
import workflow_common as common


ROOT = Path(__file__).resolve().parents[2] / "integrations/bettafish"
adapter = common.module("offline_structure_verifier_adapter", ROOT / "adapter.py")
FROZEN_NODE_ADAPTER = Path(__file__).resolve().parent / "revisions/code-605e3ad9a505f90e/integration/adapter.py"
assert common.sha(FROZEN_NODE_ADAPTER) == "016f5dfea59c364c8a2b06600a172e11e38d5c9e940cb69416d63220c2970e03"
# Node-only archives use the actual frozen legacy source, not a v2 budget bypass.
frozen_node_adapter = common.module("offline_structure_frozen_node_adapter", FROZEN_NODE_ADAPTER)


class StructureVerifierTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.prepare()

    def prepare(self, count=2, first_rejected=False, redacted=False):
        self.task = self.root / ("case-" + str(len(list(self.root.iterdir()))))
        self.launch = {"workflow_id": "offline-outline", "topic": "测试话题", "max_paragraphs": count}
        paragraphs = [{"title": f"角度{index}", "content": f"核查范围{index}"} for index in range(count)]
        accepted = adapter.canonical({"paragraphs": paragraphs})
        if redacted:
            accepted = "<think>unit-test-private-value</think>" + accepted
        texts = ["Explanation before JSON " + "x" * 17000, accepted] if first_rejected else [accepted]
        audit = {"phase": "initial", "structure_requests": [], "structure_responses": []}
        self.state = {"paragraphs": copy.deepcopy(paragraphs), "research_audit": audit}
        previous_text, previous_error = None, None
        for index, original_text in enumerate(texts, 1):
            text = original_text.replace("unit-test-private-value", "[redacted]") if redacted else original_text
            user = self.launch["topic"] if index == 1 else adapter.canonical({
                "topic": self.launch["topic"], "requested_paragraphs": count, "format_correction": {
                    "validation_error": previous_error, "instruction": "Return the exact JSON object without a prefix.",
                    "previous_response_excerpt": previous_text[:16000],
                    "previous_response_truncated": len(previous_text) > 16000}})
            request_path = self.task / "model-inputs" / f"structure-{index:03d}.json"
            common.write(request_path, {"system": "Return paragraphs, title and content only.", "user": user})
            response_path = self.task / "model-responses" / f"structure-{index:03d}.txt"
            response_path.parent.mkdir(parents=True, exist_ok=True)
            response_path.write_text(text, encoding="utf-8")
            try:
                adapter.decode_research_structure(text, count)
            except adapter.AdapterError as exc:
                validation, previous_error = "rejected", str(exc)
            else:
                validation = "accepted"
            audit["structure_requests"].append(adapter.file_record(request_path))
            audit["structure_responses"].append({**adapter.file_record(response_path),
                "raw_response_sha256": hashlib.sha256(original_text.encode()).hexdigest(),
                "sensitive_values_redacted": original_text != text, "validation": validation})
            common.write(self.task / f"model-usage/call-{index}.json", {
                "call_id": f"call-{index}", "agent": "query", "run_id": self.launch["workflow_id"],
                "status": "completed", "started_at": f"2026-10-03T00:00:{index * 2:02d}+00:00",
                "ended_at": f"2026-10-03T00:00:{index * 2 + 1:02d}+00:00"})
            previous_text = text

    def verify(self, initial=None, *, source_adapter=frozen_node_adapter):
        return verify_round.verify_research_structure(self.task, "query", self.state, self.launch, source_adapter, initial)

    def test_current_v3_source_rejects_missing_provider_budget(self):
        self.assertEqual(adapter.PROVIDER_RETRY_SCHEMA, verify_round.PROVIDER_RETRY_V3)
        with self.assertRaisesRegex(ValueError, "missing its provider trace budget"):
            self.verify(source_adapter=adapter)

    def test_exact_two_and_five_model_paragraphs_bind_to_state_without_network(self):
        for count in (2, 5):
            with self.subTest(count=count):
                self.prepare(count=count)
                with patch("socket.socket", side_effect=AssertionError("Offline fixture opened a socket")):
                    result = self.verify()
                self.assertEqual(result["paragraphs_bound_to_state"], count)
                self.assertTrue(result["stored_inputs_bind_original_query"])
                self.assertFalse(result["actual_sdk_messages_hash_recorded"])

    def test_one_budgeted_correction_binds_prior_rejection_and_truncated_excerpt(self):
        self.prepare(first_rejected=True)
        result = self.verify()
        self.assertEqual(result["structure_requests_verified"], 2)
        self.assertEqual(result["format_corrections"], 1)
        self.assertEqual(result["unredacted_original_response_hashes_verified"], 2)

    def test_structure_state_cannot_replace_the_actual_accepted_model_titles(self):
        self.state["paragraphs"][0]["title"] = "Invented replacement"
        with self.assertRaisesRegex(ValueError, "differs from final state"):
            self.verify()

    def test_rehashed_bare_array_cannot_pass_the_strict_object_contract(self):
        record = self.state["research_audit"]["structure_responses"][0]
        path = Path(record["path"])
        text = adapter.canonical(self.state["paragraphs"])
        path.write_text(text, encoding="utf-8")
        record.update(adapter.file_record(path), raw_response_sha256=hashlib.sha256(text.encode()).hexdigest())
        with self.assertRaisesRegex(ValueError, "Final outline is malformed"):
            self.verify()

    def test_fewer_model_paragraphs_cannot_pass_even_with_rehashed_response(self):
        record = self.state["research_audit"]["structure_responses"][0]
        path = Path(record["path"])
        text = adapter.canonical({"paragraphs": self.state["paragraphs"][:1]})
        path.write_text(text, encoding="utf-8")
        record.update(adapter.file_record(path), raw_response_sha256=hashlib.sha256(text.encode()).hexdigest())
        with self.assertRaisesRegex(ValueError, "Final outline is malformed"):
            self.verify()

    def test_rehashed_changed_original_topic_is_rejected(self):
        record = self.state["research_audit"]["structure_requests"][0]
        path = Path(record["path"])
        value = common.read(path)
        value["user"] = "Changed topic"
        common.write(path, value)
        record.update(adapter.file_record(path))
        with self.assertRaisesRegex(ValueError, "differs from the original topic"):
            self.verify()

    def test_rehashed_correction_cannot_reference_another_response(self):
        self.prepare(first_rejected=True)
        record = self.state["research_audit"]["structure_requests"][1]
        path = Path(record["path"])
        value = common.read(path)
        correction = json.loads(value["user"])
        correction["format_correction"]["previous_response_excerpt"] = "Changed rejection"
        value["user"] = adapter.canonical(correction)
        common.write(path, value)
        record.update(adapter.file_record(path))
        with self.assertRaisesRegex(ValueError, "actual prior rejection"):
            self.verify()

    def test_missing_usage_and_wrong_call_identity_are_rejected(self):
        path = self.task / "model-usage/call-1.json"
        value = common.read(path)
        value["run_id"] = "another-workflow"
        common.write(path, value)
        with self.assertRaisesRegex(ValueError, "usage identity"):
            self.verify()
        path.unlink()
        with self.assertRaisesRegex(ValueError, "own real usage record"):
            self.verify()

    def test_unredacted_original_hash_cannot_be_rewritten(self):
        self.state["research_audit"]["structure_responses"][0]["raw_response_sha256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "original response hash changed"):
            self.verify()

    def test_redacted_thinking_preserves_exact_fields_without_claiming_reversible_raw(self):
        self.prepare(redacted=True)
        result = self.verify()
        self.assertEqual(result["redacted_response_records"], 1)
        self.assertEqual(result["unredacted_original_response_hashes_verified"], 0)
        self.assertFalse(result["redacted_original_response_reversible"])

    def test_refinement_preserves_outline_and_has_no_new_outline_calls(self):
        initial = copy.deepcopy(self.state)
        self.task = self.root / "refinement"
        self.state = {"paragraphs": copy.deepcopy(initial["paragraphs"]), "research_audit": {"phase": "refinement"}}
        result = self.verify(initial)
        self.assertTrue(result["initial_outline_preserved"])
        self.state["paragraphs"][0]["content"] = "Changed scope"
        with self.assertRaisesRegex(ValueError, "differ from the initial model outline"):
            self.verify(initial)

    def test_refinement_cannot_hide_an_untracked_new_outline_response(self):
        initial = copy.deepcopy(self.state)
        self.state["research_audit"] = {"phase": "refinement"}
        with self.assertRaisesRegex(ValueError, "generated a new model outline"):
            self.verify(initial)


if __name__ == "__main__":
    unittest.main(verbosity=2)
