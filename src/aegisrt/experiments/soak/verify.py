"""Independently audit raw process intervals, samples and durable final state."""
import argparse
import base64
from collections import Counter, defaultdict
from datetime import datetime, timedelta
import hashlib
import json
from pathlib import Path


def require(value, message):
    if not value:
        raise ValueError(message)


def stamp(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def jain(values):
    return sum(values)**2 / (len(values) * sum(v*v for v in values))


def verify(directory):
    read = lambda name: json.loads((directory/name).read_text(encoding="utf-8"))
    summary = read("summary.json")
    events = [json.loads(s) for s in (directory/"events.jsonl").read_text().splitlines()]
    samples = [json.loads(s) for s in (directory/"samples.jsonl").read_text().splitlines()]
    envelope = read("runtime.state")
    payload = base64.b64decode(envelope["payload"], validate=True)
    require(hashlib.sha256(payload).hexdigest() == envelope["checksum"], "state checksum")
    final = json.loads(payload)
    require(summary["status"] == "complete", "incomplete run")
    start = stamp(summary["started_at"])
    end = start + timedelta(seconds=summary["requested_seconds"])
    require(stamp(summary["finished_at"]) >= end, "insufficient duration")
    counts, seconds, window, edges = Counter(), defaultdict(float), defaultdict(float), []
    ids = set()
    for event in events:
        require(not event.get("error"), "process error")
        require(event["task"] not in ids, "duplicate execution")
        ids.add(event["task"])
        a, b = stamp(event["started"]), stamp(event["finished"])
        require(start <= a < b <= stamp(summary["finished_at"]), "invalid process interval")
        duration = (b-a).total_seconds()
        require(abs(duration-event["process_seconds"]) < 0.00001, "duration mismatch")
        workflow = event["workflow"]
        counts[workflow] += 1
        seconds[workflow] += event["process_seconds"]
        window[workflow] += max(0, (min(b, end)-max(a, start)).total_seconds())
        edges += [(a, 1), (b, -1)]
    active = peak = 0
    for _, change in sorted(edges):
        active += change
        require(0 <= active <= 4, "process concurrency exceeded")
        peak = max(peak, active)
    require(active == 0, "unfinished interval")
    require(dict(counts) == summary["completed"], "completion summary mismatch")
    for workflow, total in seconds.items():
        require(abs(total-summary["process_seconds"][workflow]) < 0.00001, "time summary mismatch")
    tasks = final["graph"]["tasks"]
    require({t["spec"]["id"] for t in tasks} == ids, "durable task/event mismatch")
    require(all(t["status"] == "SUCCEEDED" for t in tasks), "durable task not successful")
    require(not final.get("pending_completions"), "pending completion leak")
    require(not final["scheduler"].get("reservations"), "reservation leak")
    require(all(v == 0 for v in summary["final_runtime"]["allocated"].values()), "allocated leak")
    require(len(samples) >= summary["requested_seconds"]*0.9, "insufficient samples")
    previous = -1
    for sample in samples:
        require(sample["elapsed_seconds"] > previous, "sample clock moved backward")
        previous = sample["elapsed_seconds"]
        stats = sample["runtime"]
        require(stats["checkpoint_healthy"], "unhealthy checkpoint")
        for resource in ("cpu_milli", "memory_mib", "llm_slots", "token_budget"):
            require(0 <= stats["allocated"].get(resource, 0) <= stats["capacity"].get(resource, 0), "capacity exceeded")
    result = {"passed": True, "policy": summary["policy"], "duration_seconds": summary["requested_seconds"],
        "completed": dict(counts), "unique_successful_attempts": len(events), "peak_processes": peak,
        "window_process_seconds": dict(window), "window_occupancy_jain": jain(list(window.values())),
        "window_capacity_utilization": sum(window.values())/(4*summary["requested_seconds"]),
        "sample_count": len(samples), "heap_alloc_min": min(s["heap_alloc"] for s in samples),
        "heap_alloc_max": max(s["heap_alloc"] for s in samples),
        "goroutines_min": min(s["goroutines"] for s in samples), "goroutines_max": max(s["goroutines"] for s in samples),
        "final_all_tasks_succeeded": True, "final_reservations_empty": True, "final_pending_completions_empty": True,
        "scope": "one synthetic process run per policy; clipped elapsed occupancy, not CPU consumption or production reliability",
        "raw_sha256": {name: hashlib.sha256((directory/name).read_bytes()).hexdigest() for name in ("summary.json", "events.jsonl", "samples.jsonl", "runtime.state")},
        "verifier_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}
    (directory/"verification.json").write_text(json.dumps(result, indent=2)+"\n", encoding="utf-8")
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("directories", type=Path, nargs="+")
    args = parser.parse_args()
    for directory in args.directories:
        print(json.dumps(verify(directory)))
