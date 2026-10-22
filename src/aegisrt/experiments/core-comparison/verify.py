"""Recompute trace invariants and metrics without importing the Go simulator."""
import argparse
import hashlib
import json
import math
from collections import Counter, defaultdict
from pathlib import Path


def require(condition, message):
    if not condition:
        raise ValueError(message)


def vector(value):
    result = {key: value.get(key, 0) for key in ("cpu_milli", "memory_mib", "llm_slots", "token_budget")}
    result.update({"tool:" + key: amount for key, amount in value.get("tool_slots", {}).items()})
    return result


def close(actual, expected, message):
    require(math.isclose(actual, expected, abs_tol=1e-10, rel_tol=1e-10), message)


def verify_trial(trial, variant):
    workload = trial["workload"]
    capacity = vector(workload["capacity"])
    tasks = {task["id"]: task for task in workload["tasks"]}
    require(len(tasks) == len(workload["tasks"]), "duplicate task IDs")
    pending, running, finished = set(tasks), {}, {}
    settled = defaultdict(float)
    first_wave = Counter()
    previous = -1
    shares = {key: max(amount / capacity[name] for name, amount in vector(task["resources"]).items() if capacity.get(name, 0)) for key, task in tasks.items()}
    for event in trial["events"]:
        now, task_id, kind = event["logical_ms"], event["task"], event["kind"]
        require(now >= previous and task_id in tasks, "invalid event ordering or task")
        previous = now
        task = tasks[task_id]
        require(event["workflow"] == task["workflow"], "event workflow mismatch")
        if kind == "dispatch":
            require(task_id in pending and task["arrival_ms"] <= now, "duplicate or premature dispatch")
            require(all(end > now for start, end in running.values()), "unfinished completion before admission")
            reserved = defaultdict(float)
            used = Counter()
            for running_id in running:
                current = tasks[running_id]
                used.update(vector(current["resources"]))
                reserved[current["workflow"]] += shares[running_id] / (current["priority"] + 1)
            feasible = [tasks[key] for key in pending if tasks[key]["arrival_ms"] <= now and all(
                used[name] + amount <= capacity.get(name, 0) for name, amount in vector(tasks[key]["resources"]).items())]
            workflow = min({item["workflow"] for item in feasible}, key=lambda key: (settled[key] + (reserved[key] if variant == "C" else 0), key))
            expected = min((item for item in feasible if item["workflow"] == workflow),
                           key=lambda item: (-(item["priority"] * 4 + (now - item["arrival_ms"]) / 1000), item["id"]))
            require(task_id == expected["id"], "policy decision differs from independent ordering")
            pending.remove(task_id)
            running[task_id] = (now, now + task["service_ms"])
            if now == 0:
                first_wave[task["workflow"]] += 1
            close(event["accounted_charge"], 0, "dispatch settled charge")
        elif kind == "finish":
            require(task_id in running and running[task_id][1] == now, "invalid finish")
            finished[task_id] = running.pop(task_id)
            increment = shares[task_id] / (task["priority"] + 1)
            settled[task["workflow"]] += increment
            close(event["accounted_charge"], increment, "finish declared charge")
        else:
            raise ValueError("unknown event kind")
        reservations = event["reservations"]
        require(len(reservations) == len(running) and {row["task_id"] for row in reservations} == set(running), "reservation membership")
        total, reserve_charges = Counter(), defaultdict(float)
        for row in reservations:
            item = tasks[row["task_id"]]
            require(row["workflow_id"] == item["workflow"] and row["priority"] == item["priority"] and vector(row["resources"]) == vector(item["resources"]), "reservation identity")
            total.update(vector(row["resources"]))
            reserve_charges[item["workflow"]] += shares[item["id"]] / (item["priority"] + 1)
        allocated = vector(event["allocated"])
        require(all(allocated.get(key, 0) == total[key] and total[key] <= capacity.get(key, 0) for key in set(allocated) | set(total)), "resource bound")
        require({row["workflow"] for row in event["charges"]} == {task["workflow"] for task in tasks.values()}, "charge workflow coverage")
        for row in event["charges"]:
            close(row["settled"], settled[row["workflow"]], "settled ledger")
            close(row["reserved"], reserve_charges[row["workflow"]], "reserved ledger")
    require(not pending and not running and len(finished) == len(tasks), "missing terminal task")
    require(trial["invariants_passed"] and len(trial["events"]) == 2 * len(tasks), "trace coverage")
    waits = sorted(finished[key][0] - task["arrival_ms"] for key, task in tasks.items())
    measured = trial["metrics"]
    makespan = max(end for start, end in finished.values())
    require(measured["makespan_ms"] == makespan and measured["max_wait_ms"] == max(waits), "duration metrics")
    require(measured["p95_wait_ms"] == waits[math.ceil(len(waits) * .95) - 1], "p95 metric")
    close(measured["mean_wait_ms"], sum(waits) / len(waits), "mean wait")
    close(measured["logical_tasks_per_second"], len(tasks) * 1000 / makespan, "logical throughput")
    require(measured["first_wave_admissions"] == dict(first_wave), "first wave metric")
    require(dict(first_wave) == ({"a": 2, "b": 2} if variant == "C" else {"a": 4}), "initial reservation behavior")
    for row in measured["workflows"]:
        members = [task for task in tasks.values() if task["workflow"] == row["workflow"]]
        require(row["first_start_delay_ms"] == min(finished[item["id"]][0] for item in members) - members[0]["arrival_ms"], "first start delay")
        require(row["last_finish_ms"] == max(finished[item["id"]][1] for item in members), "workflow finish")
        close(row["settled_charge"], settled[row["workflow"]], "final workflow charge")
        close(row["declared_share_time_ms"], sum(shares[item["id"]] * item["service_ms"] for item in members), "resource-time diagnostic")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--run-directory", type=Path, required=True)
    parser.add_argument("--build-directory", type=Path, required=True)
    args = parser.parse_args()
    manifest = json.loads((args.build_directory / "build.json").read_text(encoding="utf-8-sig"))
    reports = {key: json.loads((args.run_directory / (key + ".json")).read_text(encoding="utf-8")) for key in "ABC"}
    require(len({report["input_sha256"] for report in reports.values()}) == 1, "input hash mismatch")
    for key, report in reports.items():
        role = "baseline" if key == "A" else "current"
        require(report["variant"] == key and report["build_role"] == role, "build role mismatch")
        require(report["source_sha256"] == manifest[role + "_source_sha256"] and report["adapter_sha256"] == manifest["adapter_sha256"], "report identity mismatch")
        require(len(report["trials"]) == 9, "fixed matrix coverage")
        require({(trial["workload"]["case"], trial["workload"]["seed"]) for trial in report["trials"]} == {
            (case, seed) for case in ("multi-slot", "dynamic-arrival", "mixed-duration") for seed in (104729, 104743, 104759)}, "fixed cases and seeds")
        input_bytes = json.dumps([trial["workload"] for trial in report["trials"]], ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        require(hashlib.sha256(input_bytes).hexdigest() == report["input_sha256"], "recomputed input hash")
        for case in ("multi-slot", "dynamic-arrival", "mixed-duration"):
            require(len({tuple(task["service_ms"] for task in trial["workload"]["tasks"]) for trial in report["trials"] if trial["workload"]["case"] == case}) == 3, "seed duration vectors unexpectedly duplicate")
        for trial in report["trials"]:
            verify_trial(trial, key)
    for left, right in zip(reports["A"]["trials"], reports["B"]["trials"]):
        require(left == right, "baseline and settled-only ablation differ in this controlled scope")
    for left, right in zip(reports["B"]["trials"], reports["C"]["trials"]):
        require(left["workload"] == right["workload"], "workload mismatch")
    summary = {"schema": "aegisrt.core-comparison-verification/v1", "passed": True,
               "trials": 27, "input_sha256": reports["A"]["input_sha256"], "baseline_equals_settled_only": True,
               "checks": ["fixed matrix", "source identity", "policy ordering", "every dispatch and finish", "resource conservation and capacity", "settled and reserved charges", "independent metric recomputation"],
               "reports": {key: hashlib.sha256((args.run_directory / (key + ".json")).read_bytes()).hexdigest() for key in "ABC"},
               "rows": [{"variant": key, "case": trial["workload"]["case"], "seed": trial["workload"]["seed"], **trial["metrics"]} for key, report in reports.items() for trial in report["trials"]]}
    output = args.run_directory / "verification.json"
    with output.open("x", encoding="utf-8", newline="\n") as handle:
        json.dump(summary, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
    print("Verified 27 trials: matching inputs, full traces, capacity, charges and metrics.")


if __name__ == "__main__":
    main()
