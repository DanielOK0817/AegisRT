"""Counted provider recovery with real usage recording and a socket-free SDK stub.

All token values and responses here are synthetic fixtures, never platform usage.
"""
import copy
from contextlib import contextmanager
from datetime import datetime
import importlib.util
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import types
import unittest
from unittest.mock import Mock, patch


def load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


adapter = load(Path(__file__).resolve().parents[1] / "adapter.py", "provider_recovery_adapter")
SOURCE = Path(__file__).resolve().parents[4] / "reproductions/bettafish-20261001/source"
if not SOURCE.is_dir():
    SOURCE = Path("/app")
usage = load(SOURCE / "utils/llm_usage.py", "provider_recovery_actual_usage")


class ProviderHTTPError(Exception):
    def __init__(self, status):
        super().__init__("synthetic provider HTTP failure")
        self.status_code = status


class ScriptedStream:
    def __init__(self, events):
        self.events = iter(events)
        self.close_count = 0

    def __iter__(self):
        return self

    def __next__(self):
        event = next(self.events)
        if isinstance(event, BaseException):
            raise event
        return event

    def close(self):
        self.close_count += 1


def completion(text='{"paragraph_latest_state":"fixture analysis"}', finish="stop"):
    return types.SimpleNamespace(model="fixture-model", choices=[types.SimpleNamespace(
        finish_reason=finish, message=types.SimpleNamespace(content=text))],
        usage=types.SimpleNamespace(prompt_tokens=3, completion_tokens=2, total_tokens=5))


def chunk(content=None, finish=None, *, usage_only=False):
    return types.SimpleNamespace(model="fixture-model", choices=[] if usage_only else [types.SimpleNamespace(
        finish_reason=finish, delta=types.SimpleNamespace(content=content))],
        usage=types.SimpleNamespace(prompt_tokens=3, completion_tokens=2, total_tokens=5) if usage_only else None)


class ProviderTransientRecoveryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="provider-recovery-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.sequence = 0
        environment = patch.dict(os.environ, {"BETTAFISH_RUN_ID": "offline-provider-recovery"})
        environment.start()
        self.addCleanup(environment.stop)
        network = patch("socket.socket", side_effect=AssertionError("offline recovery test opened a socket"))
        network.start()
        self.addCleanup(network.stop)

    def session(self, events, *, role="query", limit=4, enabled=True):
        self.sequence += 1
        folder = self.root / str(self.sequence)
        folder.mkdir()
        os.environ["LLM_USAGE_DIR"] = str(folder / "model-usage")
        options = {} if enabled is None else {"retry_transient": enabled}
        budget = adapter.ModelBudget(folder / "model-budget.json", "same-task-input", limit, 120, 16000, **options)
        source = folder / "model-input.json"
        source.write_text(json.dumps({"system": "return JSON only", "user": "supplied frozen evidence"}), encoding="utf-8")
        record = adapter.file_record(source)
        events, calls = iter(events), []

        def provider(**kwargs):
            calls.append(copy.deepcopy(kwargs))
            event = next(events)
            if isinstance(event, BaseException):
                raise event
            return event

        client = types.SimpleNamespace(base_url="https://offline.invalid/v1", max_retries=9,
            chat=types.SimpleNamespace(completions=types.SimpleNamespace(create=Mock(side_effect=provider))))
        bounded = budget.wrap(usage.recorded_completion)
        return types.SimpleNamespace(folder=folder, budget=budget, record=record, client=client,
                                     calls=calls, bounded=bounded, role=role)

    def invoke(self, session, *, stream=False):
        with adapter.model_input_context(session.record):
            result = session.bounded(session.client, session.role, model="fixture-model",
                messages=[{"role": "system", "content": "return JSON only"},
                          {"role": "user", "content": "fixed time prefix and frozen evidence"}],
                response_format={"type": "json_object"}, timeout=300, max_tokens=32000,
                max_completion_tokens=32000, temperature=0.2, stream=stream)
            return list(result) if stream else result

    def assert_ledger(self, session, expected_calls, failed=None):
        data = adapter.read_json(session.budget.path)
        self.assertEqual(data["attempts"], expected_calls)
        self.assertEqual(data["agents"], {session.role: expected_calls})
        self.assertEqual(len(session.calls), expected_calls)
        if failed is not None:
            self.assertEqual(data["failed_attempts"], failed)
        entries = data["provider_attempts"]
        files = {path.stem: adapter.read_json(path) for path in (session.folder / "model-usage").glob("*.json")}
        self.assertEqual(len(entries), expected_calls)
        self.assertEqual(len(files), expected_calls)
        self.assertEqual({row["usage_call_id"] for row in entries}, set(files))
        for index, row in enumerate(entries, 1):
            self.assertEqual(row["attempt"], index)
            self.assertEqual(row["agent"], session.role)
            self.assertEqual(row["input_record"], session.record)
            self.assertRegex(row["request_sha256"], re.compile(r"^[a-f0-9]{64}$"))
            self.assertIn(row["status"], ("started", "completed", "error", "cancelled"))
            self.assertEqual(files[row["usage_call_id"]]["agent"], session.role)
            self.assertEqual(files[row["usage_call_id"]]["run_id"], "offline-provider-recovery")
        return data, [files[row["usage_call_id"]] for row in entries]

    def assert_retry(self, session):
        data, rows = self.assert_ledger(session, 2, 1)
        self.assertEqual([row["status"] for row in rows], ["error", "completed"])
        self.assertFalse(rows[0]["usage_present"])
        self.assertTrue(rows[1]["usage_present"])
        self.assertEqual(rows[1]["total_tokens"], 5)  # Synthetic provider payload.
        self.assertEqual(session.calls[0], session.calls[1])
        self.assertEqual(session.client.max_retries, 0)
        self.assertEqual(len(data["provider_retries"]), 1)
        retry = data["provider_retries"][0]
        self.assertEqual((retry["failed_attempt"], retry["retry_attempt"], retry["agent"]), (1, 2, session.role))
        self.assertEqual(retry["input_record"], session.record)
        self.assertEqual(retry["request_sha256"], data["provider_attempts"][0]["request_sha256"])
        self.assertEqual(retry["request_sha256"], data["provider_attempts"][1]["request_sha256"])
        self.assertEqual(retry["failed_usage_call_id"], rows[0]["call_id"])
        self.assertEqual(retry["retry_usage_call_id"], rows[1]["call_id"])

    def test_research_roles_retry_same_complete_request_once_with_actual_usage_binding(self):
        for role in ("query", "media", "insight"):
            with self.subTest(role=role):
                session = self.session([TimeoutError("synthetic SDK timeout"), completion()], role=role)
                self.assertEqual(self.invoke(session).choices[0].message.content, completion().choices[0].message.content)
                self.assert_retry(session)
                self.assertEqual(session.calls[0]["max_tokens"], 16000)
                self.assertEqual(session.calls[0]["timeout"], 119)
                self.assertNotIn("max_completion_tokens", session.calls[0])
                self.assertEqual(session.calls[0]["response_format"], {"type": "json_object"})

    def test_two_temporary_failures_stop_without_a_third_provider_call(self):
        session = self.session([ProviderHTTPError(503), ProviderHTTPError(429), completion()])
        with self.assertRaises(adapter.ModelCallStopped):
            self.invoke(session)
        data, rows = self.assert_ledger(session, 2, 2)
        self.assertEqual([row["status"] for row in rows], ["error", "error"])
        self.assertEqual(len(data["provider_retries"]), 1)
        self.assertEqual(session.calls[0], session.calls[1])

    def test_research_sdk_timeout_precedes_each_unchanged_hard_deadline(self):
        for role in ("query", "media", "insight"):
            with self.subTest(role=role):
                session = self.session([TimeoutError("synthetic SDK I/O timeout"), completion()], role=role)
                clock, guards = {"now": 0.0}, []
                sdk_create = session.client.chat.completions.create

                def timed_create(**kwargs):
                    clock["now"] += kwargs["timeout"]
                    return sdk_create(**kwargs)

                @contextmanager
                def hard_guard(seconds, **options):
                    started = clock["now"]
                    guards.append((started, seconds))
                    try:
                        yield
                    finally:
                        if clock["now"] - started >= seconds:
                            raise adapter.ModelDeadlineExceeded("synthetic hard deadline won the race")

                session.client.chat.completions.create = timed_create
                with patch.object(adapter, "time", types.SimpleNamespace(monotonic=lambda: clock["now"])), \
                        patch.object(adapter, "model_deadline", hard_guard):
                    self.invoke(session)
                self.assert_retry(session)
                self.assertEqual([row["timeout"] for row in session.calls], [119, 119])
                self.assertEqual(guards, [(0.0, 120), (119.0, 120)])
                self.assertEqual(clock["now"], 238)
                self.assertEqual(adapter.read_json(session.budget.path)["request_timeout_seconds"], 120)

    def test_soft_timeout_is_role_scoped_and_sdk_wrapped_local_alarm_cannot_retry(self):
        for role, enabled in (("report", True), ("forum", True), ("keyword_optimizer", True),
                              ("query", False), ("query", None)):
            with self.subTest(role=role, enabled=enabled):
                session = self.session([TimeoutError("synthetic SDK I/O timeout"), completion()], role=role, enabled=enabled)
                guards = []

                @contextmanager
                def hard_guard(seconds, **options):
                    guards.append(seconds)
                    yield

                with patch.object(adapter, "time", types.SimpleNamespace(monotonic=lambda: 10.0)), \
                        patch.object(adapter, "model_deadline", hard_guard):
                    with self.assertRaises(adapter.ModelCallStopped):
                        self.invoke(session)
                self.assert_ledger(session, 1, 1)
                self.assertEqual(session.calls[0]["timeout"], 119 if role == "report" else 120)
                self.assertEqual(guards, [120])

        class APITimeoutError(Exception):
            pass

        wrapped = APITimeoutError("synthetic SDK wrapper")
        wrapped.__cause__ = adapter.ModelDeadlineExceeded("synthetic local alarm")
        session = self.session([wrapped, completion()])
        guards = []

        @contextmanager
        def hard_guard(seconds, **options):
            guards.append(seconds)
            yield

        with patch.object(adapter, "time", types.SimpleNamespace(monotonic=lambda: 10.0)), \
                patch.object(adapter, "model_deadline", hard_guard):
            with self.assertRaises(adapter.ModelCallStopped) as caught:
                self.invoke(session)
        self.assertFalse(caught.exception.retryable)
        data, _ = self.assert_ledger(session, 1, 1)
        self.assertEqual(data["provider_retries"], [])
        self.assertEqual(session.calls[0]["timeout"], 119)
        self.assertEqual(guards, [120])

    def test_remaining_budget_prevents_retry_and_persisted_budget_cannot_reset_it(self):
        session = self.session([TimeoutError("synthetic SDK timeout"), completion()], limit=1)
        with self.assertRaises(adapter.ModelCallStopped):
            self.invoke(session)
        data, _ = self.assert_ledger(session, 1, 1)
        self.assertEqual(data.get("provider_retries", []), [])
        resumed = adapter.ModelBudget(session.budget.path, "same-task-input", 1, 120, 16000, retry_transient=True)
        with self.assertRaisesRegex(adapter.ModelCallStopped, "budget exhausted"):
            resumed.wrap(usage.recorded_completion)(session.client, "query", messages=[], model="fixture-model")
        self.assertEqual(len(session.calls), 1)
        self.assertEqual(adapter.read_json(session.budget.path)["attempts"], 1)

    def test_stream_retry_cannot_consume_the_reserved_required_summary(self):
        failed = ScriptedStream([TimeoutError("offline streamed connection failure")])
        session = self.session([failed, ScriptedStream([chunk("late retry"), chunk(finish="stop")])], limit=2)
        with adapter.model_call_reservation(1, "reflection-decision"), self.assertRaises(adapter.ModelCallStopped):
            self.invoke(session, stream=True)
        data, rows = self.assert_ledger(session, 1, 1)
        self.assertEqual(failed.close_count, 1)
        self.assertEqual(rows[0]["status"], "error")
        self.assertEqual(data["provider_retries"], [])
        self.assertEqual(data["provider_attempts"][0]["retry_blocked_reason"], "reserved_for_required_research_calls")

    def test_keyword_optimizer_cannot_consume_reserved_summary_capacity(self):
        session = self.session([completion()], role="keyword_optimizer", limit=1)
        with adapter.model_call_reservation(1, "reflection-search"), self.assertRaisesRegex(
                adapter.ModelCallStopped, "required research steps"):
            self.invoke(session)
        data = adapter.read_json(session.budget.path)
        self.assertEqual((data["attempts"], data["failed_attempts"]), (0, 0))
        self.assertEqual(data["agents"], {})
        self.assertEqual(data["provider_attempts"], [])
        self.assertEqual(session.calls, [])
        self.assertFalse(list((session.folder / "model-usage").glob("*.json")))
        self.assertEqual(data["blocked_requests"][0]["research_step"], "reflection-search")
        self.assertEqual(data["blocked_requests"][0]["required_calls_after"], 1)
        self.assertEqual(session.client.max_retries, 9, "blocked request modified the uncalled SDK client")

    def test_recovery_policy_change_cannot_reuse_old_saved_budget(self):
        session = self.session([completion()])
        previous = adapter.read_json(session.budget.path)
        previous["retry_policy_schema"] = "bettafish.provider-retry/v2"
        adapter.atomic_json(session.budget.path, previous)
        with self.assertRaisesRegex(adapter.AdapterError, "retry policy version changed"):
            adapter.ModelBudget(session.budget.path, "same-task-input", 4, 120, 16000, retry_transient=True)
        self.assertEqual(adapter.read_json(session.budget.path), previous)
        self.assertEqual(session.calls, [])

    def test_report_forum_optimizer_and_disabled_default_do_not_gain_retries(self):
        for role, enabled in (("report", True), ("forum", True), ("keyword_optimizer", True),
                              ("query", False), ("query", None)):
            with self.subTest(role=role, enabled=enabled):
                session = self.session([ProviderHTTPError(503), completion()], role=role, enabled=enabled)
                with self.assertRaises(adapter.ModelCallStopped):
                    self.invoke(session)
                data, _ = self.assert_ledger(session, 1, 1)
                self.assertEqual(data.get("provider_retries", []), [])

    def test_permanent_and_unknown_provider_failures_are_not_retried(self):
        errors = [ProviderHTTPError(status) for status in (400, 401, 403, 404, "503", True)]
        errors += [RuntimeError("unknown failure"), ValueError("invalid provider input")]
        for error in errors:
            with self.subTest(error_type=type(error).__name__, status=getattr(error, "status_code", None)):
                session = self.session([error, completion()])
                with self.assertRaises(adapter.ModelCallStopped) as caught:
                    self.invoke(session)
                self.assertFalse(caught.exception.retryable)
                data, _ = self.assert_ledger(session, 1, 1)
                self.assertEqual(data.get("provider_retries", []), [])

    def test_local_deadline_in_exception_cause_or_context_cannot_be_retried(self):
        direct = adapter.ModelDeadlineExceeded("local request deadline exceeded")
        caused = ProviderHTTPError(503)
        caused.__cause__ = adapter.ModelDeadlineExceeded("enclosing task deadline exceeded")
        contextual = TimeoutError("SDK-like timeout wrapping task interruption")
        contextual.__context__ = adapter.ModelDeadlineExceeded("local deadline exceeded")
        for error in (direct, caused, contextual):
            with self.subTest(error_type=type(error).__name__):
                session = self.session([error, completion()])
                with self.assertRaises(adapter.ModelCallStopped) as caught:
                    self.invoke(session)
                self.assertFalse(caught.exception.retryable)
                self.assert_ledger(session, 1, 1)

    def test_length_and_other_abnormal_finish_reasons_do_not_retry(self):
        for finish in ("length", "content_filter", None):
            with self.subTest(finish=finish):
                session = self.session([completion(finish=finish), completion()])
                with self.assertRaises(adapter.ModelCallStopped) as caught:
                    self.invoke(session)
                self.assertFalse(caught.exception.retryable)
                self.assert_ledger(session, 1, 1)
        truncated = ScriptedStream([chunk(finish="length")])
        session = self.session([truncated, ScriptedStream([chunk(finish="stop")])])
        with self.assertRaises(adapter.ModelCallStopped):
            self.invoke(session, stream=True)
        self.assert_ledger(session, 1, 1)
        self.assertGreater(truncated.close_count, 0)

    def test_completed_bad_json_is_preserved_and_not_a_transport_retry(self):
        malformed = '{"paragraph_latest_state":"unescaped\ntext"}'
        session = self.session([completion(malformed), completion()])
        text = self.invoke(session).choices[0].message.content
        self.assertEqual(text, malformed)
        with self.assertRaises(adapter.AdapterError):
            adapter.decode_research_summary(text)
        data, rows = self.assert_ledger(session, 1, 0)
        self.assertEqual(rows[0]["status"], "completed")
        self.assertEqual(data.get("provider_retries", []), [])

    def test_keyboard_interrupt_and_system_exit_are_not_retried(self):
        for error in (KeyboardInterrupt(), SystemExit(2)):
            with self.subTest(error_type=type(error).__name__):
                session = self.session([error, completion()])
                with self.assertRaises(type(error)):
                    self.invoke(session)
                data, _ = self.assert_ledger(session, 1)
                self.assertEqual(data.get("provider_retries", []), [])

    def test_stream_failure_before_any_chunk_retries_once_and_closes_both_streams(self):
        failed = ScriptedStream([ProviderHTTPError(503)])
        stopped = chunk(finish="stop")
        usage_tail = chunk(usage_only=True)
        recovered = ScriptedStream([stopped, usage_tail])
        session = self.session([failed, recovered])
        self.assertEqual(self.invoke(session, stream=True), [stopped, usage_tail])
        self.assert_retry(session)
        self.assertGreater(failed.close_count, 0)
        self.assertGreater(recovered.close_count, 0)

    def test_stream_creation_and_iteration_share_one_retry_allowance(self):
        for ordering in ("creation-first", "iteration-first"):
            with self.subTest(ordering=ordering):
                failing = ScriptedStream([ProviderHTTPError(503)])
                events = ([ProviderHTTPError(429), failing, completion()] if ordering == "creation-first"
                          else [failing, ProviderHTTPError(429), completion()])
                session = self.session(events)
                with self.assertRaises(adapter.ModelCallStopped):
                    self.invoke(session, stream=True)
                data, _ = self.assert_ledger(session, 2, 2)
                self.assertEqual(len(data["provider_retries"]), 1)
                self.assertGreater(failing.close_count, 0)

    def test_any_delivered_chunk_disables_retry_even_without_content(self):
        for delivered in (chunk(content="partial"), chunk(content=None), chunk(usage_only=True)):
            with self.subTest(chunk_has_choices=bool(delivered.choices)):
                failed = ScriptedStream([delivered, TimeoutError("synthetic stream timeout")])
                session = self.session([failed, ScriptedStream([chunk(finish="stop")])])
                with adapter.model_input_context(session.record):
                    response = session.bounded(session.client, session.role, model="fixture-model",
                        messages=[], stream=True)
                    self.assertIs(next(response), delivered)
                    with self.assertRaises(adapter.ModelCallStopped):
                        next(response)
                data, _ = self.assert_ledger(session, 1, 1)
                self.assertEqual(data.get("provider_retries", []), [])
                self.assertGreater(failed.close_count, 0)

    def test_stream_eof_and_consumer_close_do_not_create_a_retry(self):
        empty = ScriptedStream([])
        session = self.session([empty, ScriptedStream([chunk(finish="stop")])])
        with self.assertRaises(adapter.ModelCallStopped):
            self.invoke(session, stream=True)
        self.assert_ledger(session, 1, 1)
        self.assertGreater(empty.close_count, 0)
        source = ScriptedStream([chunk(content="partial"), chunk(finish="stop")])
        session = self.session([source, ScriptedStream([chunk(finish="stop")])])
        with adapter.model_input_context(session.record):
            response = session.bounded(session.client, session.role, model="fixture-model", messages=[], stream=True)
            next(response)
            response.close()
        data, _ = self.assert_ledger(session, 1)
        self.assertEqual(data.get("provider_retries", []), [])
        self.assertGreater(source.close_count, 0)

    def test_unconsumed_stream_close_does_not_create_a_provider_request(self):
        source = ScriptedStream([chunk(finish="stop")])
        session = self.session([source])
        with adapter.model_input_context(session.record):
            response = session.bounded(session.client, session.role, model="fixture-model", messages=[], stream=True)
            response.close()
        self.assertEqual(len(session.calls), 0)
        data = adapter.read_json(session.budget.path)
        self.assertEqual(data["attempts"], 0)
        self.assertEqual(data["agents"], {})
        self.assertEqual(data["provider_attempts"], [])
        self.assertEqual(data["provider_retries"], [])
        self.assertEqual(list((session.folder / "model-usage").glob("*.json")), [])
        self.assertEqual(source.close_count, 0)  # The mocked SDK was never invoked.

    def test_consumer_close_failure_is_recorded_and_sanitized_without_retry(self):
        class BrokenCloseStream(ScriptedStream):
            def close(self):
                self.close_count += 1
                raise RuntimeError("synthetic private cleanup detail")

        source = BrokenCloseStream([chunk(content="partial"), chunk(finish="stop")])
        session = self.session([source, ScriptedStream([chunk(finish="stop")])])
        with adapter.model_input_context(session.record):
            response = session.bounded(session.client, session.role, model="fixture-model", messages=[], stream=True)
            next(response)
            with self.assertRaises(adapter.ModelCallStopped) as caught:
                response.close()
        self.assertFalse(caught.exception.retryable)
        self.assertNotIn("private cleanup detail", str(caught.exception))
        data, _ = self.assert_ledger(session, 1, 1)
        self.assertEqual(data["provider_attempts"][0]["status"], "error")
        self.assertEqual(data.get("provider_retries", []), [])
        self.assertGreater(source.close_count, 0)

    def original_client(self, session, package):
        stubs = {"openai": types.SimpleNamespace(OpenAI=Mock(side_effect=AssertionError("real SDK construction forbidden"))),
                 "loguru": types.SimpleNamespace(logger=Mock()), "utils.llm_usage": usage,
                 "retry_helper": types.SimpleNamespace(with_retry=lambda config: lambda function: function,
                                                        LLM_RETRY_CONFIG=types.SimpleNamespace(max_retries=0))}
        with patch.dict(sys.modules, stubs), patch.object(sys, "path", list(sys.path)):
            llms = load(SOURCE / package / "llms/base.py", "provider_recovery_" + package)
        llms.recorded_completion = session.bounded
        clock = Mock()
        clock.now.side_effect = [datetime(2024, 8, 20, 10, 30), datetime(2024, 8, 20, 10, 31)]
        llms.datetime = clock
        client = llms.LLMClient.__new__(llms.LLMClient)
        client.client, client.model_name, client.timeout = session.client, "fixture-model", 300
        client.stream_invoke_to_string = client.invoke  # Actual Query production path.
        return llms, client, clock

    def test_original_three_clients_keep_json_mode_time_prefix_and_output_limit_on_retry(self):
        for role in ("query", "media", "insight"):
            with self.subTest(role=role):
                session = self.session([ProviderHTTPError(503), completion()], role=role)
                package = role.capitalize() + "Engine"
                llms, client, clock = self.original_client(session, package)
                with patch.object(adapter.importlib, "import_module", return_value=llms):
                    adapter.install_research_json_mode(package, {"strict original node JSON prompt"})
                with adapter.model_input_context(session.record):
                    result = client.invoke("strict original node JSON prompt", "frozen original node input")
                self.assertIn("fixture analysis", result)
                self.assert_retry(session)
                self.assertEqual(clock.now.call_count, 1)
                self.assertIn("今天的实际时间是2024年08月20日10时30分", session.calls[0]["messages"][1]["content"])
                self.assertEqual(session.calls[0]["response_format"], {"type": "json_object"})
                self.assertEqual(session.calls[0]["max_tokens"], 16000)

    def original_query_nodes(self, llms):
        prompts = types.SimpleNamespace(SYSTEM_PROMPT_REPORT_STRUCTURE="strict outline JSON",
            SYSTEM_PROMPT_FIRST_SUMMARY="strict summary JSON", SYSTEM_PROMPT_REFLECTION_SUMMARY="strict reflection JSON")
        text = types.SimpleNamespace(**{name: Mock(side_effect=AssertionError("local output repair forbidden"))
            for name in ("remove_reasoning_from_output", "clean_json_tags", "extract_clean_response", "fix_incomplete_json")})
        text.format_search_results_for_prompt = lambda value, *args, **kwargs: json.dumps(value, ensure_ascii=False)
        stubs = {"loguru": types.SimpleNamespace(logger=Mock()), "QueryEngine.llms.base": llms,
            "QueryEngine.state.state": types.SimpleNamespace(State=object), "QueryEngine.prompts": prompts,
            "QueryEngine.utils.text_processing": text,
            "utils.forum_reader": types.SimpleNamespace(get_latest_host_speech=lambda: None,
                                                        format_host_speech_for_prompt=lambda value: "")}
        with patch.dict(sys.modules, stubs), patch.object(sys, "path", list(sys.path)):
            base = load(SOURCE / "QueryEngine/nodes/base_node.py", "QueryEngine.nodes.base_node")
            with patch.dict(sys.modules, {"QueryEngine.nodes.base_node": base}):
                outline = load(SOURCE / "QueryEngine/nodes/report_structure_node.py", "QueryEngine.nodes.report_structure_node")
                summary = load(SOURCE / "QueryEngine/nodes/summary_node.py", "QueryEngine.nodes.summary_node")
        summary.FORUM_READER_AVAILABLE = False
        return outline, summary, prompts

    def test_original_outline_and_summary_nodes_recover_transport_without_regeneration(self):
        outline_rows = [{"title": "original angle", "content": "frozen evidence scope"}]
        for kind, raw in (("outline", json.dumps({"paragraphs": outline_rows})),
                          ("summary", json.dumps({"paragraph_latest_state": "original summary"}))):
            with self.subTest(kind=kind):
                session = self.session([TimeoutError("synthetic SDK timeout"), completion(raw)])
                llms, client, clock = self.original_client(session, "QueryEngine")
                outline, summary, prompts = self.original_query_nodes(llms)
                with patch.object(adapter.importlib, "import_module", return_value=llms):
                    adapter.install_research_json_mode("QueryEngine", set(vars(prompts).values()))
                if kind == "outline":
                    node = outline.ReportStructureNode(client, "original topic")
                    node.process_output = lambda value: adapter.decode_research_structure(value, 1)
                    node._generate_default_structure = Mock(side_effect=AssertionError("default outline forbidden"))
                    invoke = lambda: node.run()
                    expected = outline_rows
                else:
                    node = summary.FirstSummaryNode(client)
                    node.process_output = adapter.decode_research_summary
                    invoke = lambda: node.run({"title": "original title", "content": "original scope",
                        "search_query": "original query", "search_results": [{"url": "https://offline.invalid/source", "content": "original excerpt"}]})
                    expected = "original summary"
                with adapter.model_input_context(session.record):
                    self.assertEqual(invoke(), expected)
                self.assert_retry(session)
                self.assertEqual(clock.now.call_count, 1)
                self.assertNotIn("format_correction", session.calls[1]["messages"][1]["content"])


if __name__ == "__main__":
    unittest.main()
