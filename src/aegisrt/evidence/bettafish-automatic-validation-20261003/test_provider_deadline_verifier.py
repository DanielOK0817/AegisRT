"""Offline request-deadline verification; no SDK, workflow or network calls."""
import copy
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import test_provider_retry_verifier as provider_fixtures
import verify_round
import workflow_common as common


SCHEMA = "bettafish.provider-retry/v2"


class ProviderDeadlineVerifierTests(unittest.TestCase):
    def setUp(self):
        self.fixture = provider_fixtures.ProviderRetryVerifierTests(methodName="runTest")
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.case = self.fixture.case
        self.base_adapter = self.fixture.adapter
        capabilities = {name: getattr(self.base_adapter, name) for name in
                        ("canonical", "checked_record", "RESEARCH_ROLES")}
        self.legacy_adapter = SimpleNamespace(**capabilities)
        self.adapter = SimpleNamespace(**capabilities, PROVIDER_RETRY_SCHEMA=SCHEMA)
        self.budget = self.fixture.outline_retry()
        self.budget["retry_policy_schema"] = SCHEMA
        for index, row in enumerate(self.budget["provider_attempts"]):
            self.fixture.rewrite_kwargs(self.budget, index, lambda kwargs: kwargs.update(timeout=119))
            row["absolute_timeout_seconds"] = 120
        self.budget["provider_retries"][0]["request_sha256"] = self.budget["provider_attempts"][0]["request_sha256"]
        self.failure(["APITimeoutError"])

    def failure(self, chain, *, origin=None, cancelled=False, truncated=False, status=None):
        row = self.budget["provider_attempts"][0]
        row.update(error_type=chain[0] if chain else "APITimeoutError", http_status=status,
                   exception_chain_types=chain, exception_chain_truncated=truncated,
                   deadline_origin=origin, cancellation_in_chain=cancelled)
        path = self.case.task / "model-usage" / "call-1.json"
        usage = common.read(path)
        usage.update(error_type=row["error_type"], http_status=status)
        common.write(path, usage)

    def closed_started_stream(self):
        self.failure(["ModelRequestDeadlineExceeded"], origin="request")
        for index, row in enumerate(self.budget["provider_attempts"]):
            self.fixture.rewrite_kwargs(self.budget, index, lambda data: data.update(stream=True))
            row["stream"] = True
            path = self.case.task / "model-usage" / f"call-{index + 1}.json"
            usage = common.read(path)
            usage["stream"] = True
            if index == 0:
                usage["status"] = "started"
                for key in ("ended_at", "error_type", "http_status"):
                    usage.pop(key, None)
            common.write(path, usage)
        self.budget["provider_retries"][0]["request_sha256"] = self.budget["provider_attempts"][0]["request_sha256"]
        self.budget["provider_attempts"][0].update(stream_closed=True, stream_closed_at="2026-10-03T00:00:03+00:00")
        return self.case.task / "model-usage/call-1.json"

    def verify(self, adapter=None):
        self.fixture.save(self.budget)
        with patch("socket.socket", side_effect=AssertionError("Offline verifier opened a socket")):
            return verify_round._research_provider_trace(self.case.task, "query", self.case.state,
                                                         self.case.launch, adapter or self.adapter)

    def test_direct_and_sdk_wrapped_request_deadlines_keep_both_real_usage_mappings(self):
        for chain in (["ModelRequestDeadlineExceeded"],
                      ["APITimeoutError", "ModelRequestDeadlineExceeded"],
                      ["TimeoutError", "ModelRequestDeadlineExceeded"],
                      ["APIConnectionError", "ModelRequestDeadlineExceeded"]):
            with self.subTest(chain=chain):
                self.failure(chain, origin="request")
                metrics, completed = self.verify()
                self.assertEqual(metrics["retry_policy_schema_verified"], SCHEMA)
                self.assertTrue(metrics["deadline_origin_metadata_verified"])
                self.assertEqual((metrics["provider_attempts_verified"], metrics["transient_retries_verified"],
                                  metrics["calls_missing_usage_preserved"]), (2, 1, 1))
                self.assertEqual(len(completed), 1)
                self.assertEqual(next(iter(completed.values()))["call_id"], "call-2")
                self.assertFalse(metrics["exception_cause_chains_independently_reconstructed"])

    def test_plain_sdk_timeout_and_server_failure_remain_allowed_without_local_deadline(self):
        for chain, status in ((["APITimeoutError"], None), (["InternalServerError"], 503)):
            with self.subTest(chain=chain, status=status):
                self.failure(chain, status=status)
                metrics, _ = self.verify()
                self.assertEqual(metrics["retry_policy_schema_verified"], SCHEMA)
                self.assertTrue(metrics["deadline_origin_metadata_verified"])

    def test_v2_policy_cannot_be_missing_downgraded_or_given_to_legacy_source(self):
        for value in (None, "bettafish.provider-retry/v1", "unknown"):
            with self.subTest(policy=value):
                if value is None:
                    self.budget.pop("retry_policy_schema", None)
                else:
                    self.budget["retry_policy_schema"] = value
                with self.assertRaisesRegex(ValueError, "retry policy schema"):
                    self.verify()
        self.budget["retry_policy_schema"] = SCHEMA
        with self.assertRaisesRegex(ValueError, "retry policy schema"):
            self.verify(self.legacy_adapter)

    def test_new_source_cannot_omit_budget_or_both_trace_fields(self):
        self.fixture.save(self.budget)
        path = self.case.task / "model-budget.json"
        path.unlink()
        with self.assertRaisesRegex(ValueError, "provider trace"):
            verify_round._research_provider_trace(self.case.task, "query", self.case.state, self.case.launch, self.adapter)
        self.budget.pop("provider_attempts")
        self.budget.pop("provider_retries")
        with self.assertRaisesRegex(ValueError, "provider trace"):
            self.verify()

    def test_every_failure_requires_all_four_machine_fields(self):
        original = copy.deepcopy(self.budget["provider_attempts"][0])
        for field in ("exception_chain_types", "exception_chain_truncated", "deadline_origin", "cancellation_in_chain"):
            with self.subTest(field=field):
                self.budget["provider_attempts"][0] = copy.deepcopy(original)
                del self.budget["provider_attempts"][0][field]
                with self.assertRaisesRegex(ValueError, "exception-chain metadata"):
                    self.verify()

    def test_chain_metadata_is_bounded_typed_and_consistent_with_top_error(self):
        original = copy.deepcopy(self.budget["provider_attempts"][0])
        mutations = [
            {"exception_chain_types": []}, {"exception_chain_types": ["APITimeoutError"] * 33},
            {"exception_chain_types": "APITimeoutError"}, {"exception_chain_types": [7]},
            {"exception_chain_types": ["APITimeoutError: private message"]},
            {"exception_chain_types": ["TimeoutError"]}, {"exception_chain_truncated": 1},
            {"cancellation_in_chain": "false"}, {"deadline_origin": "task"},
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                self.budget["provider_attempts"][0] = {**copy.deepcopy(original), **mutation}
                with self.assertRaisesRegex(ValueError, "exception-chain metadata"):
                    self.verify()

    def test_cancelled_attempt_keeps_valid_types_without_requiring_error_only_fields(self):
        row = self.budget["provider_attempts"][1]
        row.update(status="cancelled", exception_chain_types=["GeneratorExit"],
                   exception_chain_truncated=False, deadline_origin=None, cancellation_in_chain=True)
        self.assertFalse(verify_round._provider_exception_metadata(row))
        with self.assertRaisesRegex(ValueError, "cancelled physical request"):
            self.verify()

    def test_enclosing_deadline_wins_even_with_sdk_wrapper_request_type_or_server_status(self):
        for chain, status in ((["ModelDeadlineExceeded"], None),
                              (["APITimeoutError", "ModelDeadlineExceeded"], None),
                              (["InternalServerError", "ModelRequestDeadlineExceeded", "ModelDeadlineExceeded"], 503)):
            with self.subTest(chain=chain):
                self.failure(chain, origin="enclosing", status=status)
                with self.assertRaisesRegex(ValueError, "allowed transient failure"):
                    self.verify()

    def test_origin_cannot_hide_enclosing_type_or_invent_request_deadline(self):
        for chain, origin in ((["APITimeoutError", "ModelDeadlineExceeded"], "request"),
                              (["APITimeoutError", "ModelDeadlineExceeded"], None),
                              (["APITimeoutError"], "request"),
                              (["APITimeoutError"], "enclosing"),
                              (["APITimeoutError", "ModelRequestDeadlineExceeded"], None)):
            with self.subTest(chain=chain, origin=origin):
                self.failure(chain, origin=origin)
                with self.assertRaisesRegex(ValueError, "exception-chain metadata"):
                    self.verify()

    def test_cancellation_and_truncated_chain_override_request_retry(self):
        for chain, cancelled, truncated in (
                (["APITimeoutError", "ModelRequestDeadlineExceeded", "KeyboardInterrupt"], True, False),
                (["APITimeoutError", "ModelRequestDeadlineExceeded", "SystemExit"], False, False),
                (["APITimeoutError", "ModelRequestDeadlineExceeded", "GeneratorExit"], True, False),
                (["APITimeoutError", "ModelRequestDeadlineExceeded"], True, False),
                (["APITimeoutError", "ModelRequestDeadlineExceeded"], False, True)):
            with self.subTest(chain=chain, cancelled=cancelled, truncated=truncated):
                self.failure(chain, origin="request", cancelled=cancelled, truncated=truncated)
                with self.assertRaisesRegex(ValueError, "allowed transient failure|exception-chain metadata"):
                    self.verify()

    def test_v2_timeout_caps_bind_saved_kwargs_and_each_absolute_guard(self):
        for sdk_timeout, absolute, top in ((120, 120, 120), (118, 120, 120), (119, 121, 120),
                                          (119, 119, 120), (119, 120, 121), (119, True, 120)):
            with self.subTest(sdk_timeout=sdk_timeout, absolute=absolute, top=top):
                for index, row in enumerate(self.budget["provider_attempts"]):
                    self.fixture.rewrite_kwargs(self.budget, index, lambda kwargs: kwargs.update(timeout=sdk_timeout))
                    row["absolute_timeout_seconds"] = absolute
                self.budget["request_timeout_seconds"] = top
                self.budget["provider_retries"][0]["request_sha256"] = self.budget["provider_attempts"][0]["request_sha256"]
                with self.assertRaisesRegex(ValueError, "120/119-second"):
                    self.verify()

    def test_partial_stream_and_unknown_errors_cannot_use_own_deadline_permission(self):
        for index, row in enumerate(self.budget["provider_attempts"]):
            self.fixture.rewrite_kwargs(self.budget, index, lambda kwargs: kwargs.update(stream=True))
            row["stream"] = True
            path = self.case.task / "model-usage" / f"call-{index + 1}.json"
            usage = common.read(path)
            usage["stream"] = True
            common.write(path, usage)
        self.budget["provider_retries"][0]["request_sha256"] = self.budget["provider_attempts"][0]["request_sha256"]
        self.failure(["APITimeoutError", "ModelRequestDeadlineExceeded"], origin="request")
        self.budget["provider_attempts"][0].update(failure_stage="stream", delivered_chunks=1)
        with self.assertRaisesRegex(ValueError, "before any delivered chunk"):
            self.verify()
        self.budget["provider_attempts"][0]["delivered_chunks"] = 0
        for chain, origin in ((["RuntimeError"], None),
                              (["RuntimeError", "ModelRequestDeadlineExceeded"], "request")):
            with self.subTest(chain=chain):
                self.failure(chain, origin=origin)
                with self.assertRaisesRegex(ValueError, "allowed transient failure"):
                    self.verify()

    def test_optimizer_in_research_task_retains_sdk_120_and_has_no_retry_link(self):
        request_path = self.case.task / "model-requests/request-003.json"
        kwargs = {"model": "offline-model", "messages": [], "timeout": 120, "max_tokens": 16000, "stream": False}
        request_path.write_text(self.base_adapter.canonical(kwargs), encoding="utf-8")
        usage = {"call_id": "call-3", "agent": "keyword_optimizer", "run_id": self.case.launch["workflow_id"],
                 "status": "completed", "stream": False, "started_at": "2026-10-03T00:00:06+00:00",
                 "ended_at": "2026-10-03T00:00:07+00:00", "usage_present": True, "total_tokens": 5}
        common.write(self.case.task / "model-usage/call-3.json", usage)
        self.budget["provider_attempts"].append({"attempt": 3, "agent": "keyword_optimizer",
            "request_sha256": common.sha(request_path), "request_record": self.base_adapter.file_record(request_path),
            "input_record": None, "usage_call_id": "call-3", "status": "completed", "stream": False,
            "delivered_chunks": 0, "absolute_timeout_seconds": 120})
        self.budget.update(attempts=3, agents={"query": 2, "keyword_optimizer": 1})
        metrics, completed = self.verify()
        self.assertEqual(metrics["provider_attempts_verified"], 3)
        self.assertEqual(metrics["transient_retries_verified"], 1)
        self.assertEqual(len(completed), 1)
        self.fixture.rewrite_kwargs(self.budget, 2, lambda data: data.update(timeout=119))
        with self.assertRaisesRegex(ValueError, "120/119-second"):
            self.verify()

    def test_started_sdk_stream_uses_only_successful_local_close_for_chronology(self):
        path = self.closed_started_stream()
        original = path.read_bytes()
        metrics, _ = self.verify()
        self.assertEqual(metrics["adapter_closed_started_usage_records"], 1)
        self.assertEqual(metrics["adapter_closed_started_usage_end_evidence"], [{
            "attempt": 1, "usage_call_id": "call-1", "stream_closed_at": "2026-10-03T00:00:03+00:00",
            "scope": "adapter local stream close; not SDK completion"}])
        self.assertEqual(metrics["calls_missing_usage_preserved"], 1)
        self.assertEqual(path.read_bytes(), original)
        self.assertEqual(common.read(path)["status"], "started")
        self.assertNotIn("ended_at", common.read(path))

    def test_started_close_substitute_requires_exact_scope_and_successful_close_evidence(self):
        path = self.closed_started_stream()
        row = copy.deepcopy(self.budget["provider_attempts"][0])
        call = common.read(path)
        cases = [
            ({"stream_closed": False}, {}), ({"stream_closed": "true"}, {}),
            ({"stream_closed_at": None}, {}), ({"stream_closed_at": "not-a-timestamp"}, {}),
            ({"stream_closed_at": "2026-10-03T00:00:01+00:00"}, {}),
            ({"stream_closed_at": "2026-10-03T00:00:05+00:00"}, {}),
            ({"stream_closed_at": "2026-10-03T00:00:03"}, {}),
            ({"close_error_type": "TimeoutError"}, {}), ({"close_error_type": None}, {}),
            ({"failure_stage": "stream"}, {}), ({"delivered_chunks": 1}, {}),
            ({"retryable": False}, {}), ({"status": "cancelled"}, {}),
            ({"exception_chain_types": ["ModelDeadlineExceeded"], "error_type": "ModelDeadlineExceeded",
              "deadline_origin": "enclosing"}, {}),
            ({"exception_chain_truncated": True}, {}), ({"cancellation_in_chain": True}, {}),
            ({}, {"ended_at": "2026-10-03T00:00:03+00:00"}),
            ({}, {"usage_present": True, "total_tokens": 7}),
            ({}, {"status": "error"}), ({}, {"status": "completed"}),
        ]
        for row_changes, call_changes in cases:
            with self.subTest(row=row_changes, call=call_changes):
                self.budget["provider_attempts"][0] = {**copy.deepcopy(row), **row_changes}
                common.write(path, {**copy.deepcopy(call), **call_changes})
                with self.assertRaises(ValueError):
                    self.verify()
        for missing in ("stream_closed", "stream_closed_at"):
            with self.subTest(missing=missing):
                self.budget["provider_attempts"][0] = copy.deepcopy(row)
                self.budget["provider_attempts"][0].pop(missing)
                common.write(path, call)
                with self.assertRaises(ValueError):
                    self.verify()

    def test_actual_recorder_late_stream_recovery_preserves_started_bytes_and_verifies(self):
        fixture_path = Path(__file__).resolve().parents[2] / "integrations/bettafish/tests/test_provider_attempt_deadline.py"
        actual = common.module("offline_deadline_verifier_actual_recorder", fixture_path)
        case = actual.ProviderAttemptDeadlineTests(methodName="runTest")
        case.setUp()
        self.addCleanup(case.doCleanups)
        timers = actual.Timers()
        abandoned = actual.fixtures.ScriptedStream([actual.fixtures.chunk(content="late", finish="stop")])
        recovered = actual.fixtures.ScriptedStream([actual.fixtures.chunk(content="recovered", finish="stop")])

        def late_creation():
            try:
                timers.expire()
            except Exception:
                return abandoned

        def retry_creation():
            self.assertEqual(abandoned.close_count, 1)
            return recovered

        session = case.session([late_creation, retry_creation])
        input_path = session.folder / "model-inputs/decision-001.json"
        common.write(input_path, {"system": "return JSON only", "user": "fixed time prefix and frozen evidence"})
        session.record = actual.adapter.file_record(input_path)
        with actual.adapter.model_call_reservation(0, "initial-decision"):
            response = case.invoke(session, timers, stream=True)
        self.assertEqual(response[0].choices[0].delta.content, "recovered")
        data, usages = case.fixture.assert_ledger(session, 2, 1)
        first_path = session.folder / "model-usage" / (usages[0]["call_id"] + ".json")
        original = first_path.read_bytes()
        self.assertEqual(usages[0]["status"], "started")
        self.assertNotIn("ended_at", usages[0])
        state = {"research_audit": {"decision_requests": [session.record]}}
        launch = {"workflow_id": "offline-provider-recovery", "max_output_tokens": 16000}
        metrics, completed = verify_round._research_provider_trace(session.folder, "query", state, launch, actual.adapter)
        self.assertEqual(metrics["adapter_closed_started_usage_records"], 1)
        self.assertEqual(metrics["calls_missing_usage_preserved"], 2)
        self.assertEqual((data["attempts"], len(completed), abandoned.close_count, recovered.close_count), (2, 1, 1, 1))
        self.assertEqual(first_path.read_bytes(), original)

    def test_legacy_started_usage_cannot_use_new_local_close_substitution(self):
        self.closed_started_stream()
        self.budget.pop("retry_policy_schema")
        with self.assertRaisesRegex(ValueError, "Physical usage identity|chronology"):
            self.verify(self.legacy_adapter)

    def test_legacy_source_keeps_original_v1_timeout_and_no_chain_metadata_rules(self):
        self.budget.pop("retry_policy_schema")
        for index, row in enumerate(self.budget["provider_attempts"]):
            self.fixture.rewrite_kwargs(self.budget, index, lambda kwargs: kwargs.update(timeout=120))
            row.pop("absolute_timeout_seconds", None)
            for key in ("exception_chain_types", "exception_chain_truncated", "deadline_origin", "cancellation_in_chain"):
                row.pop(key, None)
        self.budget["provider_retries"][0]["request_sha256"] = self.budget["provider_attempts"][0]["request_sha256"]
        metrics, _ = self.verify(self.legacy_adapter)
        self.assertEqual((metrics["provider_attempts_verified"], metrics["transient_retries_verified"]), (2, 1))
        self.assertNotIn("deadline_origin_metadata_verified", metrics)


if __name__ == "__main__":
    unittest.main(verbosity=2)
