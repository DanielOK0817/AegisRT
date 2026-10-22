"""Offline provider-search audit fixtures; never call external services."""
import copy
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import verify_round
import workflow_common as common


ROOT = Path(__file__).resolve().parents[2] / "integrations/bettafish"
adapter = common.module("offline_search_verifier_adapter", ROOT / "adapter.py")


class SearchVerifierTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.task = Path(self.temp.name)
        self.prepare()

    def success(self, tool="basic_search_news", *, attempt=1, tool_attempt=1, empty=False, relaxed=False):
        return {"tool": tool, "query": "frozen-query", "parameters": {}, "request_attempt": attempt,
                "tool_attempt": tool_attempt, "response_query": "frozen-query", "error_message": None,
                "results": [] if empty else [{"url": "https://example.com/evidence", "content": "Fixture evidence"}],
                "result_count": 0 if empty else 1, **({"date_constraints_relaxed": True} if relaxed else {})}

    def error(self, tool="basic_search_news", *, attempt=1, tool_attempt=1, relaxed=False):
        return {**self.success(tool, attempt=attempt, tool_attempt=tool_attempt, relaxed=relaxed),
                "response_query": None, "results": None, "result_count": None, "error_message": "Fixture read timeout",
                "error_type": "ReadTimeout", "error_module": "requests.exceptions", "http_status": None, "retryable": True}

    def prepare(self, first=None, date=False, role="query", phase="initial"):
        self.role = role
        baseline_tool = "comprehensive_search" if role == "media" else "basic_search_news"
        first = first or [self.success(baseline_tool)]
        group_count = 1 if phase == "refinement" else 2
        groups = [(index, copy.deepcopy(first) if index == 0 and number == 0 else [self.success(baseline_tool)])
                  for index in range(2) for number in range(group_count)]
        audit = {"phase": phase, "provider_search_failure_boundary": True, "search_responses": [],
                 "paragraphs": [{"index": index, "reflections_executed": 1, "search_calls": 0} for index in range(2)]}
        self.state = {"paragraphs": [{}, {}], "research_audit": audit}
        for index, values in groups:
            for value in values:
                ordinal = len(audit["search_responses"]) + 1
                path = self.task / "search-responses" / f"search-{ordinal:03d}.json"
                common.write(path, value)
                audit["search_responses"].append({**adapter.file_record(path), "paragraph_index": index,
                    "tool": value["tool"], "result_count": value["result_count"],
                    "date_constraints_relaxed": value.get("date_constraints_relaxed", False),
                    "provider_error_present": bool(value.get("error_message") or value.get("response_query") == "搜索失败")})
                audit["paragraphs"][index]["search_calls"] += 1
        if date:
            audit["search_scope_changes"] = [{"paragraph_index": 0, "requested_tool": first[0]["tool"],
                "effective_tool": first[-1]["tool"], "query": first[0]["query"], "requested_start_date": None,
                "requested_end_date": None, "date_constraints_relaxed": True, "search_attempts": len(first),
                "transient_retries": sum(bool(row.get("error_type")) for row in first)}]

    def verify(self):
        return verify_round.verify_research_searches(self.task, self.role, self.state, adapter)

    def change(self, ordinal, update):
        record = self.state["research_audit"]["search_responses"][ordinal - 1]
        path = Path(record["path"])
        value = common.read(path)
        value.update(update)
        common.write(path, value)
        record.update(adapter.file_record(path), tool=value["tool"], result_count=value["result_count"],
                      date_constraints_relaxed=value.get("date_constraints_relaxed", False),
                      provider_error_present=bool(value.get("error_message") or value.get("response_query") == "搜索失败"))

    def test_normal_initial_and_feedback_searches_use_real_nonempty_groups(self):
        with patch("socket.socket", side_effect=AssertionError("Offline search verifier opened a socket")):
            result = self.verify()
        self.assertEqual(result["requests_verified"], 4)
        self.assertEqual(result["search_groups_verified"], 4)
        self.prepare(role="media", phase="refinement")
        # Initial preparation had four fixture files; use a separate owned test directory.
        self.task = self.task / "feedback"
        self.prepare(role="media", phase="refinement")
        self.assertEqual(self.verify()["search_groups_verified"], 2)

    def test_one_transient_error_retries_the_same_tool_query_and_parameters(self):
        self.prepare([self.error(), self.success(attempt=2, tool_attempt=2)])
        result = self.verify()
        self.assertEqual(result["requests_verified"], 5)
        self.assertEqual(result["transient_retries"], 1)

    def test_successful_empty_window_then_transient_fallback_share_one_retry(self):
        first = self.success("search_news_last_week", empty=True)
        error = self.error(attempt=2, relaxed=True)
        final = self.success(attempt=3, tool_attempt=2, relaxed=True)
        self.prepare([first, error, final], date=True)
        result = self.verify()
        self.assertEqual(result["date_relaxations"], 1)
        self.assertEqual(result["transient_retries"], 1)
        self.assertEqual(result["requests_verified"], 6)

    def test_false_boundary_or_missing_archive_cannot_be_accepted(self):
        self.state["research_audit"]["provider_search_failure_boundary"] = False
        with self.assertRaisesRegex(ValueError, "not actually unwrapped"):
            self.verify()
        self.state["research_audit"]["provider_search_failure_boundary"] = True
        Path(self.state["research_audit"]["search_responses"][0]["path"]).unlink()
        with self.assertRaisesRegex(ValueError, "archive is missing"):
            self.verify()

    def test_rehashed_retry_query_cannot_change_the_model_selected_query(self):
        self.prepare([self.error(), self.success(attempt=2, tool_attempt=2)])
        self.change(2, {"query": "different-query", "response_query": "different-query"})
        with self.assertRaisesRegex(ValueError, "changed the actual model-selected query"):
            self.verify()

    def test_provider_failure_cannot_be_disguised_as_a_date_relaxation(self):
        self.prepare([self.error("search_news_last_week"), self.success(attempt=2, relaxed=True)], date=True)
        with self.assertRaisesRegex(ValueError, "changed the tool"):
            self.verify()

    def test_http403_is_permanent_even_when_the_saved_retryable_flag_claims_true(self):
        self.prepare([self.error(), self.success(attempt=2, tool_attempt=2)])
        self.change(1, {"http_status": 403, "error_type": "HTTPError"})
        with self.assertRaisesRegex(ValueError, "permanent or unknown"):
            self.verify()

    def test_a_second_transient_failure_exceeds_the_shared_retry_bound(self):
        self.prepare([self.error(), self.error(attempt=2, tool_attempt=2), self.success(attempt=3, tool_attempt=3)])
        with self.assertRaisesRegex(ValueError, "shared one-error bound"):
            self.verify()

    def test_original_failure_sentinel_with_empty_error_message_still_cannot_pass(self):
        self.change(1, {"response_query": "搜索失败", "results": [], "result_count": 0, "error_message": None})
        with self.assertRaisesRegex(ValueError, "disguised a failure"):
            self.verify()

    def test_attempt_gaps_and_rehashed_paragraph_call_count_are_rejected(self):
        self.change(1, {"request_attempt": 2})
        with self.assertRaisesRegex(ValueError, "gaps"):
            self.verify()
        self.change(1, {"request_attempt": 1})
        self.state["research_audit"]["paragraphs"][0]["search_calls"] += 1
        with self.assertRaisesRegex(ValueError, "does not match"):
            self.verify()

    def test_date_scope_cannot_omit_the_actual_transient_retry(self):
        self.prepare([self.success("search_news_last_week", empty=True), self.error(attempt=2, relaxed=True),
                      self.success(attempt=3, tool_attempt=2, relaxed=True)], date=True)
        self.state["research_audit"]["search_scope_changes"][0]["transient_retries"] = 0
        with self.assertRaisesRegex(ValueError, "differs from the actual bounded"):
            self.verify()


if __name__ == "__main__":
    unittest.main(verbosity=2)
