"""Independently check raw bodies, provenance, matrices, hashes and aggregates."""
import argparse
import base64
from collections import Counter, defaultdict
import csv
import hashlib
import itertools
import json
from pathlib import Path
import statistics
import struct
import tarfile
import unicodedata


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def read(path):
    return json.loads(path.read_text(encoding="utf-8-sig"))


def write(path, data):
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def count_prefix(text, counter, budget=None):
    tokens, word, end = 0, False, 0
    for i, char in enumerate(text):
        if char.isspace():
            word = False
        elif counter == "whitespace-v1":
            tokens += not word
            word = True
        elif "\u4e00" <= char <= "\u9fff":
            tokens += 1
            word = False
        elif unicodedata.category(char)[0] in "LNM" or char == "_":
            tokens += not word
            word = True
        else:
            tokens += 1
            word = False
        if budget is not None and tokens > budget:
            return text[:end].strip()
        end = i + 1
    return tokens if budget is None else text.strip()


def checkpoint(path):
    envelope = read(path)
    payload = base64.b64decode(envelope["payload"], validate=True)
    require(sha(payload) == envelope["checksum"], str(path) + ": envelope checksum")
    value = json.loads(payload)
    blobs = {b["content_hash"]: b for b in value["store"].get("blobs", [])}
    for key, blob in blobs.items():
        require(sha(blob["content"].encode()) == key, "blob digest")
        require(count_prefix(blob["content"], value.get("counter_identity", "whitespace-v1")) == blob["token_count"], "blob token count")
    for page in value["store"].get("pages", []):
        require(page["content_hash"] in blobs, "page references absent blob")
        fields=[page["content_hash"],page["scope"],page.get("workflow_id",""),page.get("agent_id",""),
            str(blobs[page["content_hash"]]["token_count"])]
        for ref in page.get("source_refs",[]):
            fields += [ref["page_id"],str(ref.get("pin",False)).lower(),str(ref.get("cold",False)).lower()]
        encoded=b"".join(struct.pack(">Q",len(f.encode()))+f.encode() for f in fields)
        require(sha(encoded)==page["id"], "page identity/ownership/provenance digest")
    return value, blobs


