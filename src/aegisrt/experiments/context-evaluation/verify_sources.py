"""Check frozen source against actual Git bytes and the current workspace."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tarfile


def main():
    parser=argparse.ArgumentParser();parser.add_argument("prepared",type=Path)
    parser.add_argument("--archive-only",action="store_true",help="verify frozen C after later workspace changes; preserve original verification")
    args=parser.parse_args()
    root=Path(__file__).resolve().parents[2]
    frozen=json.loads((args.prepared/"sources.json").read_text())
    sha=lambda b:hashlib.sha256(b).hexdigest()
    for name,expected in frozen["variants"]["A"]["files"].items():
        data=subprocess.check_output(["git","show",frozen["baseline_commit"]+":"+name],cwd=root)
        assert sha(data)==expected,("Git source",name)
    drift=[]
    for name,expected in frozen["variants"]["C"]["files"].items():
        if not (root/name).exists() or sha((root/name).read_bytes())!=expected:
            drift.append(name)
    if not args.archive_only:
        assert not drift,("current source differs; use --archive-only for historical evidence",drift)
    with tarfile.open(args.prepared/"B.tar.gz") as b,tarfile.open(args.prepared/"C.tar.gz") as c,tarfile.open(args.prepared/"N.tar.gz") as n:
        for name,expected in frozen["variants"]["C"]["files"].items():
            assert sha(c.extractfile(name).read())==expected,("frozen current source",name)
        if "previous_current" in frozen:
            previous=frozen["previous_current"]
            assert frozen["variants"]["B"]["files"]==previous["files"]
            assert frozen["variants"]["B"]["source_sha256"]==previous["source_sha256"]
            for name,expected in previous["files"].items():
                assert sha(b.extractfile(name).read())==expected,("prior source",name)
        else:
            original=c.extractfile("internal/contextstate/repository.go").read().decode()
            changed=b.extractfile("internal/contextstate/repository.go").read().decode()
            nl="\r\n" if "\r\n" in original else "\n"
            replacement=("value, err := buildComponents(r.config, stored)\n\tif err == nil { value.store.ExperimentCopyStatsFrom(r.store) }\n\treturn value, err").replace("\n",nl)
            assert original.replace("return buildComponentsWithStore(r.config, stored, r.store.Clone())",replacement)==changed
            extra=b.extractfile("internal/contextvm/experiment_stats.go").read().decode()
            assert "s.stats = source.stats" in extra and "source.mu.RLock()" in extra and "s.mu.Lock()" in extra
            assert frozen["variants"]["B"]["diff_from_current"]==["internal/contextstate/repository.go","internal/contextvm/experiment_stats.go"]
        original=c.extractfile("internal/contextstate/compression.go").read().decode()
        changed=n.extractfile("internal/contextstate/compression.go").read().decode()
        assert original.replace("if record, exists := s.records[key]; exists {","if record, exists := s.records[key]; exists && false { // experiment: bypass cache hits only")==changed
    assert frozen["variants"]["N"]["diff_from_current"]==["internal/contextstate/compression.go"]
    result={"passed":True,"baseline_files":len(frozen["variants"]["A"]["files"]),"current_files":len(frozen["variants"]["C"]["files"]),
        "baseline_git_bytes_match":True,"current_workspace_bytes_match":not drift,
        "workspace_drift":drift,"verification_mode":"archive_only" if args.archive_only else "workspace",
        "B_verified_as":"prior_current_archive" if "previous_current" in frozen else "clone_ablation",
        "N_exact_cache_ablation_patch_matches":True}
    filename="source-verification-archived.json" if args.archive_only else "source-verification.json"
    (args.prepared/filename).write_text(json.dumps(result,indent=2)+"\n")
    print(json.dumps(result))


if __name__=="__main__":main()
