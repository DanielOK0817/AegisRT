"""Rebind real adapter repair envelopes offline; no provider requests are sent."""
import copy
from pathlib import Path
import unittest
from unittest.mock import patch

import workflow_common as common
import verify_round


SOURCE = Path(__file__).resolve().parents[2] / "integrations/bettafish"
fixture = common.module("offline_review_feedback_fixture", SOURCE / "tests/test_report_input_budget.py")
adapter = fixture.adapter
reporting = fixture.reporting


class ReviewFeedbackVerifierTests(unittest.TestCase):
    def setUp(self):
        self.case = fixture.ReportInputBudgetTests()
        self.case.setUp()
        self.addCleanup(self.case.doCleanups)
        self.calls = []

    def prepare(self, *, near_limit=True, provider_error=False, frozen_adapter=adapter):
        self.adapter = frozen_adapter
        self.reporting = common.module("offline_review_feedback_reporting_" + str(id(frozen_adapter)),
                                      Path(frozen_adapter.__file__).with_name("comprehensive_report.py"))
        limit = self.case.case.payload["max_content_length"]

        def completion(_usage, _role, _system, text):
            value = self.adapter.decode_model_json(text)
            self.calls.append(value)
            self.assertLessEqual(len(text), limit)
            if value.get("task") != "paragraph-evidence-review":
                candidate = self.adapter.decode_model_json(self.case.response(value))
                if near_limit:
                    bundle = common.read(self.case.case.target / "report-evidence.json")

                    def envelope_length():
                        supplied = {"task": "paragraph-evidence-review", "topic": self.case.case.payload["topic"],
                            **self.reporting.build_review_input(candidate, bundle, limit,
                                                                 sample_scope=value["sample_scope"])}
                        return len(self.adapter.canonical(supplied))

                    for section in candidate["sections"]:
                        for paragraph in section["paragraphs"]:
                            available = min(1800 - len(paragraph["text"]), (59000 - envelope_length()) // 2)
                            paragraph["text"] = "材料保留来源范围" * (max(0, available) // 8) + paragraph["text"]
                            remainder = (59000 - envelope_length()) // 2
                            paragraph["text"] = "材" * max(0, min(1800 - len(paragraph["text"]), remainder, 7)) + paragraph["text"]
                    self.assertGreater(envelope_length(), 58980)
                return self.adapter.canonical(candidate)
            if provider_error and len(self.calls) == 3:
                raise self.adapter.ModelCallStopped("Offline transient fixture", retryable=True,
                                                    error_type="APIConnectionError")
            response = self.adapter.decode_model_json(self.case.response(value))
            if len(self.calls) == 2:
                for row in response["checks"]:
                    reason = "本段未引用（" + row["evidence_ids"][0] + "），需要重新核对实际引用。"
                    row.update(supported=False, reason=reason)
                    for sentence in row.get("sentence_checks", []):
                        sentence.update(supported=False, reason=reason)
            return self.adapter.canonical(response)

        with patch.object(self.adapter, "complete_text", side_effect=completion):
            self.manifest = self.adapter.downstream_role(self.case.case.args, self.case.case.request,
                self.case.case.payload, self.case.case.dependencies, self.case.case.target, None)
        self.folder = self.case.case.target
        self.audit = common.read(self.folder / "report-audit.json")
        self.candidate = common.read(self.folder / "report-content.json")
        self.bundle = common.read(self.folder / "report-evidence.json")
        self.launch = {"workflow_id": self.case.case.request["workflow_id"],
                       "topic": self.case.case.payload["topic"], "max_content_length": limit}
        attempts = common.read(self.folder / "report-validation-attempts.json")
        common.write(self.folder / "model-budget.json", {"attempts": len(attempts), "limit": 5})
        for index, attempt in enumerate(attempts, 1):
            common.write(self.folder / f"model-usage/call-{index}.json", {
                "call_id": f"call-{index}", "agent": "report", "run_id": self.launch["workflow_id"],
                "status": "error" if attempt["status"] == "provider_error" else "completed",
                "started_at": f"2026-10-05T00:00:{index * 2:02d}+00:00",
                "ended_at": f"2026-10-05T00:00:{index * 2 + 1:02d}+00:00"})

    def verify(self):
        return verify_round.verify_report_review(self.folder, self.manifest, self.audit,
            self.candidate, self.bundle, self.launch, self.adapter, self.reporting,
            sample_scope=self.audit.get("sample_scope"))

    def rewrite_final_input(self, change):
        path = self.adapter.checked_record(self.manifest["files"]["review_input"], self.folder)
        value = common.read(path)
        change(value)
        common.write(path, value)
        changed = self.adapter.file_record(path)
        self.manifest["files"]["review_input"] = changed
        final_path = self.folder / "report-evidence-review.json"
        final = common.read(final_path)
        final["review_input"] = changed
        common.write(final_path, final)
        self.manifest["files"]["evidence_review"] = self.adapter.file_record(final_path)
        self.audit["evidence_review"] = self.manifest["files"]["evidence_review"]

    def test_near_limit_bounded_feedback_preserves_full_rejection_and_passes_replay(self):
        self.prepare()
        self.assertEqual(len(self.calls), 3)
        feedback = self.calls[-1]["format_correction"]
        self.assertTrue(feedback["feedback_truncated"])
        self.assertEqual(len(self.audit["validation_attempts"][1]["issues"]), 14)
        self.assertNotEqual(feedback["issues"], self.audit["validation_attempts"][1]["issues"])
        self.assertEqual(self.verify()["requests_verified"], 3)

    def test_feedback_and_body_limits_use_the_exact_complete_request_envelope(self):
        self.prepare()
        with patch.object(self.reporting, "build_review_input", wraps=self.reporting.build_review_input) as build, \
                patch.object(self.adapter, "bounded_review_feedback", wraps=self.adapter.bounded_review_feedback) as bound:
            self.verify()
        envelope = {"task": "paragraph-evidence-review", "topic": self.launch["topic"]}
        expected_body_limit = self.launch["max_content_length"] - len(self.adapter.canonical(envelope)) + 1 - 500
        self.assertEqual([call.args[2] for call in build.call_args_list], [expected_body_limit] * 2)
        first_input_length = len(self.adapter.canonical(self.calls[1]))
        bound.assert_called_once()
        self.assertEqual(bound.call_args.args[1], self.launch["max_content_length"] - first_input_length -
                         len(',"format_correction":'))
        self.assertEqual(bound.call_args.args[0]["issues"], self.audit["validation_attempts"][1]["issues"])

    def test_rehashed_bounded_feedback_tampering_is_rejected(self):
        self.prepare()
        original = self.calls[-1]["format_correction"]
        mutations = {
            "instruction": "changed instruction",
            "validation_error": "changed original error",
            "issues": [],
            "issue_count": 0,
            "omitted_issue_count": 0,
            "feedback_truncated": False,
        }
        for field, replacement in mutations.items():
            with self.subTest(field=field):
                changed = {**copy.deepcopy(original), field: replacement}
                self.rewrite_final_input(lambda value: value.update(format_correction=changed))
                with self.assertRaisesRegex(ValueError, "independently rebuilt failed-review feedback"):
                    self.verify()

    def test_full_rejection_metadata_is_bound_to_the_original_failed_response(self):
        self.prepare()
        original = copy.deepcopy(self.audit["validation_attempts"])
        for field, replacement in (("reason", "changed archived reason"), ("issues", [])):
            with self.subTest(field=field):
                changed = copy.deepcopy(original)
                changed[1][field] = replacement
                common.write(self.folder / "report-validation-attempts.json", changed)
                self.audit["validation_attempts"] = changed
                with self.assertRaisesRegex(ValueError, "independently replayed raw response"):
                    self.verify()

    def test_missing_required_feedback_cannot_pass_after_rehashing(self):
        self.prepare()
        self.rewrite_final_input(lambda value: value.pop("format_correction"))
        with self.assertRaisesRegex(ValueError, "independently rebuilt failed-review feedback"):
            self.verify()

    def test_unsolicited_first_review_feedback_is_rejected(self):
        self.prepare()
        path = self.folder / "report-review-input-2.json"
        value = common.read(path)
        value["format_correction"] = self.calls[-1]["format_correction"]
        common.write(path, value)
        with self.assertRaisesRegex(ValueError, "independently rebuilt failed-review feedback"):
            self.verify()

    def test_rehashed_evidence_and_sentence_tampering_remain_rejected(self):
        self.prepare()
        original = copy.deepcopy(self.calls[-1])
        for field in ("evidence", "paragraphs"):
            with self.subTest(field=field):
                changed = copy.deepcopy(original)
                if field == "evidence":
                    changed["evidence"]["sources"][0]["excerpt"] += "changed evidence"
                else:
                    changed["paragraphs"][0]["sentences"][0]["text"] += "changed original sentence"
                self.rewrite_final_input(lambda value: value.update(changed))
                with self.assertRaisesRegex(ValueError, "exact supplied cited excerpts"):
                    self.verify()

    def test_retryable_provider_error_preserves_feedback_from_the_last_rejected_review(self):
        self.prepare(provider_error=True)
        self.assertEqual(len(self.calls), 4)
        self.assertEqual(self.calls[2], self.calls[3])
        result = self.verify()
        self.assertEqual(result["requests_verified"], 4)
        self.assertEqual(result["provider_errors"], 1)

    def test_noncallable_frozen_feedback_helper_cannot_use_legacy_rules(self):
        self.prepare()
        with patch.object(self.adapter, "bounded_review_feedback", None):
            with self.assertRaisesRegex(ValueError, "Frozen bounded review feedback helper"):
                self.verify()

    def test_legacy_frozen_adapter_keeps_its_exact_unbounded_feedback(self):
        path = Path(__file__).resolve().parent / "revisions/code-0aa0576bd95d1485/integration/adapter.py"
        old = common.module("offline_legacy_unbounded_review_adapter", path)
        self.assertFalse(hasattr(old, "bounded_review_feedback"))
        self.prepare(near_limit=False, frozen_adapter=old)
        feedback = self.calls[-1]["format_correction"]
        self.assertNotIn("feedback_truncated", feedback)
        self.assertEqual(feedback["issues"], self.audit["validation_attempts"][1]["issues"])
        self.assertEqual(self.verify()["requests_verified"], 3)


if __name__ == "__main__":
    unittest.main(verbosity=2)