def verify_communication(result, job, variant, directory, read_only_dependencies=False):
    c = result["case"]
    for key, val in job["case"].items():
        require(c[key] == val, "communication input drift")
    require(result["repetition"] == job["repetition"], "repetition drift")
    passes = 32 if c["operation"] == "context_load" else 1
    require(c["read_passes"] == passes and len(result["samples"]) == 4, "sample shape")
    latencies = []
    for i, sample in enumerate(result["samples"]):
        require(sample["index"] == i and sample["latency_ns"] >= 0, "latency/index invalid")
        require(sample["delivered_bytes"] == c["payload_bytes"] * c["recipients"] * passes, "delivery bytes")
        latencies.append(sample["latency_ns"])
    ordered = sorted(latencies)
    require(result["p50_ns"] == ordered[1] and result["p95_ns"] == ordered[3], "raw quantile mismatch")
    require(result["wall_ns"] >= 0, "negative wall time")
    expected_saves = {"context_load": 0, "output_publish": 4, "context_hydrate": 4*c["recipients"]}[c["operation"]]
    if read_only_dependencies and c["operation"] == "context_hydrate": expected_saves = 0
    require(result["checkpoint_save_calls"] == expected_saves, "checkpoint save count")
    require((directory/"replay-before.state").read_bytes() == (directory/"replay-after.state").read_bytes(), "raw replay changed")
    require((directory/"context.state").read_bytes() == (directory/"replay-after.state").read_bytes(), "reload changed durable bytes")
    stored, blobs = checkpoint(directory/"context.state")
    require(len(stored["outputs"]) == 4 and len(blobs) == 1, "output/dedup count")
    payload = ("payload " * ((c["payload_bytes"]+7)//8))[:c["payload_bytes"]]
    require(next(iter(blobs.values()))["content"] == payload, "raw payload differs")
    require(stored["format_version"] == (1 if variant=="A" else 2), "format identity")
    probes = read(directory/"isolation-probes.json")
    require(len(probes) == 4, "probe count")
    for probe in probes:
        observed = probe["observed_text"]
        expected = payload if variant=="A" else ""
        require(observed == expected, "unexpected isolation outcome")
        isolated = not observed and not probe.get("observed_refs")
        require(probe["isolated"] == isolated, "isolation flag mismatch")
    require(result["isolation_verified"] == (variant!="A"), "isolation summary mismatch")
    require(result["replay_stable"] and result["reload_verified"], "replay/reload not verified")
    return {"variant":variant,"operation":c["operation"],"payload_bytes":c["payload_bytes"],
        "recipients":c["recipients"],"concurrency":c["concurrency"],"repetition":job["repetition"],
        "latency_ms":result["p50_ns"] / passes / 1e6,
        "p95_ms":result["p95_ns"] / passes / 1e6,
        "throughput_ops_s":4*passes*1e9/result["wall_ns"] if result["wall_ns"]>0 else None,
        "zero_latency_samples":sum(t==0 for t in latencies),"zero_wall_window":result["wall_ns"]==0,
        "alloc_kib":result["total_alloc_bytes"]/4/passes/1024,
        "save_calls":result["checkpoint_save_calls"],"written_bytes":result["checkpoint_payload_written_bytes"],
        "checkpoint_bytes":result["checkpoint_after_bytes"],"isolated":variant!="A"}


def verify_compression(result, job, directory):
    source = read(directory/"input.json")
    require(source == result["input"], "input file/report mismatch")
    text, counter, budget = source["content"], source["counter"], source["budget"]
    words = [f"item{i:04d}" if job["corpus"]=="english" else f"数据{i:04d}" for i in range(job["words"])]
    facts = ["FACT_HEAD_731", "FACT_MIDDLE_947", "FACT_TAIL_563"]
    for position,fact in zip([job["words"]//10,job["words"]//2,job["words"]*9//10],facts):
        words[position]=fact
    require(text==" ".join(words) and source["facts"]==facts, "fixed corpus mismatch")
    tokens = count_prefix(text, counter)
    require(tokens == source["source_tokens"] and budget == tokens*job["fraction_eighths"]//8, "source budget oracle")
    truncated = budget < tokens
    expected = text if not truncated else ("" if job["mode"]=="off" else count_prefix(text,counter,budget))
    require(len(result["samples"]) == 10, "compression sample count")
    final, blobs = checkpoint(directory/"context.state")
    initial, _ = checkpoint(directory/"initial.state")
    cold, _ = checkpoint(directory/"cold.state")
    require((directory/"cold.state").read_bytes() == (directory/"context.state").read_bytes(), "repeat reads changed checkpoint")
    summaries = final.get("summaries", [])
    compressed = truncated and job["mode"]!="off"
    require(len(summaries) == int(compressed), "summary record count")
    require(cold["generation"] - initial["generation"] == int(compressed), "generation delta")
    pages = {p["id"]:p for p in final["store"]["pages"]}
    require(blobs[pages[source["source_page_id"]]["content_hash"]]["content"]==text, "source bytes lost")
    for record in summaries:
        require(record["source_id"]==source["source_page_id"] and record["token_budget"]==budget, "summary binding")
        page = pages[record["summary_id"]]
        require(page["source_refs"] == [{"page_id":source["source_page_id"]}], "summary provenance")
        require(blobs[page["content_hash"]]["content"] == expected, "summary body")
    rows=[]
    for i,s in enumerate(result["samples"]):
        phase="cold" if i==0 else ("restart" if i==9 else "warm")
        passes=job.get("compression_read_passes",1) if phase=="warm" else 1
        require(s.get("read_passes",1)==passes, "compression batch size")
        require(s["index"]==i and s["phase"]==phase and s["text"]==expected, "text oracle or phase mismatch")
        actual_tokens = count_prefix(s["text"],counter)
        require(s["metrics"]["physical_tokens"]==actual_tokens<=budget, "output budget mismatch")
        require(s["facts_retained"]==[fact in expected for fact in facts], "fact retention mismatch")
        calls=passes*int(compressed and (i==0 or job["mode"]=="no-cache"))
        require(s["summary_calls"]==calls and s["save_calls"]==int(compressed and i==0), "cache/save count")
        require(s["checkpoint_bytes"]==(directory/"context.state").stat().st_size, "checkpoint size")
        require(s["source_verified"] and s["isolation_verified"], "protection assertion")
        for ref in s.get("refs") or []:
            page=pages[ref["page_id"]]
            require(page["workflow_id"]=="workflow-000" and page["agent_id"]=="consumer-01", "ownership")
            require(blobs[page["content_hash"]]["content"]==expected, "resolved ref body")
        require(s["latency_ns"]>=0 and s["alloc_bytes"]>=0, "negative measurement")
        rows.append({"corpus":job["corpus"],"words":job["words"],"fraction_eighths":job["fraction_eighths"],
            "mode":job["mode"],"repetition":job["repetition"],"phase":phase,"index":i,
            "read_passes":passes,"raw_batch_latency_ns":s["latency_ns"],
            "latency_ms":s["latency_ns"]/passes/1e6,"reload_ms":s["reload_ns"]/1e6,"alloc_kib":s["alloc_bytes"]/passes/1024,
            "summary_calls":s["summary_calls"],"save_calls":s["save_calls"],"written_bytes":s["written_bytes"],
            "summary_calls_per_read":s["summary_calls"]/passes,
            "retained_fraction":actual_tokens/tokens,"facts_fraction":sum(s["facts_retained"])/3,
            "checkpoint_bytes":s["checkpoint_bytes"],"budget":budget,"source_tokens":tokens})
    return rows


def save_csv(path, rows):
    require(bool(rows), "empty result table")
    with path.open("w",newline="",encoding="utf-8-sig") as stream:
        writer=csv.DictWriter(stream,fieldnames=list(rows[0]));writer.writeheader();writer.writerows(rows)


def summarize(rows, keys, metrics, per_sequence=False):
    grouped=defaultdict(list)
    for row in rows:
        grouped[tuple(row[k] for k in keys)].append(row)
    summary=[]
    for group, samples in sorted(grouped.items()):
        by_rep=defaultdict(list)
        for row in samples: by_rep[row["repetition"]].append(row)
        value=dict(zip(keys,group),n=len(by_rep))
        for metric in metrics:
            # The eight warm reads are repeated observations in one trial, not eight independent trials.
            estimates=[]
            for rep in by_rep.values():
                defined=[r[metric] for r in rep if r[metric] is not None]
                if defined:estimates.append(statistics.mean(defined))
            value[metric+"_defined_n"]=len(estimates)
            value[metric+"_undefined_n"]=len(by_rep)-len(estimates)
            value[metric+"_median"]=statistics.median(estimates) if estimates else None
            value[metric+"_min"]=min(estimates) if estimates else None
            value[metric+"_max"]=max(estimates) if estimates else None
        summary.append(value)
    return summary


def main():
    parser=argparse.ArgumentParser();parser.add_argument("run",type=Path);args=parser.parse_args()
    directory=args.run.resolve();meta=read(directory/"run.json");root=directory.parent
    require(meta["status"]=="complete" and meta["source_and_binary_unchanged"], "run incomplete")
    require(sha((root/"sources.json").read_bytes())==meta["sources_manifest_sha256"], "source manifest identity")
    frozen=read(root/"sources.json")
    for variant,spec in frozen["variants"].items():
        canonical="\n".join(k+" "+v for k,v in sorted(spec["files"].items())).encode()
        require(sha(canonical)==spec["source_sha256"], "source inventory digest")
        require(meta["binaries"][variant]["source_sha256"]==spec["source_sha256"], "binary/source binding")
        archive=root/(variant+".tar.gz")
        require(sha(archive.read_bytes())==spec["archive_sha256"], "source archive hash")
        with tarfile.open(archive) as tar:
            for name,expected in {**spec["files"],**frozen["adapter_files"]}.items():
                require(sha(tar.extractfile(name).read())==expected, "archived source identity")
        binary="evaluation.exe" if meta.get("platform")=="Windows" else "evaluation"
        require(sha((directory/("build-"+variant)/binary).read_bytes())==meta["binaries"][variant]["sha256"], "binary digest")
    comm,comp,all_counts=[],[],Counter()
    shape=Counter()
    for entry in meta["jobs"]:
        trial=directory/("trial-"+entry["id"]);path=trial/"result.json"
        require(sha(path.read_bytes())==entry["result_sha256"], "raw report digest")
        report=read(path);job=entry["job"]
        require(report["success"] and report["variant"]==entry["variant"], "failed/mismatched trial")
        for key,val in job.items():
            if isinstance(val,dict):
                for subkey,subval in val.items():require(report["job"][key][subkey]==subval, "job/report drift")
            else:require(report["job"][key]==val, "job/report drift")
        require(entry["warmup"]==(job["repetition"]==0), "warmup flag")
        kind=job["kind"];all_counts[kind]+=1
        if kind=="communication":
            row=verify_communication(report["result"],job,entry["variant"],trial,entry["variant"] in frozen.get("dependency_reads_without_write",[]))
            if not entry["warmup"]: comm.append(row)
            key=(kind,tuple(sorted(job["case"].items())),entry["variant"],job["repetition"])
        else:
            rows=verify_compression(report["result"],job,trial)
            if not entry["warmup"]:comp.extend(rows)
            key=(kind,job["corpus"],job["words"],job["fraction_eighths"],job["mode"],job["repetition"])
        shape[key]+=1
    require(all(v==1 for v in shape.values()), "duplicate matrix cells")
    if not meta["pilot"]:
        expected_comm=0 if meta.get("compression_only") else 756
        require(all_counts==Counter(communication=expected_comm,compression=252), "incomplete matrix")
        require(len(comm)==(0 if meta.get("compression_only") else 648) and len(comp)==2160, "measurement counts")
        orders=defaultdict(list)
        for entry in meta["jobs"]:
            if not entry["warmup"]: orders[entry["id"].rsplit("-",1)[0]].append(int(entry["id"].rsplit("-",1)[1]))
        by_case=defaultdict(set)
        for key,val in orders.items():by_case[key.split("-")[0]].add(tuple(val))
        require(all(v==set(itertools.permutations(range(3))) for v in by_case.values()), "counterbalancing")
    out=directory/"analysis";out.mkdir(exist_ok=True)
    if comm:
        save_csv(out/"communication-trials.csv",comm)
        save_csv(out/"communication-summary.csv",summarize(comm,["operation","payload_bytes","recipients","concurrency","variant"],
            ["latency_ms","throughput_ops_s","alloc_kib","save_calls","written_bytes","checkpoint_bytes"]))
        keys=["operation","payload_bytes","recipients","concurrency"]
        pairs=defaultdict(dict)
        for row in comm:
            pairs[tuple(row[k] for k in keys)+(row["repetition"],)][row["variant"]]=row
        paired=[]
        for key,values in sorted(pairs.items()):
            require(set(values)=={"A","B","C"}, "missing paired variant")
            row=dict(zip(keys+["repetition"],key))
            for metric in ("latency_ms","throughput_ops_s","alloc_kib"):
                denominator,numerator=values["B"][metric],values["C"][metric]
                row[metric+"_C_over_B"]=numerator/denominator if denominator is not None and numerator is not None and denominator>0 and numerator>0 else None
            paired.append(row)
        save_csv(out/"communication-paired-ratios.csv",paired)
        save_csv(out/"communication-paired-summary.csv",summarize(paired,keys,
            ["latency_ms_C_over_B","throughput_ops_s_C_over_B","alloc_kib_C_over_B"]))
    if comp:
        save_csv(out/"compression-reads.csv",comp)
        save_csv(out/"compression-summary.csv",summarize(comp,["corpus","words","fraction_eighths","mode","phase"],
            ["latency_ms","alloc_kib","reload_ms","retained_fraction","facts_fraction","summary_calls","summary_calls_per_read","save_calls","checkpoint_bytes"]))
    verification={"passed":True,"pilot":meta["pilot"],"all_trial_counts":dict(all_counts),
        "communication_measured_trials":len(comm),"communication_samples":len(comm)*4,"compression_read_samples":len(comp),
        "communication_zero_latency_samples":sum(r["zero_latency_samples"] for r in comm),
        "communication_zero_wall_windows":sum(r["zero_wall_window"] for r in comm),
        "compression_zero_latency_samples":sum(r["latency_ms"]==0 for r in comp),
        "compression_warm_zero_latency_samples":sum(r["latency_ms"]==0 and r["phase"]=="warm" for r in comp),
        "compression_logical_reads":sum(r["read_passes"] for r in comp),
        "undefined_derived_values":"zero wall windows have no throughput; paired ratios with zero or undefined terms remain blank; per-metric defined counts reported",
        "raw_bodies_provenance_envelopes_hashes_verified":True,"zero_samples_preserved":True,
        "baseline_isolation_failure_is_expected_and_reported":True,
        "statistics":"median and full range across six trial estimates; warm reads averaged within trial; no significance test",
        "verifier_sha256":sha(Path(__file__).read_bytes())}
    write(out/"verification.json",verification);print(json.dumps(verification,ensure_ascii=False))


if __name__=="__main__":main()
