"""Build once, prewarm uniformly, and execute a sequential matched matrix."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import shutil
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

import verify


HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
POLICIES = ["fifo", "dag-drcfs", "dag-drcfs-time"]
SCENARIOS = {"simultaneous": "0ms", "late-short": "100ms"}


def write(path, data):
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def source_files():
    files = [REPO / "go.mod", REPO / "go.sum", REPO / "cmd/aegisrt/scheduler_policy.go"]
    files.extend(path for path in (REPO / "internal").rglob("*.go") if not path.name.endswith("_test.go"))
    files.extend(path for path in HERE.iterdir() if path.suffix in (".py", ".go", ".md"))
    return sorted(set(files))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True, type=Path, help="must be a new directory")
    parser.add_argument("--repetitions", type=int, default=3)
    parser.add_argument("--tasks-per-workflow", type=int, default=24)
    parser.add_argument("--long-duration", default="1200ms")
    parser.add_argument("--short-duration", default="250ms")
    parser.add_argument("--go", default="go")
    parser.add_argument("--binary", type=Path, help="reuse an externally built executable, recording its SHA256")
    args = parser.parse_args()
    if args.repetitions < 3:
        parser.error("formal matrix requires at least three repetitions")
    output = args.output.resolve()
    output.mkdir(parents=False, exist_ok=False)
    (output / "bin").mkdir()
    sources = {}
    for path in source_files():
        relative = path.relative_to(REPO).as_posix()
        destination = output / "source" / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, destination)
        sources[relative] = verify.digest(destination)
    binary = output / "bin" / ("contention.exe" if os.name == "nt" else "contention")
    if args.binary:
        shutil.copyfile(args.binary.resolve(), binary)
        binary.chmod(0o700)
        (output / "build.stdout.txt").write_text("Externally built binary copied; exact SHA256 recorded.\n", encoding="utf-8")
        (output / "build.stderr.txt").write_text("", encoding="utf-8")
    else:
        build = subprocess.run([args.go, "build", "-trimpath", "-o", str(binary), "./experiments/workflow-contention"], cwd=REPO, capture_output=True, text=True)
        (output / "build.stdout.txt").write_text(build.stdout, encoding="utf-8")
        (output / "build.stderr.txt").write_text(build.stderr, encoding="utf-8")
        if build.returncode:
            raise RuntimeError("Go build failed; preserved build logs")
    for relative, sha in sources.items():
        if verify.digest(REPO / relative) != sha:
            raise RuntimeError("source changed during build; output preserved, run rejected")
    manifest = {"schema": "aegisrt.workflow-contention-matrix/v1", "created_at": datetime.now(timezone.utc).isoformat(),
                "host": {"platform": platform.platform(), "python": sys.version, "logical_cpus": os.cpu_count()},
                "go_version": subprocess.check_output([args.go, "version"], text=True).strip(),
                "runtime_controls": {key: os.environ.get(key) for key in ("GOMAXPROCS", "GOMEMLIMIT")},
                "binary_path": binary.relative_to(output).as_posix(), "binary_sha256": verify.digest(binary),
                "build_mode": "external-binary" if args.binary else "go-build-on-host",
                "source_tree_sha256": hashlib.sha256(json.dumps(sources, sort_keys=True).encode()).hexdigest(), "sources": sources,
                "protocol": {"repetitions": args.repetitions, "policies": POLICIES, "scenarios": list(SCENARIOS),
                             "workers": 4, "tasks_per_workflow": args.tasks_per_workflow,
                             "long_duration": args.long_duration, "short_duration": args.short_duration,
                             "warmup": "one retained independent short run per policy before measurements",
                             "cache_control": "uniform prewarm; fresh executable process and empty persistent state per run; no cache flush",
                             "order_control": "cyclic Latin-square policy orders, opposite rotation in second scenario",
                             "model_requests": 0}, "runs": []}
    write(output / "manifest.json", manifest)

    def execute(policy, scenario, repetition, order, phase):
        label = f"{phase}-{scenario}-r{repetition}-{order}-{policy}"
        destination = output / label
        warm = phase == "warmup"
        command = [str(binary), "--policy", policy, "--output", str(destination), "--workers", "4",
                   "--tasks-per-workflow", "4" if warm else str(args.tasks_per_workflow),
                   "--long-duration", "100ms" if warm else args.long_duration,
                   "--short-duration", "40ms" if warm else args.short_duration,
                   "--short-arrival", "0ms" if warm else SCENARIOS[scenario]]
        print(f"{label}: starting", flush=True)
        result = subprocess.run(command, capture_output=True, text=True, timeout=240)
        destination.mkdir(exist_ok=True)
        (destination / "stdout.txt").write_text(result.stdout, encoding="utf-8")
        (destination / "stderr.txt").write_text(result.stderr, encoding="utf-8")
        manifest["runs"].append({"directory": label, "policy": policy, "scenario": scenario, "repetition": repetition,
                                 "order": order, "phase": phase, "command": command, "exit_code": result.returncode})
        write(output / "manifest.json", manifest)
        if result.returncode:
            raise RuntimeError(f"{label} failed; raw evidence retained")
        metrics = verify.verify_run(destination)
        print(f"{label}: passed; makespan={metrics['makespan_seconds']:.3f}s short-p95={metrics['short_ready_to_start_p95_seconds']:.3f}s", flush=True)

    for order, policy in enumerate(POLICIES, 1):
        execute(policy, "prewarm", 0, order, "warmup")
    for scenario_index, scenario in enumerate(SCENARIOS):
        for repetition in range(1, args.repetitions + 1):
            rotation = (repetition - 1) * (1 if scenario_index == 0 else -1) % len(POLICIES)
            order = POLICIES[rotation:] + POLICIES[:rotation]
            for index, policy in enumerate(order, 1):
                execute(policy, scenario, repetition, index, "measured")
    report = verify.verify_matrix(output)
    print(json.dumps({"passed": report["passed"], "measured_runs": report["measured_runs"], "output": str(output), "model_requests": 0}), flush=True)


if __name__ == "__main__":
    main()
