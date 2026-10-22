"""Offline verifier fixtures only; they are not real business acceptance runs."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import workflow_common as common
import verify_round


ROOT = Path(__file__).resolve().parents[2] / "integrations/bettafish"
adapter = common.module("offline_review_verifier_adapter", ROOT / "adapter.py")
fixture = common.module("offline_review_verifier_evidence", ROOT / "tests/test_comprehensive_report.py")
reporting = fixture.report


class ReviewVerifierTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name)
        source = fixture.ComprehensiveReportTests()
        source.setUp()
        self.bundle = source.bundle()
        self.candidate = reporting.validate_report(source.candidate(self.bundle), self.bundle)
        self.launch = {"workflow_id": "offline-semantic-fixture", "topic": "测试话题", "max_content_length": 60000}
        self.prepare()

    def review(self, candidate, supported=True):
        supplied = reporting.build_review_input(candidate, self.bundle)
        checks = []
        for row in supplied["paragraphs"]:
            ids = (row["source_ids"] + row["comment_ids"] + row["research_ids"])[:1]
            check = {"path": row["path"], "supported": supported,
                     "reason": "给定摘录支持该段谨慎归因。" if supported else "摘录不足以支持当前表述。",
                     "evidence_ids": ids}
            if reporting.REVIEW_INPUT_SCHEMA in ("bettafish.evidence-review-input/v2", "bettafish.evidence-review-input/v3"):
                check["sentence_checks"] = [{"index": sentence["index"], "supported": supported,
                    "reason": "原句只归纳本段自身引用材料。" if supported else "本段引用不足以支持该原句。",
                    "evidence_ids": ids} for sentence in row["sentences"]]
            checks.append(check)
        return {"checks": checks}

    def prepare(self, correction=False):
        old = copy.deepcopy(self.candidate)
        old["sections"][0]["paragraphs"][0]["text"] = "来源材料呈现关注点，具体结论尚须核对。"
        if correction:
            rejected = self.review(old, False)
            _, issues = reporting.validate_evidence_review(rejected, old, self.bundle)
            stages = [(old, "generation", "candidate_validated", None),
                      (old, "evidence_review", "evidence_rejected", rejected),
                      (self.candidate, "generation", "candidate_validated", None),
                      (self.candidate, "evidence_review", "passed", self.review(self.candidate))]
        else:
            stages = [(self.candidate, "generation", "candidate_validated", None),
                      (self.candidate, "evidence_review", "passed", self.review(self.candidate))]
        attempts = []
        for index, (candidate, phase, status, review) in enumerate(stages, 1):
            attempt = {"attempt": index, "phase": phase, "status": status}
            if status == "evidence_rejected":
                attempt["issues"] = issues
            attempts.append(attempt)
            if phase == "generation":
                response = adapter.canonical(candidate)
                if correction and index == 3:
                    resolved, receipt = reporting.apply_scoped_correction(old, candidate, issues)
                    self.assertEqual(resolved, candidate)
                    common.write(self.folder / f"report-correction-{index}.json", receipt)
                    common.write(self.folder / f"report-generation-input-{index}.json", {
                        "correction": {"editable_paths": receipt["editable_paths"]}})
                (self.folder / f"model-report-response-{index}.txt").write_text(response, encoding="utf-8")
                (self.folder / "model-report-response.txt").write_text(response, encoding="utf-8")
            else:
                self.input_path = self.folder / f"report-review-input-{index}.json"
                common.write(self.input_path, {"task": "paragraph-evidence-review", "topic": self.launch["topic"],
                    **reporting.build_review_input(candidate, self.bundle, self.launch["max_content_length"] - 500)})
                (self.folder / f"model-report-review-response-{index}.txt").write_text(adapter.canonical(review), encoding="utf-8")
                common.write(self.folder / f"report-evidence-review-{index}.json", review)
                self.final_review = review
            common.write(self.folder / f"model-usage/call-{index}.json", {
                "call_id": f"call-{index}", "agent": "report", "run_id": self.launch["workflow_id"],
                "status": "completed", "started_at": f"2026-10-03T00:00:{index * 2:02d}+00:00",
                "ended_at": f"2026-10-03T00:00:{index * 2 + 1:02d}+00:00"})
        common.write(self.folder / "model-budget.json", {"attempts": len(stages), "limit": 5})
        common.write(self.folder / "report-validation-attempts.json", attempts)
        common.write(self.folder / "report-evidence-review.json", {
            "candidate_sha256": verify_round.hashlib.sha256(adapter.canonical(self.candidate).encode()).hexdigest(),
            "review": self.final_review, "issues": [], "all_paragraphs_supported": True,
            "review_input": adapter.file_record(self.input_path),
            "scope": "support within supplied cited excerpts; not independent world fact verification"})
        self.manifest = {"files": {"evidence_review": adapter.file_record(self.folder / "report-evidence-review.json"),
                                  "review_input": adapter.file_record(self.input_path)}}
        self.audit = {"validation_attempts": attempts, "evidence_review": self.manifest["files"]["evidence_review"],
                      "generation_policy": reporting.CORRECTION_SCHEMA,
                      "automatic_corrections": [adapter.file_record(path) for path in sorted(self.folder.glob('report-correction-*.json'))],
                      "validation": {"model_evidence_review_performed": True, "model_fact_check_performed": False}}

    def verify(self):
        return verify_round.verify_report_review(self.folder, self.manifest, self.audit, self.candidate,
                                                self.bundle, self.launch, adapter, reporting)

    def test_exact_generated_candidate_and_all_paragraph_review_pass_without_network(self):
        with patch("socket.socket", side_effect=AssertionError("offline verifier attempted network")):
            result = self.verify()
        self.assertEqual(result["requests_verified"], 2)
        self.assertEqual(result["paragraphs_supported"], 7)
        self.assertFalse(result["independent_world_fact_verification"])

    def test_automatic_unsupported_correction_is_rebound_and_reaudited(self):
        self.prepare(correction=True)
        result = self.verify()
        self.assertEqual(result["requests_verified"], 4)
        self.assertEqual(result["generation_requests"], 2)
        self.assertEqual(result["review_requests"], 2)

    def test_audit_true_does_not_accept_a_model_unsupported_check(self):
        review = self.review(self.candidate, False)
        (self.folder / "model-report-review-response-2.txt").write_text(adapter.canonical(review), encoding="utf-8")
        common.write(self.folder / "report-evidence-review-2.json", review)
        with self.assertRaisesRegex(ValueError, "unsupported paragraphs"):
            self.verify()

    def test_scoped_receipt_tampering_is_rejected(self):
        self.prepare(correction=True)
        receipt = common.read(self.folder / 'report-correction-3.json')
        receipt['base_sha256'] = '0' * 64
        common.write(self.folder / 'report-correction-3.json', receipt)
        self.audit['automatic_corrections'] = [adapter.file_record(self.folder / 'report-correction-3.json')]
        with self.assertRaisesRegex(ValueError, 'independently replayed'):
            self.verify()

    def test_scoped_receipt_cannot_be_removed(self):
        self.prepare(correction=True)
        (self.folder / 'report-correction-3.json').unlink()
        with self.assertRaisesRegex(ValueError, 'correction receipts'):
            self.verify()

    def test_scoped_input_cannot_expand_reviewed_paths(self):
        self.prepare(correction=True)
        common.write(self.folder / 'report-generation-input-3.json', {'correction': {'editable_paths': ['$']}})
        with self.assertRaisesRegex(ValueError, 'actual rejected paragraph paths'):
            self.verify()

    def test_missing_model_paragraph_check_cannot_pass(self):
        review = copy.deepcopy(self.final_review)
        review["checks"].pop()
        (self.folder / "model-report-review-response-2.txt").write_text(adapter.canonical(review), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "malformed or incomplete"):
            self.verify()

    def test_reviewer_extra_or_changed_evidence_is_rejected(self):
        supplied = common.read(self.input_path)
        supplied["evidence"]["comments"][0]["content"] = "changed evidence"
        common.write(self.input_path, supplied)
        with self.assertRaisesRegex(ValueError, "exact supplied cited excerpts"):
            self.verify()

    def test_rehashed_record_still_cannot_claim_another_candidate(self):
        record = common.read(self.folder / "report-evidence-review.json")
        record["candidate_sha256"] = "0" * 64
        common.write(self.folder / "report-evidence-review.json", record)
        self.manifest["files"]["evidence_review"] = adapter.file_record(self.folder / "report-evidence-review.json")
        self.audit["evidence_review"] = self.manifest["files"]["evidence_review"]
        with self.assertRaisesRegex(ValueError, "not bound to this candidate"):
            self.verify()

    def test_audit_flags_cannot_replace_a_real_review_usage_record(self):
        (self.folder / "model-usage/call-2.json").unlink()
        with self.assertRaisesRegex(ValueError, "own usage record"):
            self.verify()

    def test_generation_structure_success_is_not_final_review_success(self):
        attempts = self.audit["validation_attempts"]
        attempts[-1].update(phase="generation", status="candidate_validated")
        common.write(self.folder / "report-validation-attempts.json", attempts)
        with self.assertRaisesRegex(ValueError, "followed by evidence review"):
            self.verify()

    def test_v2_review_input_retains_complete_frozen_pool_and_original_sentences(self):
        supplied = common.read(self.input_path)
        self.assertEqual(supplied["schema"], "bettafish.evidence-review-input/v3")
        self.assertEqual(supplied["evidence"], {kind: self.bundle["model_input"][kind]
                                               for kind in ("sources", "research", "comments")})
        for row in supplied["paragraphs"]:
            self.assertEqual("".join(sentence["text"] for sentence in row["sentences"]), row["text"])
            self.assertEqual([sentence["index"] for sentence in row["sentences"]], list(range(len(row["sentences"]))))
        self.assertTrue(supplied["scope"]["sentence_checks_required"])
        self.assertTrue(supplied["scope"]["uncited_evidence_for_contradiction_only"])
        with patch("socket.socket", side_effect=AssertionError("offline verifier attempted network")):
            self.verify()

    def test_v2_missing_sentence_model_check_cannot_pass(self):
        review = copy.deepcopy(self.final_review)
        review["checks"][0]["sentence_checks"].pop()
        (self.folder / "model-report-review-response-2.txt").write_text(adapter.canonical(review), encoding="utf-8")
        with self.assertRaises(ValueError):
            self.verify()

    def test_rehashed_sample_metadata_cannot_replace_verified_scope(self):
        supplied = common.read(self.input_path)
        supplied['sample_scope'] = {'historical_relative_to_collection': False}
        common.write(self.input_path, supplied)
        with self.assertRaisesRegex(ValueError, 'exact supplied cited excerpts'):
            self.verify()

    def test_rehashed_citation_index_cannot_hide_an_existing_citation(self):
        supplied = common.read(self.input_path)
        supplied['paragraphs'][0]['citation_index'] = []
        common.write(self.input_path, supplied)
        with self.assertRaisesRegex(ValueError, 'exact supplied cited excerpts'):
            self.verify()

    def test_v2_uncited_frozen_evidence_cannot_support_another_paragraph_sentence(self):
        supplied = reporting.build_review_input(self.candidate, self.bundle)
        first = supplied["paragraphs"][0]
        own = set(first["source_ids"] + first["comment_ids"] + first["research_ids"])
        borrowed = next(row["id"] for rows in supplied["evidence"].values() for row in rows if row["id"] not in own)
        review = copy.deepcopy(self.final_review)
        review["checks"][0]["sentence_checks"][0]["evidence_ids"] = [borrowed]
        (self.folder / "model-report-review-response-2.txt").write_text(adapter.canonical(review), encoding="utf-8")
        with self.assertRaises(ValueError):
            self.verify()

    def test_v2_rehashed_changed_sentence_input_still_fails_actual_binding(self):
        supplied = common.read(self.input_path)
        supplied["paragraphs"][0]["sentences"][0]["text"] += "changed original sentence"
        common.write(self.input_path, supplied)
        changed_record = adapter.file_record(self.input_path)
        self.manifest["files"]["review_input"] = changed_record
        final = common.read(self.folder / "report-evidence-review.json")
        final["review_input"] = changed_record
        common.write(self.folder / "report-evidence-review.json", final)
        record = adapter.file_record(self.folder / "report-evidence-review.json")
        self.manifest["files"]["evidence_review"] = record
        self.audit["evidence_review"] = record
        with self.assertRaisesRegex(ValueError, "exact supplied cited excerpts"):
            self.verify()


if __name__ == "__main__":
    unittest.main(verbosity=2)
