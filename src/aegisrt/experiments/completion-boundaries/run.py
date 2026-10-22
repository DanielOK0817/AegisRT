"""Externally kill isolated production-component probes; never call a model."""
from __future__ import annotations

import argparse
import csv
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
POINTS = ("none", "running", "prepared", "published", "succeeded")


def save(path: Path, value: object) -> None:
    with path.open("x", encoding="utf-8") as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write("\n")


def snapshot(trial: Path, phase: str) -> None:
    for name in ("runtime.state", "context.state"):
        shutil.copyfile(trial / name, trial / (phase + "-" + name))


def execute(binary: Path, trial: Path, point: str, mode: str, kill: bool = False) -> dict:
    command = [str(binary), "-mode", mode, "-dir", str(trial), "-point", point]
    start = time.perf_counter_ns()
    with (trial / (mode + ".stdout.log")).open("wb") as stdout, (trial / (mode + ".stderr.log")).open("wb") as stderr:
        process = subprocess.Popen(command, stdout=stdout, stderr=stderr)
        try:
            if kill:
                deadline = time.monotonic() + 20
                while not (trial / "boundary.json").exists():
                    if process.poll() is not None:
                        raise RuntimeError(f"probe exited before {point} boundary: {process.returncode}")
                    if time.monotonic() >= deadline:
                        raise TimeoutError(f"probe did not reach {point} boundary")
                    time.sleep(0.01)
                # File creation precedes the fsync/close by a few instructions.
                # Wait until its JSON is readable, then snapshot durable state.
                while True:
                    try:
                        marker = json.loads((trial / "boundary.json").read_text(encoding="utf-8"))
                        break
                    except json.JSONDecodeError:
                        time.sleep(0.001)
                if marker["pid"] != process.pid or marker["point"] != point:
                    raise RuntimeError("fault marker identity mismatch")
                snapshot(trial, "before")
                process.kill()
                process.wait(timeout=5)
                if process.returncode == 0:
                    raise RuntimeError("hard-killed probe reported successful exit")
            else:
                process.wait(timeout=25)
                if process.returncode != 0:
                    raise RuntimeError(f"{mode} probe failed: {process.returncode}; inspect {trial / (mode + '.stderr.log')}")
        except BaseException:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=5)
            raise
    return {"mode": mode, "pid": process.pid, "returncode": process.returncode,
            "hard_kill_requested": kill, "elapsed_ns": time.perf_counter_ns() - start}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True, type=Path, help="fresh empty evidence directory")
    parser.add_argument("--repeat", type=int, default=5)
    parser.add_argument("--binary", type=Path, help="prebuilt native binary, otherwise build using local Go")
    parser.add_argument("--source-manifest", type=Path, help="source_sha256 mapping recorded when cross-compiling")
    parser.add_argument("--points", nargs="+", choices=POINTS, default=list(POINTS))
    args = parser.parse_args()
    if args.repeat < 1 or args.repeat > 100:
        parser.error("repeat must be between 1 and 100")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    binary = args.binary.resolve() if args.binary else output / ("boundary-probe.exe" if os.name == "nt" else "boundary-probe")
    if not args.binary:
        subprocess.run(["go", "build", "-o", str(binary), "./experiments/completion-boundaries"], cwd=REPO, check=True)
    if not binary.is_file():
        raise FileNotFoundError(binary)
    sources = [path for path in (REPO / "internal").rglob("*.go") if not path.name.endswith("_test.go")]
    sources += [REPO / "go.mod", REPO / "go.sum"]
    sources += list(HERE.glob("*.go")) + [HERE / "run.py", HERE / "verify.py"]
    manifest = {str(path.relative_to(REPO)).replace("\\", "/"): hashlib.sha256(path.read_bytes()).hexdigest() for path in sources if path.is_file()}
    if args.source_manifest:
        supplied = json.loads(args.source_manifest.read_text(encoding="utf-8"))
        for name, digest in manifest.items():
            if name in supplied and supplied[name] != digest:
                raise RuntimeError(f"deployed source differs from build manifest: {name}")
        manifest = supplied
    save(output / "environment.json", {"platform": platform.platform(), "python": sys.version,
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "source_sha256": manifest,
        "repeat": args.repeat, "points": args.points, "new_model_requests": 0,
        "scope": "native production Coordinator/Worker/statefile/Context components; experiment wrappers; no HTTP daemon or cgroup"})
    rows = []
    for repetition in range(1, args.repeat + 1):
        for point in args.points:
            trial = output / f"{point}-{repetition:03d}"
            (trial / "journal").mkdir(parents=True)
            actions = [execute(binary, trial, point, "initial", point != "none")]
            if point == "none":
                snapshot(trial, "before")
            actions.append(execute(binary, trial, point, "recover"))
            snapshot(trial, "after")
            actions.append(execute(binary, trial, point, "recover-again"))
            snapshot(trial, "reopened")
            save(trial / "controller.json", {"point": point, "repetition": repetition, "actions": actions})
            rows.append({"point": point, "repetition": repetition, "initial_elapsed_ns": actions[0]["elapsed_ns"],
                         "recover_elapsed_ns": actions[1]["elapsed_ns"], "reopen_elapsed_ns": actions[2]["elapsed_ns"]})
            print(f"completed {point} {repetition}/{args.repeat}", flush=True)
    with (output / "controller-timing.csv").open("x", newline="", encoding="utf-8") as stream:
        writer = csv.DictWriter(stream, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)
    subprocess.run([sys.executable, str(HERE / "verify.py"), str(output)], check=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
