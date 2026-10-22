"""Independently validate raw evidence and recompute contention metrics."""
from __future__ import annotations

import argparse
import base64
import calendar
import csv
import hashlib
import json
import math
import re
import statistics
from datetime import datetime
from pathlib import Path


def load(path: Path):
    return json.loads(path.read_text(encoding="utf-8"))


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def rows(path: Path):
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]


def ns(value: str) -> int:
    match = re.fullmatch(r"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)", value)
    if not match:
        raise ValueError(f"RFC3339 timestamp required: {value}")
    seconds = calendar.timegm(datetime.strptime(match[1], "%Y-%m-%dT%H:%M:%S").timetuple())
    if match[3] != "Z":
        offset = int(match[3][1:3]) * 3600 + int(match[3][4:6]) * 60
        seconds -= offset if match[3][0] == "+" else -offset
    return seconds * 1_000_000_000 + int((match[2] or "").ljust(9, "0") or "0")


def percentile95(values):
    ordered = sorted(values)
    return ordered[max(0, math.ceil(len(ordered) * 0.95) - 1)]


def jain(values):
    squared = sum(value * value for value in values)
    return sum(values) ** 2 / (len(values) * squared) if squared else None


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def verify_run(directory: Path):
    config, summary = load(directory / "config.json"), load(directory / "summary.json")
    require(summary["status"] == "complete", "run incomplete")
    require(config["policy"] == summary["policy"], "policy mismatch")
    require(config["model_requests"] == summary["model_requests"] == 0, "model calls found")
    wall_elapsed = ns(summary["finished_at"]) - ns(config["started_at"])
    require(wall_elapsed == summary["elapsed_wall_ns"], "wall elapsed differs from raw endpoints")
    require(summary["clock_skew_ns"] == wall_elapsed - summary["elapsed_ns"], "clock skew arithmetic mismatch")
    require(abs(summary["clock_skew_ns"]) <= max(5_000_000, summary["elapsed_ns"] * 0.005), "wall/monotonic disagreement exceeds 5ms or 0.5%")
    specs = {task["id"]: task for group in config["tasks"].values() for task in group}
    require(len(specs) == 2 * config["tasks_per_workflow"], "input task count mismatch")
    events = rows(directory / "events.jsonl")
    grouped = {task_id: [] for task_id in specs}
    generations = []
    for event in events:
        require(event["task_id"] in specs, "unknown transition task")
        grouped[event["task_id"]].append(event)
        generations.append(event["generation"])
        wall_progress = ns(event["observed_at"]) - ns(config["started_at"])
        require(abs(wall_progress - event["observed_ns"]) <= max(5_000_000, event["observed_ns"] * 0.005), "event wall/monotonic disagreement exceeds 5ms or 0.5%")
        for resource in ("cpu_milli", "memory_mib", "llm_slots", "token_budget"):
            require(0 <= event["allocated"][resource] <= config["capacity"][resource], "reservation exceeds capacity")
    require(generations == sorted(generations), "commit generations out of order")
    for task_id, task_events in grouped.items():
        require([event["status"] for event in task_events] == ["READY", "ADMITTED", "RUNNING", "SUCCEEDED"], f"invalid lifecycle: {task_id}")
        require([event["observed_ns"] for event in task_events] == sorted(event["observed_ns"] for event in task_events), "observation order mismatch")
        require(all(event["workflow_id"] == specs[task_id]["workflow_id"] for event in task_events), "workflow mismatch")
    outputs = rows(directory / "results.jsonl")
    by_task = {}
    intervals = []
    task_metrics = []
    artifact_hashes = {}
    origin = min(ns(task_events[0]["observed_at"]) for task_events in grouped.values())
    workflow_ready = {workflow: min(ns(grouped[task["id"]][0]["observed_at"]) for task in tasks) for workflow, tasks in config["tasks"].items()}
    for row in outputs:
        require(not row.get("error"), "execution error")
        result = row["result"]
        attempt, process = result["execution"]["attempt"], result["execution"]["process"]
        task_id = attempt["task_id"]
        require(task_id in specs and task_id not in by_task, "missing or repeated task")
        by_task[task_id] = result
        require(attempt["status"] == "SUCCEEDED" and attempt["number"] == 1, "attempt incorrect")
        require(process["process_started"] and process["wait_completed"] and process["exit_code"] == 0, "child not reaped successfully")
        require(not process["timed_out"] and not process["canceled"], "child interrupted")
        require(result["decision"]["policy"] == config["policy"], "decision policy mismatch")
        require(result["decision"]["candidate"]["task_id"] == task_id, "decision task mismatch")
        process_start, process_end = ns(process["process_started_at"]), ns(process["wait_completed_at"])
        preparation_start = ns(process["preparation_started_at"])
        ready_at, admitted_at = (ns(event["observed_at"]) for event in grouped[task_id][:2])
        require(ready_at <= admitted_at <= preparation_start <= process_start <= process_end, "process preparation/start-confirmation/commit timestamps invalid")
        child = load(directory / "artifacts" / f"{task_id}.process.json")
        require(child["task_id"] == task_id and child["workflow_id"] == specs[task_id]["workflow_id"] and child["pid"] > 0, "child identity mismatch")
        child_start, child_end = ns(child["started_at"]), ns(child["finished_at"])
        # command.Start can return after the Linux child has begun execution.
        # ProcessStartedAt is the parent's confirmation, not a launch barrier.
        require(preparation_start <= child_start <= child_end <= process_end, "child metadata outside preparation-to-wait interval")
        duration = config[f"{specs[task_id]['workflow_id']}_duration_ns"]
        require(child["requested_duration_ns"] == duration, "different child duration")
        require(ns(child["finished_at"]) - ns(child["started_at"]) >= duration, "child hold duration too short")
        artifact = directory / "artifacts" / f"{task_id}.txt"
        expected = f"aegisrt-contention/v1:{task_id}\n".encode()
        require(artifact.read_bytes() == expected and digest(artifact) == child["artifact_sha256"], "artifact content mismatch")
        artifact_hashes[task_id] = digest(artifact)
        workflow = specs[task_id]["workflow_id"]
        intervals.append((process_start, process_end, workflow, task_id))
        task_metrics.append({"task_id": task_id, "workflow": workflow, "ready_at_ns": ready_at,
                             "admitted_at_ns": admitted_at, "preparation_start_ns": preparation_start,
                             "process_start_ns": process_start, "process_start_confirmation_ns": process_start,
                             "child_work_start_ns": child_start, "process_end_ns": process_end,
                             "ready_to_admit_ns": admitted_at - ready_at, "ready_to_start_ns": process_start - ready_at,
                             "ready_to_child_work_start_ns": child_start - ready_at})
    require(set(by_task) == set(specs), "input/result sets differ")
    expected_files = {f"{task_id}.{suffix}" for task_id in specs for suffix in ("txt", "process.json")}
    require({file.name for file in (directory / "artifacts").iterdir()} == expected_files, "unexpected/missing artifact files")
    edges = sorted([(start, 1) for start, _, _, _ in intervals] + [(end, -1) for _, end, _, _ in intervals])
    running = peak = 0
    for _, delta in edges:
        running += delta
        peak = max(peak, running)
        require(0 <= running <= config["workers"], "real process concurrency exceeds slots")
    require(running == 0, "process interval leak")
    state_envelope = load(directory / "runtime.state")
    payload = base64.b64decode(state_envelope["payload"])
    require(hashlib.sha256(payload).hexdigest() == state_envelope["checksum"], "checkpoint checksum incorrect")
    checkpoint = json.loads(payload)
    require({task["spec"]["id"] for task in checkpoint["graph"]["tasks"]} == set(specs), "persisted tasks mismatch")
    require(all(task["status"] == "SUCCEEDED" for task in checkpoint["graph"]["tasks"]), "persisted task unfinished")
    require(all(attempt["number"] == 1 for attempt in checkpoint["attempts"]), "persisted repeated attempt")
    require(not checkpoint.get("pending_completions") and not checkpoint["scheduler"]["reservations"], "persisted resource/completion leak")
    for resource in ("cpu_milli", "memory_mib", "llm_slots", "token_budget"):
        require(checkpoint["scheduler"]["allocated"][resource] == summary["final_runtime"]["allocated"][resource] == 0, "nonzero final allocation")
    workflow_end = {workflow: max(end for _, end, row_workflow, _ in intervals if row_workflow == workflow) for workflow in config["tasks"]}
    window_start, window_end = max(workflow_ready.values()), min(workflow_end.values())
    require(window_end > window_start, "common demand window empty")
    occupied = {workflow: sum(max(0, min(end, window_end) - max(start, window_start)) for start, end, row_workflow, _ in intervals if row_workflow == workflow) / 1e9 for workflow in config["tasks"]}
    completions = {workflow: sum(window_start <= end <= window_end for _, end, row_workflow, _ in intervals if row_workflow == workflow) for workflow in config["tasks"]}
    makespan = max(workflow_end.values()) - origin
    short = [task for task in task_metrics if task["workflow"] == "short"]
    metrics = {"policy": config["policy"], "tasks": len(specs), "peak_processes": peak,
               "makespan_seconds": makespan / 1e9, "throughput_tasks_per_second": len(specs) / (makespan / 1e9),
               "short_ready_to_admit_p95_seconds": percentile95([task["ready_to_admit_ns"] for task in short]) / 1e9,
               "short_ready_to_start_p95_seconds": percentile95([task["ready_to_start_ns"] for task in short]) / 1e9,
               "short_ready_to_child_work_start_p95_seconds": percentile95([task["ready_to_child_work_start_ns"] for task in short]) / 1e9,
               "short_ready_to_start_mean_seconds": statistics.mean(task["ready_to_start_ns"] for task in short) / 1e9,
               "workflow_completion_seconds": {workflow: (end - workflow_ready[workflow]) / 1e9 for workflow, end in workflow_end.items()},
               "common_demand_window_seconds": (window_end - window_start) / 1e9,
               "common_demand_process_seconds": occupied, "common_demand_jain_reserved_time": jain(list(occupied.values())),
               "common_demand_completed_tasks": completions, "common_demand_jain_task_counts": jain(list(completions.values())),
               "resource_zero": True, "all_children_waited": True, "artifact_sha256": artifact_hashes,
               "trial_elapsed_monotonic_seconds": summary["elapsed_ns"] / 1e9,
               "trial_elapsed_wall_seconds": wall_elapsed / 1e9, "clock_skew_ns": summary["clock_skew_ns"],
               "model_requests": 0, "kernel_resource_limits": False}
    write(directory / "metrics.json", metrics)
    with (directory / "task-metrics.csv").open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(task_metrics[0]))
        writer.writeheader()
        writer.writerows(sorted(task_metrics, key=lambda task: task["task_id"]))
    return metrics


