"""Plan or explicitly start an independent fresh round; no automatic chaining."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import uuid
from workflow_common import (CONTAINER_STAGE, REMOTE_ROOT, STAGE_NAME, inside, module, plan, read,
    profile_record, require, require_capacity, revision, runner_arguments, sha, usage, utc_now, write)

DOCKER = [str(REMOTE_ROOT / "docker-bin/docker"), "-H", "unix://" + str(REMOTE_ROOT / "docker-runtime/docker.sock")]
APP = "bettafish-integration-20261002-app"


def status(stage):
    rounds = []
    for path in sorted((stage / "private/rounds").glob("*.json")):
        launch = read(path)
        folder = stage / "private/runs" / launch["workflow_id"]
        summary = read(folder / "summary.json") if (folder / "summary.json").is_file() else {}
        verification = read(folder / "verification.json") if (folder / "verification.json").is_file() else {}
        rounds.append({"workflow_id": launch["workflow_id"], "profile": launch["profile"],
            "code_revision": launch["code_revision"], "max_model_requests": launch["max_model_requests"],
            "aggregate_limit": launch["aggregate_limit"], "status": summary.get("status", "pending_summary"),
            "elapsed_seconds": summary.get("elapsed_seconds"), "usage": usage(folder),
            "verification": verification.get("status", "not_verified"), "cleanup": summary.get("cleanup")})
    return {"rounds": rounds, "prior_65_call_ledger_touched": False, "public_entry_touched": False}


def ensure_no_active_round(stage):
    for row in status(stage)["rounds"]:
        cleanup = row.get("cleanup") or {}
        require(row["status"] in ("error", "completed") and cleanup.get("tasks_terminal") and cleanup.get("daemon_stopped"),
                "A previous round is active or has uncertain cleanup; no new calls started")


def preflight(stage):
    selected, _, records, _ = revision(stage)
    manifest_hash = sha(stage / "stage-manifest.json")
    code = ("import hashlib,json,pathlib,sys; p=pathlib.Path('" + str(CONTAINER_STAGE) + "'); sys.path.insert(0,str(p)); "
            "from workflow_common import revision; selected,folder,records,runner=revision(p); "
            "assert hashlib.sha256((p/'stage-manifest.json').read_bytes()).hexdigest()=='" + manifest_hash + "'; "
            "checks={name:pathlib.Path(name).is_file() for name in ['/app/app.py','/app/.env','/work/bin/aegisrt']}; "
            "print(json.dumps({'required_files':checks,'code_revision':selected,'code_files_verified':len(records),'frozen_code_hashes_verified':True,'stage_mounted':(p/'stage-manifest.json').is_file(),"
            "'runtime_sha256':hashlib.sha256(pathlib.Path('/work/bin/aegisrt').read_bytes()).hexdigest()})); "
            "sys.exit(0 if all(checks.values()) and (p/'stage-manifest.json').is_file() else 1)")
    result = subprocess.run(DOCKER + ["exec", APP, "/usr/local/bin/python", "-c", code], capture_output=True, text=True, timeout=20)
    require(result.returncode == 0, "Read-only environment preflight failed")
    return json.loads(result.stdout)


def start(stage, profile, aggregate_limit):
    require(os.name == "posix" and stage == (REMOTE_ROOT / STAGE_NAME).resolve(), "Start is only allowed in the new remote acceptance stage")
    configuration = plan(stage, profile)
    require(profile_record(stage, configuration["code_revision"], profile).get("legacy_launch") is None,
            "Historical revisions only support read-only launch verification; never start them again")
    private = stage / "private"
    private.mkdir(mode=0o700, exist_ok=True)
    lock = private / "start.lock"
    with lock.open("x"):
        try:
            ensure_no_active_round(stage)
            spent = require_capacity(stage, configuration, aggregate_limit)
            if profile == "default":
                successful = [read(path) for path in (private / "runs").glob("*/verification.json")]
                require(any(row.get("status") == "passed" and row.get("profile") == "small" and
                            row.get("code_revision") == configuration["code_revision"] and row.get("fresh_from_zero") is True
                            for row in successful), "Default scale requires a verified automatic small round on this frozen code")
            environment = preflight(stage)
            selected, code_folder, _, _ = revision(stage, configuration["code_revision"])
            sample_module = module("automatic_samples", code_folder / "integration/sample_inputs.py")
            original = REMOTE_ROOT / "entry/samples/wukong-experience"
            sample_manifest = read(original / "manifest.json")
            sample = sample_module.load_sample_inputs(original, sample_manifest["topic"])
            workflow = "bf-auto-" + profile + "-" + uuid.uuid4().hex[:18]
            folder = inside(private / "runs", private / "runs" / workflow)
            require(not folder.exists(), "Fresh workflow folder already exists")
            folder.mkdir(parents=True, mode=0o700)
            frozen = sample_module.freeze_sample_values(sample["topic"], sample["comments"], sample["provenance"], folder / "inputs")
            require(not list(folder.glob("*/state.json")), "Fresh round contains a cached task")
            command = DOCKER + ["exec", APP] + runner_arguments(configuration, workflow)
            launch = {**configuration, "workflow_id": workflow, "created_at": utc_now(), "aggregate_limit": aggregate_limit,
                "prior_profile_requests": spent, "topic": sample["topic"], "sample_count": sample["sample_count"],
                "input_manifest_sha256": frozen["manifest_sha256"], "source_sample_manifest_sha256": sha(original / "manifest.json"),
                "runtime_sha256": environment["runtime_sha256"], "started_with_no_task_cache": True,
                "configuration_sha256": sha(REMOTE_ROOT / "private/application.env"), "no_automatic_next_round": True}
            launch_path = private / "rounds" / (workflow + ".json")
            write(launch_path, launch)
            with (folder / "coordinator.log").open("xb") as log:
                process = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            launch["coordinator_pid"] = process.pid
            write(launch_path, launch)
            write(private / "ledger.json", status(stage))
            return launch
        finally:
            lock.unlink()


def main():
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("action", choices=("plan", "preflight", "start", "status"))
    cli.add_argument("--profile", choices=("small", "default"), default="small")
    cli.add_argument("--aggregate-limit", type=int)
    cli.add_argument("--stage-dir", type=Path, default=Path(__file__).resolve().parent)
    options = cli.parse_args()
    stage = options.stage_dir.resolve()
    if options.action == "plan":
        result = {**plan(stage, options.profile), "dry_run": True, "model_calls_started": 0}
    elif options.action == "preflight":
        result = {**preflight(stage), "read_only": True, "model_calls_started": 0}
    elif options.action == "start":
        result = start(stage, options.profile, options.aggregate_limit)
    else:
        result = status(stage)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
