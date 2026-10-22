"""Offline acceptance-tool tests; no Docker, Runtime or provider is started."""
import contextlib
import io
import json
from pathlib import Path
import shutil
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import bounded_control
import workflow_common as common
import verify_round


class ControlTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.stage = Path(self.temp.name) / common.STAGE_NAME
        frozen = self.stage / "revisions/code-fixture/integration"
        frozen.mkdir(parents=True)
        integration = Path(__file__).resolve().parents[2] / "integrations/bettafish"
        for name in ("runner.py", "adapter.py", "comprehensive_report.py"):
            shutil.copyfile(integration / name, frozen / name)
        records = [{"path": "integration/" + path.name, "sha256": common.sha(path)} for path in frozen.iterdir()]
        runner = common.module("offline_profile_runner", frozen / "runner.py")
        profiles = {}
        for profile in ("small", "default"):
            parameters = common.new_profile_parameters(profile, runner)
            profiles[profile] = {"parameters": parameters, "sha256": common.profile_hash(parameters)}
        common.write(self.stage / "stage-manifest.json", {"current_revision": "code-fixture",
            "revisions": {"code-fixture": records}, "profiles": {"code-fixture": profiles}})

    def frozen_245_common(self):
        path = Path(__file__).resolve().with_name("workflow_common-report-scope-before-291.py")
        self.assertEqual(common.sha(path), "970c0756c6c3a01009453497bd5309f03af289e3da7fa5a6149bffb943387f4d")
        return common.module("offline_control_frozen_cap245", path)

    def frozen_291_common(self):
        path = Path(__file__).resolve().with_name("workflow_common-attempt-deadline-before-331.py")
        self.assertEqual(common.sha(path), "6c2ecb5568a3007201d4e12569f7eff70673f4caa2ded3cdf3b218723d3b3b6c")
        return common.module("offline_control_frozen_cap291", path)

    def charged(self, attempts, cleanup=True, workflow=None, aggregate_limit=70, code_revision=None):
        workflow = workflow or "bf-auto-small-" + "1" * 18
        common.write(self.stage / "private/rounds" / (workflow + ".json"), {
            **common.plan(self.stage, "small"), "workflow_id": workflow, "aggregate_limit": aggregate_limit,
            "code_revision": code_revision or "code-fixture", "started_with_no_task_cache": True})
        folder = self.stage / "private/runs" / workflow
        common.write(folder / (workflow + "-query") / "model-budget.json", {"attempts": attempts})
        common.write(folder / "summary.json", {"status": "error", "elapsed_seconds": 1,
                     "cleanup": {"tasks_terminal": cleanup, "daemon_stopped": cleanup}})
        return folder

    def register_legacy(self, folder, budget, initial, refinement):
        path = self.stage / "private/rounds" / (folder.name + ".json")
        launch = common.read(path)
        launch.pop("profile_configuration_sha256")
        launch.update(max_model_requests=budget, model_request_limits=initial, refinement_request_limits=refinement)
        common.write(path, launch)
        manifest = common.read(self.stage / "stage-manifest.json")
        code = launch["code_revision"]
        if code not in manifest["revisions"]:
            shutil.copytree(self.stage / "revisions/code-fixture", self.stage / "revisions" / code)
            manifest["revisions"][code] = manifest["revisions"]["code-fixture"]
        parameters = {key: launch[key] for key in common.PROFILE_KEYS}
        manifest["profiles"][code] = {"small": {"parameters": parameters, "sha256": common.profile_hash(parameters),
            "legacy_launch": {"workflow_id": folder.name, "sha256": common.sha(path)}}}
        common.write(self.stage / "stage-manifest.json", manifest)
        return path

    def register_previous_strict(self, folder):
        path = self.stage / "private/rounds" / (folder.name + ".json")
        launch = common.read(path)
        launch.update(max_model_requests=67, model_request_limits=[15, 12, 12, 1, 5], refinement_request_limits=[8, 6, 7])
        parameters = {key: launch[key] for key in common.PROFILE_KEYS}
        entry = {"parameters": parameters, "sha256": common.profile_hash(parameters)}
        launch["profile_configuration_sha256"] = entry["sha256"]
        common.write(path, launch)
        manifest = common.read(self.stage / "stage-manifest.json")
        code = launch["code_revision"]
        shutil.copytree(self.stage / "revisions/code-fixture", self.stage / "revisions" / code)
        manifest["revisions"][code] = manifest["revisions"]["code-fixture"]
        manifest["profiles"][code] = {"small": entry}
        common.write(self.stage / "stage-manifest.json", manifest)
        return path

    def test_profiles_are_fresh_nine_task_58_and_223_budgets(self):
        for name, count in (("small", 58), ("default", 223)):
            configuration = common.plan(self.stage, name)
            self.assertEqual(configuration["max_model_requests"], count)
            self.assertEqual(configuration["task_count"], 9)
            command = common.runner_arguments(configuration, "fresh-round")
            self.assertNotIn("--resume-dir", command)
            self.assertNotIn("--fixed-topic-search", command)
            self.assertFalse(any("retained" in value or "editorial" in value for value in command))
            if name == "small":
                self.assertEqual(configuration["model_request_limits"], [11, 11, 11, 1, 5])
                self.assertEqual(configuration["refinement_request_limits"], [6, 6, 6])
                self.assertEqual(configuration["max_paragraphs"], 2)
                self.assertEqual(configuration["max_reflections"], 1)
                self.assertEqual(configuration["insight_max_reflections"], 1)

    def test_smaller_explicit_bound_still_counts_prior_requests(self):
        configuration = common.plan(self.stage, "small")
        self.charged(32)
        self.assertEqual(common.require_capacity(self.stage, configuration, 90), 32)
        self.charged(33)
        with self.assertRaisesRegex(ValueError, "Insufficient aggregate"):
            common.require_capacity(self.stage, configuration, 90)

    def test_small_175_counts_five_old_revisions_117_and_preserves_all_launch_bounds(self):
        configuration = common.plan(self.stage, "small")
        first = self.charged(42, code_revision="code-old-first")
        second = self.charged(41, workflow="bf-auto-small-" + "2" * 18,
                              aggregate_limit=90, code_revision="code-old-second")
        third = self.charged(4, workflow="bf-auto-small-" + "3" * 18,
                             aggregate_limit=170, code_revision="code-old-third")
        fourth = self.charged(21, workflow="bf-auto-small-" + "4" * 18,
                             aggregate_limit=175, code_revision="code-old-fourth")
        paths = [self.register_legacy(folder, budget, initial, refinement)
            for folder, budget, initial, refinement in zip((first, second, third, fourth), (45, 47, 85, 85),
                ([10, 9, 9, 1, 3], [10, 9, 9, 1, 5], [18, 18, 18, 1, 5], [18, 18, 18, 1, 5]),
                ([4, 4, 4], [4, 4, 4], [8, 8, 8], [8, 8, 8]))]
        fifth = self.charged(9, workflow="bf-auto-small-" + "5" * 18,
                             aggregate_limit=175, code_revision="code-old-fifth")
        paths.append(self.register_previous_strict(fifth))
        original = [path.read_bytes() for path in paths]
        self.assertEqual(common.require_capacity(self.stage, configuration, 175), 117)
        self.assertEqual(117 + configuration["max_model_requests"], 175)
        for limit in (70, 90, 170, 174):
            with self.subTest(limit=limit), self.assertRaisesRegex(ValueError, "Insufficient aggregate"):
                common.require_capacity(self.stage, configuration, limit)
        self.assertEqual([path.read_bytes() for path in paths], original)
        self.assertEqual([common.read(path)["aggregate_limit"] for path in paths], [70, 90, 170, 175, 175])
        self.assertEqual([common.read(path)["max_model_requests"] for path in paths], [45, 47, 85, 85, 67])
        for path, expected in zip(paths, (45, 47, 85, 85, 67)):
            config, legacy = common.validate_launch_configuration(self.stage, path)
            self.assertEqual(legacy, expected != 67)
            self.assertEqual(config["max_model_requests"], expected)
            self.assertNotEqual(config["profile_configuration_sha256"], configuration["profile_configuration_sha256"])

    def test_small_aggregate_rejects_missing_bool_over_510_and_exhaustion(self):
        configuration = common.plan(self.stage, "small")
        for limit in (None, True, 0, 511):
            with self.subTest(limit=limit), self.assertRaises(ValueError):
                common.require_capacity(self.stage, configuration, limit)
        self.charged(117)
        self.assertEqual(common.require_capacity(self.stage, configuration, 175), 117)
        self.charged(118)
        with self.assertRaisesRegex(ValueError, "Insufficient aggregate"):
            common.require_capacity(self.stage, configuration, 175)

    def test_authorized_single_round_58_counts_six_prior_revisions_165_without_rewriting_history(self):
        configuration = common.plan(self.stage, "small")
        paths = []
        for index, (calls, limit) in enumerate(zip((42, 41, 4, 21, 9, 48), (70, 90, 170, 175, 175, 175)), 1):
            folder = self.charged(calls, workflow="bf-auto-small-" + str(index) * 18,
                                  aggregate_limit=limit, code_revision="code-old-" + str(index))
            paths.append(self.stage / "private/rounds" / (folder.name + ".json"))
        original = [path.read_bytes() for path in paths]
        self.assertEqual(common.require_capacity(self.stage, configuration, 223), 165)
        self.assertEqual(165 + configuration["max_model_requests"], 223)
        for limit in (175, 222):
            with self.subTest(limit=limit), self.assertRaisesRegex(ValueError, "Insufficient aggregate"):
                common.require_capacity(self.stage, configuration, limit)
        with self.assertRaisesRegex(ValueError, "cannot exceed 510"):
            common.require_capacity(self.stage, configuration, 511)
        self.assertEqual([path.read_bytes() for path in paths], original)

    def test_new_launch_requires_its_exact_profile_hash_and_parameters(self):
        folder = self.charged(0)
        path = self.stage / "private/rounds" / (folder.name + ".json")
        original = common.read(path)
        configuration, legacy = common.validate_launch_configuration(self.stage, path)
        self.assertFalse(legacy)
        self.assertEqual(configuration["max_model_requests"], 58)
        for changed in ({key: value for key, value in original.items() if key != "profile_configuration_sha256"},
                        {**original, "profile_configuration_sha256": "0" * 64},
                        {**original, "model_request_limits": [18, 18, 18, 1, 5]}):
            with self.subTest(changed_hash=changed.get("profile_configuration_sha256")):
                common.write(path, changed)
                with self.assertRaisesRegex(ValueError, "hash|parameters changed"):
                    common.validate_launch_configuration(self.stage, path)

    def test_authorized_eighth_round_counts_seven_prior_187_and_requires_exact_245_capacity(self):
        historical = self.frozen_245_common()
        configuration = historical.plan(self.stage, "small")
        paths = []
        for index, (calls, limit) in enumerate(zip((42, 41, 4, 21, 9, 48, 22),
                                                   (70, 90, 170, 175, 175, 175, 223)), 1):
            folder = self.charged(calls, workflow="bf-auto-small-" + str(index) * 18,
                                  aggregate_limit=limit, code_revision="code-old-" + str(index))
            paths.append(self.stage / "private/rounds" / (folder.name + ".json"))
        originals = [path.read_bytes() for path in paths]
        self.assertEqual(historical.require_capacity(self.stage, configuration, 245), 187)
        self.assertEqual(187 + configuration["max_model_requests"], 245)
        for limit in (223, 244):
            with self.subTest(limit=limit), self.assertRaisesRegex(ValueError, "Insufficient aggregate"):
                historical.require_capacity(self.stage, configuration, limit)
        with self.assertRaisesRegex(ValueError, "cannot exceed 245"):
            historical.require_capacity(self.stage, configuration, 246)
        self.assertEqual([path.read_bytes() for path in paths], originals)
        self.assertEqual([common.read(path)["aggregate_limit"] for path in paths],
                         [70, 90, 170, 175, 175, 175, 223])

    def test_historical_291_counts_eight_prior_233_then_ninth_40_without_resetting_history(self):
        historical = self.frozen_291_common()
        configuration = historical.plan(self.stage, "small")
        paths, budgets = [], []
        for index, (calls, limit) in enumerate(zip((42, 41, 4, 21, 9, 48, 22, 46),
                                                   (70, 90, 170, 175, 175, 175, 223, 245)), 1):
            folder = self.charged(calls, workflow="bf-auto-small-" + str(index) * 18,
                                  aggregate_limit=limit, code_revision="code-old-" + str(index))
            paths.append(self.stage / "private/rounds" / (folder.name + ".json"))
            budgets.append(folder / (folder.name + "-query") / "model-budget.json")
        originals = [path.read_bytes() for path in paths + budgets]
        self.assertEqual(common.profile_spent(self.stage, "small"), 233)
        self.assertEqual(historical.require_capacity(self.stage, configuration, 291), 233)
        self.assertEqual(233 + configuration["max_model_requests"], 291)
        ninth = self.charged(40, workflow="bf-auto-small-" + "9" * 18, aggregate_limit=291)
        ninth_paths = [self.stage / "private/rounds" / (ninth.name + ".json"),
                       ninth / (ninth.name + "-query") / "model-budget.json"]
        ninth_originals = [path.read_bytes() for path in ninth_paths]
        self.assertEqual(common.profile_spent(self.stage, "small"), 273)
        self.assertEqual(273 + configuration["max_model_requests"], 331)
        with self.assertRaisesRegex(ValueError, "Insufficient aggregate"):
            historical.require_capacity(self.stage, configuration, 291)
        with self.assertRaisesRegex(ValueError, "cannot exceed 291"):
            historical.require_capacity(self.stage, configuration, 331)
        self.assertEqual([path.read_bytes() for path in paths + budgets], originals)
        self.assertEqual([path.read_bytes() for path in ninth_paths], ninth_originals)
        self.assertEqual([common.read(path)["aggregate_limit"] for path in paths],
                         [70, 90, 170, 175, 175, 175, 223, 245])

    def test_current_331_counts_nine_prior_273_allows_58_and_rejects_one_extra_request(self):
        configuration = common.plan(self.stage, "small")
        paths = []
        for index, (calls, limit) in enumerate(zip((42, 41, 4, 21, 9, 48, 22, 46, 40),
                                                   (70, 90, 170, 175, 175, 175, 223, 245, 291)), 1):
            folder = self.charged(calls, workflow="bf-auto-small-" + str(index) * 18,
                                  aggregate_limit=limit, code_revision="code-old-" + str(index))
            paths.extend([self.stage / "private/rounds" / (folder.name + ".json"),
                          folder / (folder.name + "-query") / "model-budget.json"])
        originals = [path.read_bytes() for path in paths]
        self.assertEqual(common.profile_spent(self.stage, "small"), 273)
        self.assertEqual(common.require_capacity(self.stage, configuration, 331), 273)
        self.assertEqual(273 + configuration["max_model_requests"], 331)
        with self.assertRaisesRegex(ValueError, "cannot exceed 510"):
            common.require_capacity(self.stage, configuration, 511)
        self.charged(1, workflow="bf-auto-small-" + "a" * 18, aggregate_limit=331)
        self.assertEqual(common.profile_spent(self.stage, "small"), 274)
        self.assertEqual(274 + configuration["max_model_requests"], 332)
        with self.assertRaisesRegex(ValueError, "Insufficient aggregate"):
            common.require_capacity(self.stage, configuration, 331)
        self.assertEqual([path.read_bytes() for path in paths], originals)
        self.assertEqual(common.require_capacity(self.stage, common.plan(self.stage, "default"), 223), 0)

    def test_new_510_bound_with_452_spent_allows_one_58_call_round_without_rewriting_history(self):
        configuration = common.plan(self.stage, "small")
        for index, attempts in enumerate([58] * 7 + [46]):
            self.charged(attempts, workflow="bf-auto-small-" + str(index + 1) * 18,
                         aggregate_limit=469)
        self.assertEqual(common.profile_spent(self.stage, "small"), 452)
        with self.assertRaisesRegex(ValueError, "Insufficient aggregate"):
            common.require_capacity(self.stage, configuration, 469)
        paths = sorted((self.stage / 'private/rounds').glob('*.json'))
        original = [path.read_bytes() for path in paths]
        self.assertEqual(common.require_capacity(self.stage, configuration, 510), 452)
        with self.assertRaisesRegex(ValueError, "Insufficient aggregate"):
            common.require_capacity(self.stage, configuration, 509)
        with self.assertRaisesRegex(ValueError, "cannot exceed 510"):
            common.require_capacity(self.stage, configuration, 511)
        self.assertEqual([path.read_bytes() for path in paths], original)
        self.charged(1, workflow='bf-auto-small-' + 'e' * 18, aggregate_limit=510)
        with self.assertRaisesRegex(ValueError, "Insufficient aggregate"):
            common.require_capacity(self.stage, configuration, 510)

    def test_legacy_launch_external_hash_rejects_any_rewrite(self):
        folder = self.charged(42, code_revision="code-first")
        path = self.register_legacy(folder, 45, [10, 9, 9, 1, 3], [4, 4, 4])
        original = path.read_bytes()
        configuration, legacy = common.validate_launch_configuration(self.stage, path)
        self.assertTrue(legacy)
        self.assertEqual(configuration["max_model_requests"], 45)
        value = common.read(path)
        value["profile_configuration_sha256"] = configuration["profile_configuration_sha256"]
        common.write(path, value)
        with self.assertRaisesRegex(ValueError, "Original legacy launch bytes"):
            common.validate_launch_configuration(self.stage, path)
        path.write_bytes(original)
        self.assertTrue(common.validate_launch_configuration(self.stage, path)[1])

    def test_legacy_only_check_launch_keeps_original_failed_verification_bytes(self):
        folder = self.charged(42, code_revision="code-first")
        path = self.register_legacy(folder, 45, [10, 9, 9, 1, 3], [4, 4, 4])
        verification = folder / "verification.json"
        common.write(verification, {"status": "failed_validation", "original_error": "Declared Report3 was actually capped at2"})
        originals = (path.read_bytes(), verification.read_bytes())
        arguments = ["verify_round.py", "--stage-dir", str(self.stage), "--workflow", folder.name]
        output = io.StringIO()
        with patch.object(sys, "argv", arguments + ["--check-launch"]), contextlib.redirect_stdout(output):
            self.assertEqual(verify_round.main(), 0)
        result = json.loads(output.getvalue())
        self.assertEqual(result["status"], "launch_configuration_validated_only")
        self.assertEqual(result["existing_business_verification_status"], "failed_validation")
        self.assertFalse(result["business_acceptance_passed"])
        with patch.object(sys, "argv", arguments):
            with self.assertRaisesRegex(ValueError, "preserve their original failed verification"):
                verify_round.main()
        self.assertEqual((path.read_bytes(), verification.read_bytes()), originals)

    def test_previous_strict_67_profile_keeps_hash_and_failed_bytes_after_new_revision(self):
        folder = self.charged(9, code_revision="code-previous-strict")
        path = self.register_previous_strict(folder)
        verification = folder / "verification.json"
        common.write(verification, {"status": "failed_run", "original_error": "Query returned non-JSON twice"})
        originals = (path.read_bytes(), verification.read_bytes())
        old = common.profile_record(self.stage, "code-previous-strict", "small")
        self.assertEqual(old["parameters"]["max_model_requests"], 67)
        self.assertEqual(old["parameters"]["model_request_limits"], [15, 12, 12, 1, 5])
        arguments = ["verify_round.py", "--stage-dir", str(self.stage), "--workflow", folder.name]
        output = io.StringIO()
        with patch.object(sys, "argv", arguments + ["--check-launch"]), contextlib.redirect_stdout(output):
            self.assertEqual(verify_round.main(), 0)
        result = json.loads(output.getvalue())
        self.assertFalse(result["legacy_original_launch_sha256_verified"])
        self.assertEqual(result["profile_configuration_sha256"], old["sha256"])
        self.assertEqual(result["existing_business_verification_status"], "failed_run")
        self.assertFalse(result["business_acceptance_passed"])
        with patch.object(sys, "argv", arguments + ["--export"]):
            with self.assertRaisesRegex(ValueError, "Previous revisions only support --check-launch"):
                verify_round.main()
        self.assertEqual((path.read_bytes(), verification.read_bytes()), originals)

    def test_frozen_profile_tampering_and_new_report3_are_rejected(self):
        path = self.stage / "stage-manifest.json"
        value = common.read(path)
        record = value["profiles"]["code-fixture"]["small"]
        record["parameters"]["max_model_requests"] = 85
        common.write(path, value)
        with self.assertRaisesRegex(ValueError, "profile configuration hash"):
            common.plan(self.stage, "small")
        record["parameters"]["model_request_limits"][-1] = 3
        record["sha256"] = common.profile_hash(record["parameters"])
        common.write(path, value)
        with self.assertRaisesRegex(ValueError, "Strict acceptance requires Forum1 and Report5"):
            common.plan(self.stage, "small")

    def test_legacy_revision_cannot_start_another_model_round(self):
        folder = self.charged(42, code_revision="code-first")
        self.register_legacy(folder, 45, [10, 9, 9, 1, 3], [4, 4, 4])
        path = self.stage / "stage-manifest.json"
        value = common.read(path)
        value["current_revision"] = "code-first"
        common.write(path, value)
        with patch.object(bounded_control, "os", SimpleNamespace(name="posix")), \
                patch.object(bounded_control, "REMOTE_ROOT", self.stage.parent), \
                patch.object(bounded_control.subprocess, "Popen") as model_start:
            with self.assertRaisesRegex(ValueError, "Historical revisions only support read-only"):
                bounded_control.start(self.stage, "small", 175)
            model_start.assert_not_called()

    def test_old_65_call_ledger_is_outside_the_new_charge_scope(self):
        old = Path(self.temp.name) / "core-restoration-20261003/private/old-success/model-budget.json"
        common.write(old, {"attempts": 65})
        before = old.read_bytes()
        self.assertEqual(common.profile_spent(self.stage, "small"), 0)
        bounded_control.status(self.stage)
        self.assertEqual(old.read_bytes(), before)

    def test_unfinished_or_unclean_round_blocks_start(self):
        self.charged(1, cleanup=False)
        with self.assertRaisesRegex(ValueError, "uncertain cleanup"):
            bounded_control.ensure_no_active_round(self.stage)
        self.charged(1, cleanup=True)
        bounded_control.ensure_no_active_round(self.stage)

    def test_unknown_summary_blocks_start_even_when_no_calls_are_recorded(self):
        folder = self.charged(0)
        (folder / "summary.json").unlink()
        with self.assertRaisesRegex(ValueError, "uncertain cleanup"):
            bounded_control.ensure_no_active_round(self.stage)

    def test_frozen_revision_tampering_is_detected(self):
        path = self.stage / "revisions/code-fixture/integration/runner.py"
        path.write_text("changed", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "Frozen code hash changed"):
            common.plan(self.stage, "small")

    def test_start_outside_the_new_remote_stage_rejects_before_mutation(self):
        with self.assertRaisesRegex(ValueError, "new remote acceptance stage"):
            bounded_control.start(self.stage, "small", 70)
        self.assertFalse((self.stage / "private").exists())

    def test_default_budget_is_separate_and_explicit(self):
        configuration = common.plan(self.stage, "default")
        self.assertEqual(common.require_capacity(self.stage, configuration, 223), 0)
        for limit in (None, 221, 222, 224, 245):
            with self.subTest(limit=limit), self.assertRaises(ValueError):
                common.require_capacity(self.stage, configuration, limit)

    def test_default_gate_still_requires_same_revision_fresh_small_pass(self):
        configuration = common.plan(self.stage, "default")
        original_open = type(self.stage).open
        def closed_offline_lock(path, *args, **kwargs):
            # Production runs only on POSIX. Close its test lock before unlinking on Windows.
            if path.name == "start.lock" and args == ("x",):
                with original_open(path, *args, **kwargs):
                    pass
                return contextlib.nullcontext()
            return original_open(path, *args, **kwargs)
        valid = {"status": "passed", "profile": "small", "code_revision": "code-fixture", "fresh_from_zero": True}
        cases = [({**valid, "code_revision": "code-old"}, False),
                 ({**valid, "status": "failed_validation"}, False),
                 ({**valid, "profile": "default"}, False),
                 ({**valid, "fresh_from_zero": False}, False), (valid, True)]
        for value, reaches_preflight in cases:
            with self.subTest(value=value):
                common.write(self.stage / "private/runs/gate-fixture/verification.json", value)
                with patch.object(bounded_control, "os", SimpleNamespace(name="posix")), \
                        patch.object(bounded_control, "REMOTE_ROOT", self.stage.parent), \
                        patch.object(bounded_control, "plan", return_value=configuration), \
                        patch.object(type(self.stage), "open", closed_offline_lock), \
                        patch.object(bounded_control, "preflight", side_effect=RuntimeError("offline stop before Docker")) as probe:
                    if reaches_preflight:
                        with self.assertRaisesRegex(RuntimeError, "offline stop"):
                            bounded_control.start(self.stage, "default", 223)
                        probe.assert_called_once()
                    else:
                        with self.assertRaisesRegex(ValueError, "requires a verified automatic small"):
                            bounded_control.start(self.stage, "default", 223)
                        probe.assert_not_called()
                self.assertFalse((self.stage / "private/start.lock").exists())

    def test_verifier_rejects_an_old_workflow_before_reading_or_writing(self):
        with patch.object(sys, "argv", ["verify_round.py", "--workflow", "bf-core-20261002-220503"]):
            with self.assertRaisesRegex(ValueError, "Only new automatic-round IDs"):
                verify_round.main()
        self.assertFalse((self.stage / "private").exists())

    def test_success_verifier_cannot_accept_a_partial_nine_stage_snapshot(self):
        folder = self.stage / "partial"
        common.write(folder / "task-snapshot.json", {"tasks": []})
        with self.assertRaisesRegex(ValueError, "Nine exact tasks"):
            verify_round.verify_success(self.stage, folder, {"workflow_id": "fresh-fixture"}, {}, None, None)


if __name__ == "__main__":
    unittest.main(verbosity=2)
