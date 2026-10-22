"""Offline request-deadline recovery; all SDK responses and clocks are synthetic.

The real ModelBudget, model_deadline and usage recorder run with a socket-free
SDK and simulated signal timers. No real model, source archive or launch changes.
"""
import copy
from datetime import datetime
import hashlib
import importlib.util
import json
import os
import signal as native_signal
import time as native_time
import types
import unittest
from pathlib import Path
from unittest.mock import Mock, patch


_spec = importlib.util.spec_from_file_location(
    "deadline_recovery_fixtures", Path(__file__).with_name("test_provider_transient_recovery.py"))
fixtures = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(fixtures)
adapter = fixtures.adapter


class APITimeoutError(Exception):
    """Socket-free equivalent of the SDK's timeout wrapper."""


class Timers:
    """Drive the production context manager's actual installed alarm handler."""
    SIGALRM, SIGINT, ITIMER_REAL = 14, 2, 0
    SIG_DFL, SIG_IGN = 0, 1

    def __init__(self, outer_seconds=None):
        self.now = 0.0
        self.alarm_at = outer_seconds
        self.interval = 0.0
        self.handlers = {self.SIGALRM: self.SIG_DFL, self.SIGINT: self.interrupt}
        self.installed_delays = []

    @staticmethod
    def interrupt(_signum, _frame):
        raise KeyboardInterrupt()

    def getsignal(self, signum):
        return self.handlers.get(signum, self.SIG_DFL)

    def signal(self, signum, handler):
        previous = self.getsignal(signum)
        self.handlers[signum] = handler
        return previous

    def getitimer(self, _which):
        return (max(0.0, self.alarm_at - self.now) if self.alarm_at is not None else 0.0,
                self.interval)

    def setitimer(self, _which, seconds, interval=0.0):
        previous = self.getitimer(_which)
        self.alarm_at = self.now + seconds if seconds else None
        self.interval = interval
        self.installed_delays.append(seconds)
        return previous

    def expire(self):
        if self.alarm_at is None:
            raise AssertionError("provider did not receive an absolute alarm")
        self.now = self.alarm_at + 0.0001
        self.alarm_at = None
        handler = self.getsignal(self.SIGALRM)
        if not callable(handler):
            raise AssertionError("production alarm handler was not installed")
        handler(self.SIGALRM, None)

    def sdk_timeout(self):
        try:
            self.expire()
        except Exception as expired:
            raise APITimeoutError("synthetic SDK wraps an expired timer") from expired
        raise AssertionError("deadline alarm did not interrupt the SDK")

    def sdk_timeout_context(self):
        try:
            self.expire()
        except Exception:
            raise APITimeoutError("synthetic SDK context wraps an expired timer") from None
        raise AssertionError("deadline alarm did not interrupt the SDK")


