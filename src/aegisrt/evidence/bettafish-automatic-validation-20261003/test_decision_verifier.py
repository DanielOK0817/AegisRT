"""Offline saved-node decision fixtures; no models or provider services."""
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import verify_round
import workflow_common as common


ROOT = Path(__file__).resolve().parents[2] / "integrations/bettafish"
adapter = common.module("offline_decision_verifier_adapter", ROOT / "adapter.py")
FROZEN_NODE_ADAPTER = Path(__file__).resolve().parent / "revisions/code-605e3ad9a505f90e/integration/adapter.py"
assert common.sha(FROZEN_NODE_ADAPTER) == "016f5dfea59c364c8a2b06600a172e11e38d5c9e940cb69416d63220c2970e03"
# Node-only archives use the actual frozen legacy source, not a v2 budget bypass.
frozen_node_adapter = common.module("offline_decision_frozen_node_adapter", FROZEN_NODE_ADAPTER)


class DecisionVerifierTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.prepare()

    def prepare(self, *, correction=False, phase="initial", role="query"):
        self.role = role
        self.task = self.root / ("case-" + str(len(list(self.root.iterdir()))))
        self.launch = {"topic": "Frozen exact topic"}
        self.initial = {"paragraphs": [{"title": f"Angle {index}", "content": f"Scope {index}",
            "research": {"latest_summary": f"Original summary {index}"}} for index in range(2)]}
        audit = {"phase": phase, "paragraphs": [], "decision_requests": [], "decision_responses": [],
                 "summary_responses": [], "search_responses": [], "forum_feedback_sha256": hashlib.sha256(b"Exact feedback").hexdigest()}
        self.state = {"paragraphs": copy.deepcopy(self.initial["paragraphs"]), "research_audit": audit}
        for index, paragraph in enumerate(self.state["paragraphs"]):
            entry = {"queries": [], "reflections_executed": 1}
            audit["paragraphs"].append(entry)
            kinds = ["feedback"] if phase == "refinement" else ["initial", "reflection"]
            previous_summary = paragraph["research"]["latest_summary"]
            for kind in kinds:
                decision = {"search_query": f"Model query {index} {kind}", "reasoning": "Check evidence",
                            "search_tool": "basic_search_news" if role == "query" else "comprehensive_search"}
                entry["queries"].append({"kind": kind, **decision})
                data = {"topic": self.launch["topic"], "title": paragraph["title"], "content": paragraph["content"]}
                if kind != "initial":
                    data["paragraph_latest_state"] = previous_summary
                if phase == "refinement":
                    data["forum_feedback"] = "Exact feedback"
                responses = ["Prefix " + "x" * 17000, adapter.canonical(decision)] if correction and index == 0 and kind == kinds[0] else [adapter.canonical(decision)]
                for attempt, text in enumerate(responses):
                    ordinal = len(audit["decision_requests"]) + 1
                    if attempt:
                        data["format_correction"] = {"validation_error": "model did not return complete JSON",
                            "instruction": "Return complete node JSON", "previous_response_excerpt": responses[0][:16000],
                            "previous_response_truncated": len(responses[0]) > 16000}
                    input_path = self.task / "model-inputs" / f"decision-{ordinal:03d}.json"
                    common.write(input_path, {"system": "Role exact decision contract", "user": json.dumps(data, ensure_ascii=False)})
                    audit["decision_requests"].append(adapter.file_record(input_path))
                    response_path = self.task / "model-responses" / f"decision-{ordinal:03d}.txt"
                    response_path.parent.mkdir(parents=True, exist_ok=True)
                    response_path.write_text(text, encoding="utf-8")
                    audit["decision_responses"].append({**adapter.file_record(response_path), "raw_response_sha256": common.sha(response_path),
                        "sensitive_values_redacted": False, "validation": "rejected" if len(responses) == 2 and attempt == 0 else "accepted"})
                search_path = self.task / "search-responses" / f"search-{len(audit['search_responses']) + 1:03d}.json"
                common.write(search_path, {"request_attempt": 1, "tool": decision["search_tool"], "query": decision["search_query"]})
                audit["search_responses"].append({**adapter.file_record(search_path), "paragraph_index": index})
                previous_summary = f"Model summary {index} {kind}"
                summary_path = self.task / "model-responses" / f"summary-{len(audit['summary_responses']) + 1:03d}-initial.txt"
                summary_path.write_text(adapter.canonical({"paragraph_latest_state": previous_summary}), encoding="utf-8")
                audit["summary_responses"].append({**adapter.file_record(summary_path), "validation": "accepted"})
            paragraph["research"]["latest_summary"] = previous_summary

    def verify(self, *, source_adapter=frozen_node_adapter):
        return verify_round.verify_research_decisions(self.task, self.role, self.state, self.launch, source_adapter,
                                                    self.initial if self.state["research_audit"]["phase"] == "refinement" else None)

    def test_current_v3_source_rejects_missing_provider_budget(self):
        self.assertEqual(adapter.PROVIDER_RETRY_SCHEMA, verify_round.PROVIDER_RETRY_V3)
        with self.assertRaisesRegex(ValueError, "missing its provider trace budget"):
            self.verify(source_adapter=adapter)

    def rewrite_input(self, ordinal, mutate):
        records = self.state["research_audit"]["decision_requests"]
        path = Path(records[ordinal - 1]["path"])
        value = common.read(path)
        data = json.loads(value["user"])
        mutate(data)
        value["user"] = json.dumps(data, ensure_ascii=False)
        common.write(path, value)
        records[ordinal - 1] = adapter.file_record(path)

    def test_initial_and_refinement_bind_node_inputs_responses_and_searches(self):
        self.assertEqual(self.verify()["logical_decisions_verified"], 4)
        self.prepare(phase="refinement", role="media")
        result = self.verify()
        self.assertEqual(result["logical_decisions_verified"], 2)
        self.assertFalse(result["actual_sdk_messages_hash_recorded"])

    def test_one_correction_binds_exact_prior_raw_excerpt_and_validation_error(self):
        self.prepare(correction=True)
        self.assertEqual(self.verify()["format_corrections"], 1)

    def test_rehashed_topic_and_paragraph_changes_are_rejected(self):
        self.rewrite_input(1, lambda data: data.update(topic="Other topic"))
        with self.assertRaisesRegex(ValueError, "original topic or paragraph"):
            self.verify()

    def test_correction_cannot_reference_another_response_even_if_rehashed(self):
        self.prepare(correction=True)
        self.rewrite_input(2, lambda data: data["format_correction"].update(previous_response_excerpt="Invented previous response"))
        with self.assertRaisesRegex(ValueError, "preceding rejection"):
            self.verify()

    def test_model_web_search_cannot_be_accepted_as_query_tool(self):
        record = self.state["research_audit"]["decision_responses"][0]
        path = Path(record["path"])
        value = adapter.decode_model_json(path.read_text(encoding="utf-8"))
        value["search_tool"] = "web_search"
        path.write_text(adapter.canonical(value), encoding="utf-8")
        record.update(**adapter.file_record(path), raw_response_sha256=common.sha(path))
        with self.assertRaisesRegex(ValueError, "rejected or mislabeled"):
            self.verify()

    def test_actual_search_query_cannot_replace_the_accepted_model_query(self):
        record = self.state["research_audit"]["search_responses"][0]
        path = Path(record["path"])
        value = common.read(path)
        value["query"] = "Default topic query"
        common.write(path, value)
        record.update(adapter.file_record(path))
        with self.assertRaisesRegex(ValueError, "Actual search tool or query"):
            self.verify()

    def test_reflection_must_consume_the_preceding_model_summary(self):
        self.rewrite_input(2, lambda data: data.update(paragraph_latest_state="Invented prior summary"))
        with self.assertRaisesRegex(ValueError, "preceding real paragraph summary"):
            self.verify()

    def test_feedback_input_must_bind_the_validated_forum(self):
        self.prepare(phase="refinement")
        self.rewrite_input(1, lambda data: data.update(forum_feedback="Wrong discussion"))
        with self.assertRaisesRegex(ValueError, "validated forum feedback"):
            self.verify()

    def test_missing_input_archive_is_not_hidden_by_audit_metadata(self):
        Path(self.state["research_audit"]["decision_requests"][0]["path"]).unlink()
        with self.assertRaisesRegex(ValueError, "missing from the audit"):
            self.verify()


if __name__ == "__main__":
    unittest.main(verbosity=2)
