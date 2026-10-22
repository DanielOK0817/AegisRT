"""Shared zero-model helpers for independent fresh-workflow acceptance rounds."""
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
from types import SimpleNamespace

STAGE_NAME = "automatic-validation-20261003"
REMOTE_ROOT = Path("/opt/aegisrt/acceptance")
CONTAINER_STAGE = Path("/work") / STAGE_NAME
STAGES = ("query", "media", "insight", "forum", "query-feedback", "media-feedback",
          "insight-feedback", "forum-final", "report")
PROFILE_KEYS = ("profile", "max_paragraphs", "max_reflections", "insight_max_reflections",
    "model_request_limits", "refinement_request_limits", "max_model_requests", "max_output_tokens",
    "max_content_length", "task_timeout", "wall_timeout", "task_count", "resume_used",
    "editorial_review_applied", "saved_model_response_reused", "agent_entry")


def read(path):
    return json.loads(Path(path).read_text(encoding="utf-8-sig"))


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def write(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def utc_now():
    return datetime.now(timezone.utc).isoformat()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    sys.modules[name] = value
    spec.loader.exec_module(value)
    return value


def inside(root, path):
    root, path = Path(root).resolve(), Path(path).resolve()
    require(path.is_relative_to(root) and path != root, "Path is outside the new acceptance workspace")
    return path


def revision(stage, revision_id=None):
    stage = Path(stage).resolve()
    manifest = read(stage / "stage-manifest.json")
    selected = revision_id or manifest["current_revision"]
    require(selected in manifest["revisions"], "Unknown frozen code revision")
    folder = inside(stage, stage / "revisions" / selected)
    records = manifest["revisions"][selected]
    for record in records:
        require(sha(inside(folder, folder / record["path"])) == record["sha256"], "Frozen code hash changed")
    runner = module("automatic_runner_" + selected.replace("-", "_"), folder / "integration/runner.py")
    return selected, folder, records, runner


def profile_hash(parameters):
    return hashlib.sha256(json.dumps(parameters, ensure_ascii=False, sort_keys=True,
                                    separators=(",", ":")).encode("utf-8")).hexdigest()


def new_profile_parameters(profile, runner):
    """Explicit parameters for the next frozen revision, never for legacy launches."""
    require(profile in ("small", "default"), "Unknown acceptance profile")
    defaults = list(runner.LIVE_REQUEST_LIMITS)
    require(defaults[-1] == 5, "Acceptance requires the final five-request Report generation and review budget")
    # Keep the same 58-call small cap, with two recovery calls after each
    # research task's mandatory steps. Frozen profiles are read separately and
    # must never be reconstructed from these next-revision defaults.
    initial = [11, 11, 11, defaults[3], defaults[4]] if profile == "small" else defaults
    refinement = [6, 6, 6] if profile == "small" else list(runner.REFINEMENT_REQUEST_LIMITS)
    return {"profile": profile, "max_paragraphs": 2 if profile == "small" else 5,
        "max_reflections": 1 if profile == "small" else 2, "insight_max_reflections": 1 if profile == "small" else 3,
        "model_request_limits": initial, "refinement_request_limits": refinement,
        "max_model_requests": sum(initial) + sum(refinement) + initial[3], "max_output_tokens": 16000,
        "report_max_output_tokens": 32000,
        "max_content_length": 60000, "task_timeout": 1800 if profile == "small" else 3600,
        "wall_timeout": 3600 if profile == "small" else 7200, "task_count": 9, "resume_used": False,
        "editorial_review_applied": False, "saved_model_response_reused": False,
        "agent_entry": "frozen production adapter.py"}


def profile_record(stage, revision_id, profile):
    manifest = read(Path(stage) / "stage-manifest.json")
    require(profile in ("small", "default"), "Unknown acceptance profile")
    record = manifest.get("profiles", {}).get(revision_id, {}).get(profile)
    require(isinstance(record, dict), "Frozen revision has no immutable profile configuration")
    parameters = record.get("parameters")
    require(isinstance(parameters, dict) and set(parameters) in (set(PROFILE_KEYS), set(PROFILE_KEYS) | {"report_max_output_tokens"}) and
            parameters["profile"] == profile and record.get("sha256") == profile_hash(parameters),
            "Immutable profile configuration hash or fields changed")
    if "report_max_output_tokens" in parameters:
        require(type(parameters["report_max_output_tokens"]) is int and 128 <= parameters["report_max_output_tokens"] <= 32000,
                "Report output capacity is outside the declared bounds")
    return record


def plan(stage, profile, workflow="fresh-plan", revision_id=None):
    selected, folder, records, runner = revision(stage, revision_id)
    record = profile_record(stage, selected, profile)
    parameters = record["parameters"]
    require(parameters["resume_used"] is False and parameters["editorial_review_applied"] is False and
            parameters["saved_model_response_reused"] is False, "Profile is not a fresh automatic round")
    require((parameters["max_paragraphs"], parameters["max_reflections"], parameters["insight_max_reflections"]) ==
            ((2, 1, 1) if profile == "small" else (5, 2, 3)), "Frozen profile reduces declared research depth")
    require(record.get("legacy_launch") is not None or
            (parameters["model_request_limits"][3:] == [1, 5] and
             (profile != "default" or parameters["max_model_requests"] == 223)),
            "Strict acceptance requires Forum1 and Report5 with the unchanged default223 budget")
    args = SimpleNamespace(workflow_id=workflow, mode="live", fixed_topic_search=False,
        **{name: parameters[name] for name in ("model_request_limits", "refinement_request_limits",
            "max_paragraphs", "max_reflections", "insight_max_reflections", "max_output_tokens",
            "max_content_length", "task_timeout", "wall_timeout")})
    if "report_max_output_tokens" in parameters:
        args.report_max_output_tokens = parameters["report_max_output_tokens"]
    tasks = runner.make_tasks(args, "Frozen sample topic")["tasks"]
    require({row["id"] for row in tasks} == {workflow + "-" + suffix for suffix in STAGES}, "Live graph is not the nine-stage workflow")
    require(all(row["max_attempts"] == 1 for row in tasks), "Runtime retries must remain one per task")
    budget = sum(json.loads(row["goal"])["max_model_requests"] for row in tasks)
    require(budget == parameters["max_model_requests"] and len(tasks) == parameters["task_count"],
            "Frozen profile budget or task count differs from the exact declared graph")
    return {**parameters, "code_revision": selected, "source_records": records,
            "profile_configuration_sha256": record["sha256"]}


def validate_launch_configuration(stage, launch_path):
    """Validate existing launch bytes; legacy records are never modified or passed."""
    launch = read(launch_path)
    configuration = plan(stage, launch["profile"], revision_id=launch["code_revision"])
    record = profile_record(stage, launch["code_revision"], launch["profile"])
    legacy = record.get("legacy_launch")
    if legacy is not None:
        require(legacy.get("workflow_id") == launch["workflow_id"] and sha(launch_path) == legacy.get("sha256"),
                "Original legacy launch bytes or workflow identity changed")
        require("profile_configuration_sha256" not in launch, "Legacy launch was retroactively rewritten")
    else:
        require(launch.get("profile_configuration_sha256") == configuration["profile_configuration_sha256"],
                "New launch lacks its immutable profile configuration hash or references another profile")
    require(all(launch.get(key) == value for key, value in configuration.items()
                if key != "profile_configuration_sha256" or legacy is None),
            "Frozen launch parameters changed")
    return configuration, legacy is not None


def runner_arguments(configuration, workflow):
    base = CONTAINER_STAGE
    command = ["/usr/local/bin/python", str(base / "revisions" / configuration["code_revision"] / "integration/runner.py"),
        "--aegisrt", "/work/bin/aegisrt", "--source-dir", "/app", "--env-file", "/app/.env",
        "--input-dir", str(base / "private/runs" / workflow / "inputs"),
        "--run-dir", str(base / "private/runs"), "--workflow-id", workflow, "--mode", "live",
        "--cancel-file", str(base / "private/runs" / workflow / "cancel.requested")]
    for name in ("max_paragraphs", "max_reflections", "insight_max_reflections", "max_output_tokens",
                 "max_content_length", "task_timeout", "wall_timeout"):
        command += ["--" + name.replace("_", "-"), str(configuration[name])]
    for name in ("model_request_limits", "refinement_request_limits"):
        command += ["--" + name.replace("_", "-"), ",".join(map(str, configuration[name]))]
    if "report_max_output_tokens" in configuration:
        command += ["--report-max-output-tokens", str(configuration["report_max_output_tokens"])]
    require("--resume-dir" not in command and "--fixed-topic-search" not in command, "Fresh acceptance cannot resume or force fixed search")
    return command


def usage(folder):
    folder = Path(folder)
    budgets = [read(path) for path in folder.glob("*/model-budget.json")]
    rows = [read(path) for path in folder.glob("*/model-usage/*.json")]
    provided = [row for row in rows if row.get("usage_present")]
    return {"provider_attempts": sum(value.get("attempts", 0) for value in budgets),
        "failed_attempts": sum(value.get("failed_attempts", 0) for value in budgets),
        "logged_calls": len(rows), "calls_missing_usage": len(rows) - len(provided),
        "reported_total_tokens": sum(row.get("total_tokens") or 0 for row in provided) if provided else None}


def profile_spent(stage, profile):
    launches = list((Path(stage) / "private/rounds").glob("*.json"))
    return sum(usage(Path(stage) / "private/runs" / row["workflow_id"])["provider_attempts"]
               for row in map(read, launches) if row["profile"] == profile)


def require_capacity(stage, configuration, aggregate_limit):
    require(type(aggregate_limit) is int and aggregate_limit > 0, "An explicit positive aggregate bound is required")
    if configuration["profile"] == "small":
        require(aggregate_limit <= 552, "Small-stage aggregate bound cannot exceed 552")
    else:
        require(aggregate_limit == 223, "The default round has its own 223-request bound")
    spent = profile_spent(stage, configuration["profile"])
    require(spent + configuration["max_model_requests"] <= aggregate_limit, "Insufficient aggregate budget for another fresh round")
    return spent
