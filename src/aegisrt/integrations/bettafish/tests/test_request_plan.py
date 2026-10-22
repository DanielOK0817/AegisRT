"""Reject unusable research budgets before a workflow can contact a provider."""
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("request_plan_runner", ROOT / "runner.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class RequestPlanTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.inputs = self.root / "inputs"
        self.inputs.mkdir()
        for name in ("comments.json", "provenance.json"):
            (self.inputs / name).write_text("{}", encoding="utf-8")
        (self.inputs / "manifest.json").write_text('{"topic":"offline","sample_source":"file"}', encoding="utf-8")
        self.args = SimpleNamespace(
            mode="live", workflow_id="request-plan", input_dir=self.inputs,
            run_dir=self.root / "runs", max_paragraphs=2, max_reflections=1,
            insight_max_reflections=1, fixed_topic_search=False, resume_dir=None,
            model_request_limits=[11, 11, 11, 1, 5], refinement_request_limits=[6, 6, 6],
        )

    def test_balanced_small_plan_keeps_58_and_two_recovery_calls_per_research_task(self):
        plan = runner.model_request_plan(self.args)
        self.assertEqual(sum(row["limit"] for row in plan.values()), 58)
        for role in runner.ROLES[:3]:
            self.assertEqual(plan[role]["required_calls"], 9)
            self.assertEqual(plan[role + "-feedback"]["required_calls"], 4)
            self.assertEqual(plan[role]["recovery_capacity"], 2)
            self.assertEqual(plan[role + "-feedback"]["recovery_capacity"], 2)

    def test_four_call_feedback_configuration_stops_before_runtime_or_model_launch(self):
        self.args.refinement_request_limits[1] = 4
        with patch.object(runner, "make_registry") as registry, \
                patch.object(runner.subprocess, "Popen") as process:
            with self.assertRaisesRegex(ValueError, "media-feedback.*4 required.*recovery"):
                runner.run_workflow(self.args)
        registry.assert_not_called()
        process.assert_not_called()
        self.assertFalse(self.args.run_dir.exists())

    def test_default_budget_remains_223_and_covers_all_declared_research_steps(self):
        self.args.max_paragraphs = 5
        self.args.max_reflections = 2
        self.args.insight_max_reflections = 3
        self.args.model_request_limits = list(runner.LIVE_REQUEST_LIMITS)
        self.args.refinement_request_limits = list(runner.REFINEMENT_REQUEST_LIMITS)
        plan = runner.model_request_plan(self.args)
        self.assertEqual(sum(row["limit"] for row in plan.values()), 223)
        self.assertEqual(plan["query"]["required_calls"], 31)
        self.assertEqual(plan["insight"]["required_calls"], 41)
        self.assertEqual(plan["insight-feedback"]["required_calls"], 10)

    def test_database_insight_includes_keyword_optimization_calls(self):
        # A legacy database freeze can contain both files; they do not make it
        # a fresh file sample without the adapter's manifest marker.
        (self.inputs / "manifest.json").write_text('{"topic":"offline","sample_source":"database"}', encoding="utf-8")
        plan = runner.model_request_plan(self.args)
        self.assertEqual(plan["insight"]["required_calls"], 13)
        self.assertEqual(plan["insight-feedback"]["required_calls"], 6)
        with self.assertRaisesRegex(ValueError, "insight.*recovery"):
            runner.validate_model_request_plan(plan)

    def test_sample_schema_also_marks_frozen_comments(self):
        (self.inputs / "manifest.json").write_text(json.dumps({
            "topic": "offline", "schema": "bettafish.sample-input/v1"}), encoding="utf-8")
        self.assertEqual(runner.model_request_plan(self.args)["insight"]["required_calls"], 9)

    def test_fixed_topic_skips_initial_decision_but_keeps_refinement_decision(self):
        self.args.fixed_topic_search = True
        plan = runner.model_request_plan(self.args)
        self.assertEqual(plan["query"]["required_calls"], 7)
        self.assertEqual(plan["query-feedback"]["required_calls"], 4)

    def test_legacy_database_manifest_without_sample_source_keeps_optimizer_calls(self):
        (self.inputs / "manifest.json").write_text('{"topic":"offline"}', encoding="utf-8")
        plan = runner.model_request_plan(self.args)
        self.assertEqual(plan["insight"]["required_calls"], 13)
        self.assertEqual(plan["insight-feedback"]["required_calls"], 6)

    def test_fixed_topic_database_insight_skips_optimizer_but_keeps_feedback_decision(self):
        (self.inputs / "manifest.json").write_text('{"topic":"offline","sample_source":"database"}', encoding="utf-8")
        self.args.fixed_topic_search = True
        plan = runner.model_request_plan(self.args)
        self.assertEqual(plan["insight"]["required_calls"], 7)
        self.assertEqual(plan["insight-feedback"]["required_calls"], 4)

    def test_reflection_depths_are_independent_and_feedback_still_runs_one_iteration(self):
        self.args.max_reflections = 2
        self.args.insight_max_reflections = 0
        plan = runner.model_request_plan(self.args)
        self.assertEqual(plan["query"]["required_calls"], 13)
        self.assertEqual(plan["media"]["required_calls"], 13)
        self.assertEqual(plan["insight"]["required_calls"], 5)
        self.assertTrue(all(plan[role + "-feedback"]["required_calls"] == 4 for role in runner.ROLES[:3]))

    def test_resume_path_cannot_discount_capacity_before_checkpoint_validation(self):
        self.args.resume_dir = self.root / "unvalidated-seed"
        plan = runner.model_request_plan(self.args)
        self.assertEqual(plan["query"]["required_calls"], 9)
        self.assertEqual(plan["query-feedback"]["required_calls"], 4)
        self.args.refinement_request_limits[0] = 4
        with self.assertRaisesRegex(ValueError, "query-feedback.*recovery"):
            runner.validate_model_request_plan(runner.model_request_plan(self.args))

    def test_report_needs_generation_and_review_and_forum_needs_one_call(self):
        for slot in (3, 4):
            with self.subTest(slot=slot):
                original = self.args.model_request_limits[slot]
                self.args.model_request_limits[slot] = 0 if slot == 3 else 1
                with self.assertRaisesRegex(ValueError, "required"):
                    runner.validate_model_request_plan(runner.model_request_plan(self.args))
                self.args.model_request_limits[slot] = original

    def test_replay_has_zero_provider_requirements(self):
        self.args.mode = "replay"
        plan = runner.model_request_plan(self.args)
        self.assertEqual(len(plan), 5)
        self.assertTrue(all(row["required_calls"] == row["limit"] == 0 for row in plan.values()))
        runner.validate_model_request_plan(plan)


if __name__ == "__main__":
    unittest.main()
