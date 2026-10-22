#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 || ( $# -eq 3 && "$3" != "--verify-only" ) ]]; then
    echo "usage: run-linux.sh <output-parent> <expected-source-manifest.json> [--verify-only]" >&2
    exit 2
fi
if [[ "$(uname -s)" != "Linux" ]]; then
    echo "this entrypoint requires a Linux host" >&2
    exit 2
fi
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
runner_path="${repo_root}/experiments/communication/run-linux.sh"

python3 - "$repo_root" "$1" "$2" "${3:-}" "$runner_path" <<'PY'
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from datetime import datetime, timezone

root = Path(sys.argv[1]).resolve()
output = Path(sys.argv[2]).resolve()
expected_path = Path(sys.argv[3]).resolve()
verify_only = sys.argv[4] == "--verify-only"
runner = Path(sys.argv[5]).resolve()
output.mkdir(parents=True, exist_ok=True)
build_dir = Path(tempfile.mkdtemp(prefix="communication-linux-build-", dir=str(output)))

def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def canonical_hash(entries):
    content = "\n".join(item["path"] + " " + item["sha256"] for item in entries)
    return hashlib.sha256(content.encode("utf-8")).hexdigest()

def snapshot():
    files = list((root / "cmd").rglob("*.go")) + list((root / "internal").rglob("*.go"))
    files.extend(root / name for name in ("go.mod", "go.sum", "experiments/communication/run.ps1"))
    records = {}
    for path in files:
        if root not in path.resolve().parents:
            raise RuntimeError("source path escapes repository: " + str(path))
        relative = path.relative_to(root).as_posix()
        records[relative] = sha256(path)
    return records

def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

expected = json.loads(expected_path.read_text(encoding="utf-8-sig"))
expected_entries = expected["files"]
if canonical_hash(expected_entries) != expected["source_sha256"]:
    raise RuntimeError("expected source manifest digest is invalid")
expected_map = {item["path"]: item["sha256"] for item in expected_entries}
if len(expected_map) != len(expected_entries):
    raise RuntimeError("expected source manifest contains duplicate paths")
before = snapshot()
differences = [
    {"path": name, "expected_sha256": expected_map.get(name), "actual_sha256": before.get(name)}
    for name in sorted(set(expected_map) | set(before))
    if expected_map.get(name) != before.get(name)
]
if differences:
    entries = [{"path": name, "sha256": before[name]} for name in sorted(before)]
    identity = {"schema_version": 1, "source_sha256": canonical_hash(entries), "files": entries,
                "canonical_format": "Unicode-sorted relative path, ASCII space, lowercase SHA256; LF separators; no trailing LF"}
else:
    # Preserve the original manifest order; PowerShell and Python need not have
    # the same collation rules even when every source byte matches.
    identity = expected
manifest_path = output / ("source-" + identity["source_sha256"] + ".json")
write_json(manifest_path, identity)
metadata = {
    "schema_version": 1,
    "status": "source-verified",
    "started_at": datetime.now(timezone.utc).isoformat(),
    "expected_source_sha256": expected["source_sha256"],
    "actual_source_sha256": identity["source_sha256"],
    "expected_file_count": len(expected_map),
    "actual_file_count": len(before),
    "matched_expected_manifest": not differences,
    "differences": differences,
    "runner_sha256": sha256(runner),
    "build_source_inventory": "cmd/**/*.go, internal/**/*.go, go.mod, go.sum, experiments/communication/run.ps1",
    "runner_binding": "Linux runner is bound separately; no Go source is changed by this script",
    "uid": os.getuid(),
    "process_environment": {key: os.environ.get(key, "default") for key in ("GOMAXPROCS", "GOMEMLIMIT", "CGO_ENABLED")},
}
metadata_path = build_dir / "source-comparison.json"
write_json(metadata_path, metadata)
print("Source comparison: " + str(metadata_path), flush=True)
print("Matched expected manifest: " + str(not differences), flush=True)
print("Source manifest: " + str(manifest_path), flush=True)
if verify_only:
    sys.exit(0)
if os.getuid() == 0:
    raise RuntimeError("communication measurement must run as an ordinary user")
binary = build_dir / "aegisbench"
previous_reports = set(output.glob("communication-*/report.json"))
try:
    metadata["go_version"] = subprocess.check_output(["go", "version"], text=True).strip()
    subprocess.run(["go", "build", "-ldflags", "-X main.communicationSourceSHA256=" + identity["source_sha256"],
                    "-o", str(binary), "./cmd/aegisbench"], cwd=str(root), check=True)
    if snapshot() != before:
        raise RuntimeError("source changed during build; no measurement was started")
    metadata["binary_sha256"] = sha256(binary)
    metadata["status"] = "measuring"
    write_json(metadata_path, metadata)
    subprocess.run([str(binary), "communication", "--profile", "smoke", "--output", str(output)], cwd=str(root), check=True)
    metadata["workspace_unchanged_after_measurement"] = snapshot() == before
    metadata["status"] = "complete"
except BaseException as error:
    metadata["status"] = "failed"
    metadata["failure"] = str(error)
    raise
finally:
    metadata["finished_at"] = datetime.now(timezone.utc).isoformat()
    metadata["reports"] = [str(path) for path in sorted(set(output.glob("communication-*/report.json")) - previous_reports)]
    write_json(metadata_path, metadata)
PY
