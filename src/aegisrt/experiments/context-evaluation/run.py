"""Serial, counterbalanced measurement runner; platform-specific reports stay separate."""
import argparse
from datetime import datetime, timezone
import hashlib
import itertools
import json
import os
from pathlib import Path
import platform
import random
import shutil
import subprocess
import tarfile
import time


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def save(path, data):
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--pilot", action="store_true")
    parser.add_argument("--build-only", action="store_true")
    parser.add_argument("--reuse-build", type=Path)
    parser.add_argument("--compression-only", action="store_true")
    parser.add_argument("--compression-read-passes",type=int,default=1,choices=[1,32])
    args = parser.parse_args()
    if platform.system() not in ("Linux","Windows"):
        raise RuntimeError("supported platforms: Linux and Windows")
    if platform.system()=="Linux" and os.getuid()==0:
        raise RuntimeError("use an ordinary Linux user")
    root = Path(__file__).resolve().parent
    frozen = json.loads((root / "sources.json").read_text(encoding="utf-8"))
    assert sha(Path(__file__)) == frozen["runner_sha256"]
    out = root / (time.strftime("pilot-%Y%m%d-%H%M%S") if args.pilot else time.strftime("run-%Y%m%d-%H%M%S"))
    out.mkdir()
    env = dict(os.environ, GOMAXPROCS="2", GOMEMLIMIT="768MiB", GOFLAGS="-p=1")
    go = "/usr/local/bin/go" if platform.system()=="Linux" else shutil.which("go")
    if not go:raise RuntimeError("Go is required")
    def command(args, cwd, target, timeout=240):
        with target.open("wb") as output:
            subprocess.run(args, cwd=cwd, env=env, stdout=output, stderr=subprocess.STDOUT, check=True, timeout=timeout)
    metadata = {"status":"building", "started_at":datetime.now(timezone.utc).isoformat(), "uid":os.getuid() if hasattr(os,"getuid") else None,
        "environment":{k:env[k] for k in ("GOMAXPROCS","GOMEMLIMIT","GOFLAGS")},
        "uname":list(platform.uname()), "os_release":Path("/etc/os-release").read_text() if platform.system()=="Linux" else platform.platform(),
        "meminfo":Path("/proc/meminfo").read_text() if platform.system()=="Linux" else "reported in host-environment.json",
        "platform":platform.system(),"cpu_count":os.cpu_count(),
        "go_version":subprocess.check_output([go,"version"],text=True).strip(),
        "sources_manifest_sha256":sha(root/"sources.json"), "runner_sha256":sha(Path(__file__)),
        "seed":20260922,"pilot":args.pilot,"compression_only":args.compression_only,
        "compression_read_passes":args.compression_read_passes,"binaries":{},"jobs":[]}
    save(out/"run.json",metadata)
    try:
        reused=json.loads((args.reuse_build/"run.json").read_text(encoding="utf-8")) if args.reuse_build else None
        if reused:
            assert reused["status"]=="build-only"
            previous_manifest=args.reuse_build.parent/"sources.json"
            assert sha(previous_manifest)==reused["sources_manifest_sha256"]
            previous_frozen=json.loads(previous_manifest.read_text(encoding="utf-8"))
            assert previous_frozen["adapter_files"]==frozen["adapter_files"]
            for variant,spec in frozen["variants"].items():
                assert previous_frozen["variants"][variant]["files"]==spec["files"]
            metadata["reused_build_validation"]={"run_json_sha256":sha(args.reuse_build/"run.json"),"path":str(args.reuse_build)}
        for variant, spec in frozen["variants"].items():
            archive=root/(variant+".tar.gz")
            assert sha(archive)==spec["archive_sha256"]
            stage=out/("build-"+variant);stage.mkdir()
            with tarfile.open(archive) as tar:
                for item in tar.getmembers():
                    assert item.isfile() and not item.name.startswith("/") and ".." not in Path(item.name).parts
                tar.extractall(stage)
            for name, expected in {**spec["files"],**frozen["adapter_files"]}.items():
                assert sha(stage/name)==expected,(variant,name)
            binary=stage/("evaluation.exe" if platform.system()=="Windows" else "evaluation")
            if reused:
                previous=Path(reused["binaries"][variant]["path"])
                assert sha(previous)==reused["binaries"][variant]["sha256"]
                shutil.copyfile(previous,binary)
                shutil.copyfile(args.reuse_build/("tests-"+variant+".txt"),out/("tests-"+variant+".txt"))
            else:
                command([go,"build","-ldflags","-X main.variant="+variant,"-o",str(binary),"./cmd/evaluation"],stage,out/("build-"+variant+".txt"))
            if not args.pilot and not reused:
                tests=[go,"test","-race","-p","1","./internal/contextstate","./internal/contextvm","./internal/contextdispatch","-count=1"]
                if variant=="N":
                    tests += ["-run","TestPersistentCompressionFailureNeverPublishesCandidate|TestPersistentCompressionRejectsMetadataAndSourceTampering|TestPersistentCompressionRejectsNonTextSummaryBeforeCommit"]
                command(tests,stage,out/("tests-"+variant+".txt"))
            metadata["binaries"][variant]={"sha256":sha(binary),"path":str(binary),"source_sha256":spec["source_sha256"]}
            save(out/"run.json",metadata)
            print("Built "+variant,flush=True)
        if args.build_only:
            metadata["status"]="build-only";return
        cases=[]
        for op,size,recipients,concurrency in itertools.product(["output_publish","context_load","context_hydrate"],[256,4096,32768],[1,4],[1,4]):
            cases.append({"kind":"communication","case":{"operation":op,"payload_bytes":size,"recipients":recipients,"concurrency":concurrency}})
        for corpus,words,fraction in itertools.product(["english","mixed"],[256,2048],[1,4,8]):
            cases.append({"kind":"compression","corpus":corpus,"words":words,"fraction_eighths":fraction})
        if args.compression_only:cases=[c for c in cases if c["kind"]=="compression"]
        for case in cases:
            if case["kind"]=="compression":case["compression_read_passes"]=args.compression_read_passes
        if args.pilot:
            cases=[c for c in cases if (c["kind"]=="communication" and c["case"]["payload_bytes"]==32768 and c["case"]["recipients"]==4 and c["case"]["concurrency"]==4) or
                   (c["kind"]=="compression" and c["words"]==2048)]
        random.Random(20260922).shuffle(cases)
        permutations=list(itertools.permutations(range(3)))
        metadata["status"]="measuring"
        save(out/"run.json",metadata)
        for case_index,case in enumerate(cases):
            levels=[("A","off"),("B","off"),("C","off")] if case["kind"]=="communication" else [("C","off"),("N","no-cache"),("C","cache")]
            repeats=range(1) if args.pilot else range(7)
            for repeat in repeats:
                order=permutations[(repeat-1)%6] if repeat else (0,1,2)
                for level in order:
                    variant,mode=levels[level]
                    job=dict(case,repetition=repeat,mode=mode)
                    job_id=f"{case_index:03d}-{repeat}-{level}"
                    jobfile=out/(job_id+".json");save(jobfile,job)
                    trial=out/("trial-"+job_id)
                    entry={"id":job_id,"variant":variant,"warmup":repeat==0,"job":job,"started_at":datetime.now(timezone.utc).isoformat()}
                    began=time.monotonic()
                    command([metadata["binaries"][variant]["path"],str(jobfile),str(trial)],out,out/(job_id+".txt"),100)
                    entry.update(wall_seconds=time.monotonic()-began,result_sha256=sha(trial/"result.json"))
                    metadata["jobs"].append(entry);save(out/"run.json",metadata)
            print(f"Completed case {case_index+1}/{len(cases)}: {case}",flush=True)
        for variant,spec in frozen["variants"].items():
            for name,expected in {**spec["files"],**frozen["adapter_files"]}.items():
                assert sha(out/("build-"+variant)/name)==expected
            assert sha(Path(metadata["binaries"][variant]["path"]))==metadata["binaries"][variant]["sha256"]
        metadata["source_and_binary_unchanged"]=True
        metadata["status"]="complete"
    except BaseException as error:
        metadata["status"]="failed";metadata["failure"]=str(error);raise
    finally:
        metadata["finished_at"]=datetime.now(timezone.utc).isoformat()
        save(out/"run.json",metadata)
        print(out,flush=True)


if __name__=="__main__":
    main()