def write(path, data):
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def verify_matrix(directory):
    manifest = load(directory / "manifest.json")
    require(digest(directory / manifest["binary_path"]) == manifest["binary_sha256"], "binary changed")
    for relative, sha in manifest["sources"].items():
        require(digest(directory / "source" / relative) == sha, f"source changed: {relative}")
    require(manifest["protocol"]["repetitions"] >= 3, "at least three measured repetitions required")
    metrics = []
    baseline_inputs = {}
    baseline_artifacts = {}
    for entry in manifest["runs"]:
        run_dir = directory / entry["directory"]
        current = verify_run(run_dir)
        config = load(run_dir / "config.json")
        require(config["binary_sha256"] == manifest["binary_sha256"], "run binary differs")
        require(config["policy"] == entry["policy"], "manifest policy mismatch")
        if entry["phase"] != "measured":
            continue
        current.update({key: entry[key] for key in ("scenario", "repetition", "order", "directory")})
        input_data = {key: config[key] for key in ("workers", "capacity", "tasks_per_workflow", "tasks", "short_arrival_ns", "long_duration_ns", "short_duration_ns")}
        scenario = entry["scenario"]
        if scenario in baseline_inputs:
            require(input_data == baseline_inputs[scenario], "matched workload differs")
            require(current["artifact_sha256"] == baseline_artifacts[scenario], "matched artifact sets differ")
        baseline_inputs[scenario], baseline_artifacts[scenario] = input_data, current["artifact_sha256"]
        metrics.append(current)
    policies = ["fifo", "dag-drcfs", "dag-drcfs-time"]
    expected = {(scenario, repetition, policy) for scenario in manifest["protocol"]["scenarios"] for repetition in range(1, manifest["protocol"]["repetitions"] + 1) for policy in policies}
    require(len(metrics) == len(expected), "repeated or missing matrix entries")
    require({(row["scenario"], row["repetition"], row["policy"]) for row in metrics} == expected, "matrix incomplete")
    scalar = ["short_ready_to_start_p95_seconds", "short_ready_to_child_work_start_p95_seconds", "short_ready_to_admit_p95_seconds", "makespan_seconds", "throughput_tasks_per_second", "common_demand_jain_reserved_time"]
    comparisons = []
    aggregate = []
    for scenario in manifest["protocol"]["scenarios"]:
        for policy in policies:
            group = [row for row in metrics if row["scenario"] == scenario and row["policy"] == policy]
            aggregate.append({"scenario": scenario, "policy": policy, "n": len(group),
                              **{key: {"median": statistics.median(row[key] for row in group), "min": min(row[key] for row in group), "max": max(row[key] for row in group)} for key in scalar}})
        for repetition in range(1, manifest["protocol"]["repetitions"] + 1):
            group = {row["policy"]: row for row in metrics if row["scenario"] == scenario and row["repetition"] == repetition}
            for policy in policies[1:]:
                comparisons.append({"scenario": scenario, "repetition": repetition, "policy": policy,
                                    "delta_vs_fifo": {key: group[policy][key] - group["fifo"][key] for key in scalar}})
    report = {"passed": True, "measured_runs": len(metrics), "warmup_runs": sum(entry["phase"] == "warmup" for entry in manifest["runs"]),
              "model_requests": 0, "aggregate": aggregate, "paired_comparisons": comparisons,
              "metric_definitions": {"p95": "nearest rank ceil(0.95*n); committed READY observation to Executor process-start confirmation after command.Start returns",
                                     "child_work_start_p95": "same nearest rank; committed READY observation to child work start recorded after stdin decoding; it may precede parent start confirmation",
                                     "jain_reserved_time": "Jain index of each workflow's integrated confirmed process occupancy from Executor start confirmation to Wait completion over the common outstanding-demand window; identical resource vectors; not CPU utilization",
                                     "common_window": "latest first READY observation to earliest last child end among workflows; includes waiting and running demand",
                                     "makespan": "first committed READY observation to last real child wait completion",
                                     "throughput": "all completed tasks divided by makespan"},
              "boundaries": ["two finite independent-task workflows; DAG critical-path ranking untested", "reserved-capacity sleepers; no cgroup limits or actual CPU contention", "three repetitions are descriptive, with no significance or confidence interval claim", "fresh process and durable state per run; system cache is warmed uniformly, not flushed", "no model or business-quality comparison"]}
    write(directory / "validation.json", report)
    flat = [{key: value for key, value in row.items() if not isinstance(value, dict)} for row in metrics]
    with (directory / "metrics.csv").open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(flat[0]))
        writer.writeheader()
        writer.writerows(flat)
    return report


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    if (args.directory / "manifest.json").exists():
        report = verify_matrix(args.directory)
        print(json.dumps({"passed": report["passed"], "measured_runs": report["measured_runs"], "model_requests": 0}))
    else:
        report = verify_run(args.directory)
        print(json.dumps({"passed": True, "policy": report["policy"], "tasks": report["tasks"], "peak_processes": report["peak_processes"]}))


if __name__ == "__main__":
    main()
