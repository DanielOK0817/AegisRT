"""Freeze source variants and adapters without changing the working runtime."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import subprocess
import tarfile
import time

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
BASE = "48a2d846697d03c564fb39555ad0004efbb06597"
PACKAGES = ["contextstate", "contextvm", "contextdispatch", "domain", "statefile"]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def replace_once(files, name, old, new):
    data = files[name].decode("utf-8")
    assert data.count(old) == 1, (name, "patch context drift")
    files[name] = data.replace(old, new).encode("utf-8")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path)
    parser.add_argument("--previous-current", type=Path, help="verified prior C.tar.gz for a before/after comparison")
    args = parser.parse_args()
    out = args.output or HERE / "evidence" / time.strftime("prepared-%Y%m%d-%H%M%S")
    out.mkdir(parents=True, exist_ok=False)
    names = ["go.mod", "go.sum"]
    for package in PACKAGES:
        names += [p.relative_to(ROOT).as_posix() for p in (ROOT / "internal" / package).glob("*.go")]
    current = {name: (ROOT / name).read_bytes() for name in sorted(names)}
    git_names = subprocess.check_output(["git", "ls-tree", "-r", "--name-only", BASE, "go.mod", "go.sum"] +
        ["internal/" + p for p in PACKAGES], cwd=ROOT, text=True).splitlines()
    baseline = {name: subprocess.check_output(["git", "show", BASE + ":" + name], cwd=ROOT)
                for name in git_names if name.endswith(".go") or name in ("go.mod", "go.sum")}
    variants = {"A": baseline, "B": dict(current), "C": dict(current), "N": dict(current)}
    newline = "\r\n" if b"\r\n" in current["internal/contextstate/repository.go"] else "\n"
    replace_once(variants["B"], "internal/contextstate/repository.go",
        "return buildComponentsWithStore(r.config, stored, r.store.Clone())",
        ("value, err := buildComponents(r.config, stored)\n"
         "\tif err == nil { value.store.ExperimentCopyStatsFrom(r.store) }\n"
         "\treturn value, err").replace("\n", newline))
    variants["B"]["internal/contextvm/experiment_stats.go"] = b'''package contextvm
// ExperimentCopyStatsFrom preserves access counters while the ablation rebuilds content.
func (s *Store) ExperimentCopyStatsFrom(source *Store) {
    source.mu.RLock()
    defer source.mu.RUnlock()
    s.mu.Lock()
    defer s.mu.Unlock()
    s.stats = source.stats
}
'''
    replace_once(variants["N"], "internal/contextstate/compression.go",
        "if record, exists := s.records[key]; exists {",
        "if record, exists := s.records[key]; exists && false { // experiment: bypass cache hits only")
    previous = None
    if args.previous_current:
        previous_manifest = json.loads((args.previous_current.parent / "sources.json").read_text(encoding="utf-8"))
        previous = previous_manifest["variants"]["C"]
        assert digest(args.previous_current.read_bytes()) == previous["archive_sha256"]
        with tarfile.open(args.previous_current) as archive:
            variants["B"] = {name: archive.extractfile(name).read() for name in previous["files"]}
        assert all(digest(data) == previous["files"][name] for name,data in variants["B"].items())
    workload = (ROOT / "internal/benchmark/communication_workload.go").read_text(encoding="utf-8")
    workload = workload[:workload.index("func (w *communicationWorkload) verify")]
    workload = workload.replace("package benchmark", "package main").replace('\n\t"os"', '\n\t"reflect"')
    workload = workload.replace("AttemptNumber: 1, ", "")
    workload = workload.replace("\t\tvar targets []string", '\t\tfield := reflect.ValueOf(&command).Elem().FieldByName("AttemptNumber")\n\t\tif field.IsValid() { field.SetInt(1) }\n\t\tvar targets []string')
    types = (ROOT / "internal/benchmark/communication_types.go").read_text(encoding="utf-8")
    types = 'package main\nimport "time"\n' + types[types.index("type CommunicationCase"):types.index("type CommunicationReport")]
    collect = (ROOT / "internal/benchmark/communication_collect.go").read_text(encoding="utf-8")
    collect = collect[collect.index("func runCommunicationTrial"):]
    collect = collect.replace("if err := w.verify(ctx); err != nil {", "if err := w.verify(ctx, &trial); err != nil {")
    collect = collect.replace("trial.ReplayStable, trial.ReloadVerified, trial.IsolationVerified = true, true, true", "// Correctness fields are measured independently by verify.")
    collect = 'package main\nimport ("context";"encoding/json";"fmt";"os";"path/filepath";"runtime";"sort";"sync";"time")\n' + collect
    collect = collect.replace(';"path/filepath"', '').replace(';"fmt"', '')
    adapters = {"cmd/evaluation/workload.go": workload.encode(), "cmd/evaluation/metrics.go": types.encode(),
                "cmd/evaluation/measure.go": collect.encode()}
    adapters.update({"cmd/evaluation/" + p.name.removesuffix(".tmpl"): p.read_bytes() for p in HERE.glob("*.go.tmpl")})
    adapters = {k: subprocess.check_output(["gofmt"], input=v) for k, v in adapters.items()}
    manifest = {"schema": 1, "baseline_commit": BASE, "created_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "variants": {}, "adapter_files": {k: digest(v) for k, v in adapters.items()},
        "derivation_inputs": {"internal/benchmark/" + n: digest((ROOT / "internal/benchmark" / n).read_bytes())
            for n in ("communication_workload.go", "communication_types.go", "communication_collect.go")},
        "definitions": {"A": "actual Git baseline, historical correctness limitations retained",
            "B": "current correctness-preserving reconstruction; snapshot revalidation replaces Store.Clone; statistics preserved; not a historical release",
            "C": "current unchanged source, optimized Store.Clone; compression disabled in communication",
            "N": "current source with summary cache lookup bypassed; provenance and durable records still retained"}}
    manifest["dependency_reads_without_write"] = ["C", "N"] + ([] if previous else ["B"])
    if previous:
        manifest["previous_current"] = previous
        manifest["definitions"]["B"] = "verified archived pre-optimization working source; durable recipient hydration; not a historical Git release"
        manifest["definitions"]["C"] = "current source; authorized dependency read views avoid recipient checkpoint writes"
    for variant, files in variants.items():
        entries = {k: digest(v) for k, v in sorted(files.items())}
        identity = digest("\n".join(k + " " + v for k, v in entries.items()).encode())
        archive = out / (variant + ".tar.gz")
        with tarfile.open(archive, "w:gz") as tar:
            for name, data in sorted({**files, **adapters}.items()):
                info = tarfile.TarInfo(name)
                info.size, info.mode, info.mtime = len(data), 0o600, 0
                tar.addfile(info, io.BytesIO(data))
        manifest["variants"][variant] = {"source_sha256": identity, "files": entries, "archive_sha256": digest(archive.read_bytes()),
            "diff_from_current": [k for k in sorted(set(current) | set(files)) if current.get(k) != files.get(k)]}
    assert all((ROOT / k).read_bytes() == v for k, v in current.items()), "working source changed during freeze"
    for name in ("run.py",):
        (out / name).write_bytes((HERE / name).read_bytes())
    manifest["runner_sha256"] = digest((out / "run.py").read_bytes())
    for name in ("prepare.py", "README.md"):
        (out / name).write_bytes((HERE / name).read_bytes())
    manifest["protocol_sha256"] = digest((out / "README.md").read_bytes())
    manifest["prepare_sha256"] = digest((out / "prepare.py").read_bytes())
    write_json(out / "sources.json", manifest)
    print(out)


if __name__ == "__main__":
    main()