class ProviderAttemptDeadlineTests(unittest.TestCase):
    def setUp(self):
        self.fixture = fixtures.ProviderTransientRecoveryTests(
            "test_research_roles_retry_same_complete_request_once_with_actual_usage_binding")
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)

    def session(self, actions, *, role="query", limit=7, timeout=120):
        session = self.fixture.session([], role=role, limit=limit)
        session.budget.timeout = timeout
        # The node input represents a feedback decision with frozen initial state.
        input_path = session.folder / "model-input.json"
        input_path.write_text(json.dumps({"system": "return JSON only", "user": json.dumps({
            "topic": "offline topic", "title": "initial angle", "content": "initial scope",
            "paragraph_latest_state": "frozen initial analysis",
            "forum_feedback": "frozen feedback requiring a specific evidence check"})}), encoding="utf-8")
        session.record = adapter.file_record(input_path)
        actions = iter(actions)

        def provider(**kwargs):
            session.calls.append(copy.deepcopy(kwargs))
            action = next(actions)
            if isinstance(action, BaseException):
                raise action
            return action() if callable(action) else action

        session.client.chat.completions.create = Mock(side_effect=provider)
        return session

    def invoke(self, session, timers=None, *, stream=False):
        if timers is None:
            return self.fixture.invoke(session, stream=stream)
        with patch.object(adapter, "signal", timers), \
                patch.object(adapter, "time", types.SimpleNamespace(monotonic=lambda: timers.now)):
            return self.fixture.invoke(session, stream=stream)

    def assert_retry_binding(self, session):
        self.fixture.assert_retry(session)
        data = adapter.read_json(session.budget.path)
        failed = data["provider_attempts"][0]
        self.assertEqual(failed["failure_stage"], "request")
        self.assertEqual(failed["delivered_chunks"], 0)
        self.assertEqual(failed.get("deadline_origin"), "request")
        self.assertIn("APITimeoutError", failed.get("exception_chain_types", []))
        self.assertIn("ModelRequestDeadlineExceeded", failed.get("exception_chain_types", []))
        self.assertEqual(session.calls[0]["timeout"], session.calls[1]["timeout"])

    def test_feedback_query_media_insight_sdk_wrapped_own_alarm_recovers_once(self):
        for role in ("query", "media", "insight"):
            for link in ("cause", "context"):
                with self.subTest(role=role, exception_link=link):
                    timers = Timers()
                    failure = timers.sdk_timeout if link == "cause" else timers.sdk_timeout_context
                    session = self.session([failure, fixtures.completion()], role=role)
                    response = self.invoke(session, timers)
                    self.assertEqual(response.choices[0].finish_reason, "stop")
                    self.assert_retry_binding(session)
                    self.assertEqual(session.calls[0]["timeout"], 119)
                    self.assertEqual(adapter.read_json(session.budget.path)["provider_attempts"][0][
                        "absolute_timeout_seconds"], 120)

    def test_execute_live_feedback_enables_recovery_for_all_three_roles(self):
        for role in ("query", "media", "insight"):
            with self.subTest(role=role):
                timers = Timers()
                session = self.session([timers.sdk_timeout, fixtures.completion()], role=role)
                folder = session.folder
                initial_path, forum_path = folder / "initial.json", folder / "forum.md"
                adapter.atomic_json(initial_path, {"parameters": {"research_phase": "initial"},
                    "research_state": {"paragraphs": [], "fixture": "frozen initial state"}})
                forum_path.write_text("frozen forum advice", encoding="utf-8")
                comments_path, environment_path = folder / "comments.json", folder / "offline.env"
                comments_path.write_text("[]", encoding="utf-8")
                environment_path.write_text("OFFLINE_FIXTURE=1\n", encoding="utf-8")
                dependencies = {role: {"files": {"state": adapter.file_record(initial_path)}},
                                "forum": {"files": {"report": adapter.file_record(forum_path)}}}
                payload = {"topic": "offline feedback topic", "research_phase": "refinement",
                    "max_model_requests": 7, "model_timeout_seconds": 120,
                    "max_output_tokens": 16000, "fixed_topic_search": False}
                args = types.SimpleNamespace(role=role, mode="live", source_dir=str(folder),
                    run_dir=str(folder / "runs"), input_dir=str(folder), env_file=str(environment_path))
                request = {"workflow_id": "offline-feedback", "task_id": role + "-feedback"}

                def boundary(_source_dir, budget, task_dir, _request, _payload):
                    session.budget, session.folder = budget, task_dir
                    session.bounded = budget.wrap(fixtures.usage.recorded_completion)
                    os.environ["LLM_USAGE_DIR"] = str(task_dir / "model-usage")
                    return None

                def run_role(actual_args, _request, _payload, task_dir, *_rest):
                    self.assertEqual(actual_args.initial_research_state["fixture"], "frozen initial state")
                    self.assertEqual(actual_args.forum_feedback, "frozen forum advice")
                    response = self.fixture.invoke(session)
                    self.assertEqual(response.choices[0].finish_reason, "stop")
                    proof = task_dir / "offline-proof.txt"
                    proof.write_text("synthetic completed feedback", encoding="utf-8")
                    return {"files": {"proof": adapter.file_record(proof)}}

                with patch.object(adapter, "signal", timers), \
                        patch.object(adapter, "time", types.SimpleNamespace(monotonic=lambda: timers.now)), \
                        patch.object(adapter, "validate_request", return_value=payload), \
                        patch.object(adapter, "dependency_manifests", return_value=dependencies), \
                        patch.object(adapter, "load_resume_seed", return_value=None), \
                        patch.object(adapter, "fresh_file_sample", return_value=None), \
                        patch.object(adapter, "load_environment", return_value=[]), \
                        patch.object(adapter, "install_live_boundary", side_effect=boundary), \
                        patch.object(adapter, "_run_role", side_effect=run_role):
                    result = adapter.execute(args, request)
                self.assertEqual(result["task_id"], role + "-feedback")
                self.assertTrue(session.budget.data["retry_transient"])
                self.assert_retry_binding(session)

    def test_second_own_deadline_stops_without_a_third_physical_request(self):
        timers = Timers()
        session = self.session([timers.sdk_timeout, timers.sdk_timeout, fixtures.completion()])
        with self.assertRaises(adapter.ModelCallStopped):
            self.invoke(session, timers)
        data, rows = self.fixture.assert_ledger(session, 2, 2)
        self.assertEqual([row["status"] for row in rows], ["error", "error"])
        self.assertEqual(len(data["provider_retries"]), 1)
        self.assertEqual(session.calls[0], session.calls[1])
        self.assertEqual([row.get("deadline_origin") for row in data["provider_attempts"]],
                         ["request", "request"])

    def test_exhausted_budget_preserves_the_failed_attempt_without_retry(self):
        timers = Timers()
        session = self.session([timers.sdk_timeout, fixtures.completion()], limit=1)
        with self.assertRaises(adapter.ModelCallStopped):
            self.invoke(session, timers)
        data, rows = self.fixture.assert_ledger(session, 1, 1)
        self.assertFalse(rows[0]["usage_present"])
        self.assertEqual(data["provider_retries"], [])
        self.assertTrue(data["provider_attempts"][0]["retryable"])

    def test_earlier_or_equal_enclosing_timer_cannot_be_retried(self):
        for remaining in (1.0, 120.0):
            with self.subTest(outer_remaining=remaining):
                timers = Timers(remaining)
                session = self.session([timers.sdk_timeout, fixtures.completion()])
                with self.assertRaises(adapter.ModelCallStopped) as stopped:
                    self.invoke(session, timers)
                self.assertFalse(stopped.exception.retryable)
                data, _ = self.fixture.assert_ledger(session, 1, 1)
                self.assertEqual(data["provider_retries"], [])
                self.assertEqual(data["provider_attempts"][0].get("deadline_origin"), "enclosing")

    def test_delivered_stream_chunk_prevents_retry_of_own_alarm(self):
        timers = Timers()
        partial = fixtures.chunk(content="synthetic delivered content")

        class LateFailure:
            def __iter__(self):
                yield partial
                timers.sdk_timeout()

            def close(self):
                pass

        session = self.session([LateFailure(), fixtures.ScriptedStream([fixtures.chunk(finish="stop")])])
        with patch.object(adapter, "signal", timers), \
                patch.object(adapter, "time", types.SimpleNamespace(monotonic=lambda: timers.now)), \
                adapter.model_input_context(session.record):
            response = session.bounded(session.client, session.role, model="fixture-model", messages=[], stream=True)
            self.assertIs(next(response), partial)
            with self.assertRaises(adapter.ModelCallStopped):
                next(response)
        data, _ = self.fixture.assert_ledger(session, 1, 1)
        self.assertEqual(data["provider_retries"], [])
        self.assertEqual(data["provider_attempts"][0]["delivered_chunks"], 1)

    def test_sigint_and_system_exit_stop_without_retry(self):
        for kind in ("sigint", "system_exit"):
            with self.subTest(cancellation=kind):
                timers = Timers()

                def cancel():
                    if kind == "sigint":
                        timers.getsignal(timers.SIGINT)(timers.SIGINT, None)
                    raise SystemExit(2)

                session = self.session([cancel, fixtures.completion()])
                expected = KeyboardInterrupt if kind == "sigint" else SystemExit
                with self.assertRaises(expected):
                    self.invoke(session, timers)
                data, _ = self.fixture.assert_ledger(session, 1, 0)
                self.assertEqual(data["provider_attempts"][0]["status"], "cancelled")
                self.assertEqual(data["provider_retries"], [])

    def test_own_and_enclosing_or_cancellation_in_chain_always_stop(self):
        for blocker in (adapter.ModelDeadlineExceeded("synthetic enclosing deadline"),
                        KeyboardInterrupt(), SystemExit(2), GeneratorExit()):
            with self.subTest(blocker=type(blocker).__name__):
                timers = Timers()

                def timeout_with_blocker():
                    try:
                        timers.expire()
                    except Exception as own:
                        alarm = own
                    # Raise outside the except suite so Python cannot overwrite
                    # the separate cancellation context with the own alarm.
                    error = APITimeoutError("synthetic SDK mixed exception graph")
                    error.__cause__ = alarm
                    error.__context__ = blocker
                    raise error

                session = self.session([timeout_with_blocker, fixtures.completion()])
                with self.assertRaises(adapter.ModelCallStopped) as stopped:
                    self.invoke(session, timers)
                self.assertFalse(stopped.exception.retryable)
                data, _ = self.fixture.assert_ledger(session, 1, 1)
                self.assertEqual(data["provider_retries"], [])

    def test_sdk_swallowed_alarm_cannot_return_a_late_completed_response(self):
        timers = Timers()

        def swallowed_timeout():
            try:
                timers.expire()
            except Exception:
                return fixtures.completion("late response must not escape")

        session = self.session([swallowed_timeout, fixtures.completion("on-time recovered response")])
        response = self.invoke(session, timers)
        self.assertEqual(response.choices[0].message.content, "on-time recovered response")
        data, rows = self.fixture.assert_ledger(session, 2, 1)
        self.assertEqual([row["status"] for row in rows], ["completed", "completed"])
        self.assertEqual(data["provider_attempts"][0]["status"], "error")
        self.assertEqual(len(data["provider_retries"]), 1)

    def test_sdk_swallowed_alarm_cannot_yield_a_late_stream_chunk(self):
        timers = Timers()

        class LateChunk:
            def __iter__(self):
                try:
                    timers.expire()
                except Exception:
                    yield fixtures.chunk(content="late chunk must not escape")
                yield fixtures.chunk(finish="stop")

            def close(self):
                pass

        recovered = fixtures.chunk(content="on-time recovered content", finish="stop")
        session = self.session([LateChunk(), fixtures.ScriptedStream([recovered])])
        self.assertEqual(self.invoke(session, timers, stream=True), [recovered])
        data, _ = self.fixture.assert_ledger(session, 2, 1)
        self.assertEqual(data["provider_attempts"][0]["delivered_chunks"], 0)
        self.assertEqual(len(data["provider_retries"]), 1)

    def test_late_sdk_stream_is_closed_exactly_once_before_retry_creation(self):
        timers = Timers()
        abandoned = fixtures.ScriptedStream([fixtures.chunk(content="late stream", finish="stop")])
        recovered = fixtures.ScriptedStream([fixtures.chunk(content="recovered stream", finish="stop")])

        def late_creation():
            try:
                timers.expire()
            except Exception:
                return abandoned

        def retry_creation():
            self.assertEqual(abandoned.close_count, 1, "SDK stream must close before opening its replacement")
            return recovered

        session = self.session([late_creation, retry_creation])
        response = self.invoke(session, timers, stream=True)
        self.assertEqual(response[0].choices[0].delta.content, "recovered stream")
        self.assertEqual(abandoned.close_count, 1)
        self.assertEqual(recovered.close_count, 1)
        data, rows = self.fixture.assert_ledger(session, 2, 1)
        first, second = data["provider_attempts"]
        self.assertEqual(first["failure_stage"], "request")
        self.assertEqual(first["delivered_chunks"], 0)
        # Closing an unconsumed recorder cannot invent SDK completion/usage.
        self.assertEqual(rows[0]["status"], "started")
        self.assertNotIn("ended_at", rows[0])
        self.assertFalse(rows[0]["usage_present"])
        self.assertIs(first["stream_closed"], True)
        closed_at = datetime.fromisoformat(first["stream_closed_at"])
        self.assertGreaterEqual(closed_at, datetime.fromisoformat(rows[0]["started_at"]))
        self.assertLessEqual(closed_at, datetime.fromisoformat(rows[1]["started_at"]))
        self.assertEqual(len(data["provider_retries"]), 1)
        retry = data["provider_retries"][0]
        self.assertEqual((retry["failed_attempt"], retry["retry_attempt"]), (1, 2))
        self.assertEqual(retry["failed_usage_call_id"], rows[0]["call_id"])
        self.assertEqual(retry["retry_usage_call_id"], rows[1]["call_id"])
        self.assertEqual(first["request_sha256"], second["request_sha256"])
        self.assertEqual(retry["request_sha256"], first["request_sha256"])
        for attempt in (first, second):
            request_path = Path(attempt["request_record"]["path"])
            self.assertEqual(hashlib.sha256(request_path.read_bytes()).hexdigest(), attempt["request_sha256"])
            self.assertEqual(attempt["request_record"]["sha256"], attempt["request_sha256"])
            self.assertEqual(attempt["input_record"], session.record)

    def test_sdk_stream_transfer_interruption_closes_raw_before_any_retry(self):
        for close_fails in (False, True):
            with self.subTest(raw_transfer_close_fails=close_fails):
                timers = Timers()

                class TransferSource(fixtures.ScriptedStream):
                    def close(self):
                        super().close()
                        if close_fails:
                            raise RuntimeError("synthetic transfer cleanup failure")

                abandoned = TransferSource([fixtures.chunk(content="unconsumed first stream", finish="stop")])
                recovered = fixtures.ScriptedStream([fixtures.chunk(content="recovered stream", finish="stop")])
                real_owner = adapter._OwnedModelStream
                interrupted = []

                class InterruptFirstTransfer(real_owner):
                    def __init__(self, stream=None, dependencies=None):
                        # Leave the outer owner(None) and all later SDK owners
                        # intact. Inject only at the proven transfer boundary.
                        if stream is abandoned and not interrupted:
                            interrupted.append(True)
                            raise adapter.ModelRequestDeadlineExceeded("synthetic stream ownership transfer deadline")
                        super().__init__(stream, dependencies)

                def retry_creation():
                    self.assertEqual(abandoned.close_count, 1, "unowned SDK stream must close before retry creation")
                    return recovered

                session = self.session([abandoned, retry_creation])
                original_create = session.client.chat.completions.create
                with patch.object(adapter, "_OwnedModelStream", InterruptFirstTransfer):
                    if close_fails:
                        with self.assertRaises(adapter.ModelCallStopped):
                            self.invoke(session, timers, stream=True)
                    else:
                        response = self.invoke(session, timers, stream=True)
                        self.assertEqual(response[0].choices[0].delta.content, "recovered stream")
                self.assertEqual(abandoned.close_count, 1)
                self.assertIs(session.client.chat.completions.create, original_create)
                data, _ = self.fixture.assert_ledger(session, 1 if close_fails else 2, 1)
                self.assertEqual(len(data["provider_retries"]), 0 if close_fails else 1)
                self.assertEqual(data["provider_attempts"][0]["delivered_chunks"], 0)
                self.assertEqual(data["provider_attempts"][0]["failure_stage"], "request")
                self.assertEqual(data["provider_attempts"][0]["deadline_origin"], "request")
                self.assertIn("ModelRequestDeadlineExceeded", data["provider_attempts"][0]["exception_chain_types"])
                if close_fails:
                    self.assertFalse(data["provider_attempts"][0]["retryable"])
                    self.assertIsNot(data["provider_attempts"][0].get("stream_closed"), True)
                    self.assertEqual(data["provider_attempts"][0]["close_error_type"], "RuntimeError")
                else:
                    self.assertEqual(recovered.close_count, 1)

    def close_facade_session(self, close_error, *, stream=None):
        """Expose a close failure at the adapter's recorded-response boundary.

        The actual recorder still creates and binds every usage file. Its
        normally exhausted generator is wrapped to make its close observable.
        """
        raw = stream or fixtures.ScriptedStream([fixtures.chunk(finish="stop"), fixtures.chunk(usage_only=True)])
        session = self.session([raw, fixtures.ScriptedStream([fixtures.chunk(finish="stop")])])
        original = fixtures.usage.recorded_completion
        facades = []

        class CloseFacade:
            def __init__(self, recorded):
                self.recorded, self.close_count = recorded, 0

            def __iter__(self):
                return self

            def __next__(self):
                return next(self.recorded)

            def close(self):
                self.close_count += 1
                self.recorded.close()
                raise close_error

        def recorded_with_close(client, agent, **kwargs):
            facade = CloseFacade(original(client, agent, **kwargs))
            facades.append(facade)
            return facade

        session.bounded = session.budget.wrap(recorded_with_close)
        return session, raw, facades

    def test_close_base_cancellation_after_normal_stop_propagates_and_is_recorded(self):
        for cancellation in (KeyboardInterrupt(), SystemExit(2), GeneratorExit()):
            with self.subTest(close_cancellation=type(cancellation).__name__):
                session, raw, facades = self.close_facade_session(cancellation)
                with self.assertRaises(type(cancellation)):
                    self.invoke(session, Timers(), stream=True)
                data, rows = self.fixture.assert_ledger(session, 1, 0)
                attempt = data["provider_attempts"][0]
                self.assertEqual(attempt["status"], "cancelled")
                self.assertTrue(attempt["cancellation_in_chain"])
                self.assertFalse(attempt["retryable"])
                self.assertIn(type(cancellation).__name__, attempt["exception_chain_types"])
                self.assertEqual(attempt["close_error_type"], type(cancellation).__name__)
                self.assertEqual(rows[0]["status"], "completed")
                self.assertEqual(data["provider_retries"], [])
                self.assertEqual((raw.close_count, facades[0].close_count), (1, 1))

    def test_close_exception_after_normal_stop_is_one_failed_cleanup_without_retry(self):
        session, raw, facades = self.close_facade_session(RuntimeError("synthetic private cleanup detail"))
        with self.assertRaises(adapter.ModelCallStopped) as stopped:
            self.invoke(session, Timers(), stream=True)
        self.assertFalse(stopped.exception.retryable)
        self.assertNotIn("private cleanup detail", str(stopped.exception))
        data, rows = self.fixture.assert_ledger(session, 1, 1)
        attempt = data["provider_attempts"][0]
        self.assertEqual(attempt["status"], "error")
        self.assertEqual(attempt["failure_stage"], "cleanup")
        self.assertEqual(attempt["error_type"], "RuntimeError")
        self.assertFalse(attempt["retryable"])
        self.assertIsNot(attempt.get("stream_closed"), True)
        self.assertNotIn("stream_closed_at", attempt)
        self.assertEqual(data["provider_retries"], [])
        self.assertEqual(rows[0]["status"], "completed")
        self.assertEqual((raw.close_count, facades[0].close_count), (1, 1))

    def test_close_exception_preserves_primary_timeout_and_blocks_retry(self):
        timers = Timers()

        class TimeoutStream:
            close_count = 0

            def __iter__(self):
                timers.sdk_timeout()
                yield fixtures.chunk(finish="stop")

            def close(self):
                self.close_count += 1

        raw = TimeoutStream()
        session, _, facades = self.close_facade_session(RuntimeError("synthetic secondary cleanup failure"), stream=raw)
        with self.assertRaises(adapter.ModelCallStopped):
            self.invoke(session, timers, stream=True)
        data, rows = self.fixture.assert_ledger(session, 1, 1)
        attempt = data["provider_attempts"][0]
        self.assertEqual(attempt["failure_stage"], "stream")
        self.assertEqual(attempt["error_type"], "APITimeoutError")
        self.assertEqual(attempt["deadline_origin"], "request")
        self.assertEqual(attempt["close_error_type"], "RuntimeError")
        self.assertFalse(attempt["retryable"])
        self.assertIsNot(attempt.get("stream_closed"), True)
        self.assertNotIn("stream_closed_at", attempt)
        self.assertEqual(data["provider_retries"], [])
        self.assertEqual(rows[0]["status"], "error")
        self.assertEqual((raw.close_count, facades[0].close_count), (1, 1))

    @unittest.skipUnless(hasattr(native_signal, "setitimer"), "native POSIX timer is unavailable on Windows")
    def test_native_posix_own_alarm_recovery_is_subsecond_and_socket_free(self):
        def native_timeout():
            try:
                native_time.sleep(0.3)
            except Exception as own:
                raise APITimeoutError("synthetic SDK wrapper around native timer") from own
            raise AssertionError("native absolute deadline did not interrupt the SDK stub")

        session = self.session([native_timeout, fixtures.completion()], timeout=0.05)
        self.invoke(session)
        self.assert_retry_binding(session)


if __name__ == "__main__":
    unittest.main()
