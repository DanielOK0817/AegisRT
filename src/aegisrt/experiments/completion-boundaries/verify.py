"""Independent assertions over actual statefile envelopes, journals and inputs."""
from __future__ import annotations

import argparse
import base64
import collections
import csv
import hashlib
import json
from pathlib import Path

FROZEN_BODY = "Frozen research result. Evidence alpha 17, evidence beta 29. 中文正文保持一致。"


def load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def state(path: Path) -> dict:
    envelope = load(path)
    assert envelope["format"] == "aegisrt-state" and envelope["version"] == 1, path
    payload = base64.b64decode(envelope["payload"], validate=True)
    assert hashlib.sha256(payload).hexdigest() == envelope["checksum"], path
    return json.loads(payload)


def tasks(runtime: dict) -> dict:
    return {item["spec"]["id"]: item["status"] for item in runtime["graph"]["tasks"]}


def counters(runtime: dict) -> dict:
    return {item["task_id"]: item["number"] for item in runtime.get("attempts", [])}


def output_body(context: dict, task_id: str) -> str:
    outputs = [item for item in context.get("outputs", []) if item["task_id"] == task_id]
    assert len(outputs) == 1, (task_id, outputs)
    artifacts = outputs[0]["artifacts"]
    assert len(artifacts) == 1 and artifacts[0]["name"] == "result", artifacts
    page = next(item for item in context["store"]["pages"] if item["id"] == artifacts[0]["page_id"])
    blob = next(item for item in context["store"]["blobs"] if item["content_hash"] == page["content_hash"])
    assert hashlib.sha256(blob["content"].encode()).hexdigest() == blob["content_hash"], blob
    return blob["content"]


def verify_trial(trial: Path) -> dict:
    controller = load(trial / "controller.json")
    point = controller["point"]
    events = [json.loads(line) for line in (trial / "events.jsonl").read_text(encoding="utf-8").splitlines()]
    before_r, before_c = state(trial / "before-runtime.state"), state(trial / "before-context.state")
    after_r, after_c = state(trial / "after-runtime.state"), state(trial / "after-context.state")
    final_r, final_c = state(trial / "reopened-runtime.state"), state(trial / "reopened-context.state")
    expected = {"running": ("RUNNING", "WAITING_DEPENDENCY", 0, 0), "prepared": ("RUNNING", "WAITING_DEPENDENCY", 1, 0),
                "published": ("RUNNING", "WAITING_DEPENDENCY", 1, 1), "succeeded": ("SUCCEEDED", "READY", 0, 1),
                "none": ("SUCCEEDED", "SUCCEEDED", 0, 2)}[point]
    assert tasks(before_r) == {"produce": expected[0], "consume": expected[1]}, (trial, tasks(before_r))
    assert len(before_r.get("pending_completions", [])) == expected[2], trial
    assert len(before_c.get("outputs", [])) == expected[3], trial
    if point in ("prepared", "published"):
        pending = before_r["pending_completions"][0]
        assert pending["task_id"] == "produce" and pending["attempt_number"] == 1
        assert pending["artifacts"][0]["content"] == FROZEN_BODY
    if point != "none":
        assert controller["actions"][0]["hard_kill_requested"] and controller["actions"][0]["returncode"] != 0
        marker = load(trial / "boundary.json")
        assert marker["point"] == point and marker["pid"] == controller["actions"][0]["pid"]
    want_producer = 2 if point == "running" else 1
    for runtime in (after_r, final_r):
        assert tasks(runtime) == {"produce": "SUCCEEDED", "consume": "SUCCEEDED"}, trial
        assert counters(runtime) == {"produce": want_producer, "consume": 1}, trial
        assert not runtime.get("pending_completions") and not runtime["scheduler"]["reservations"], trial
        assert all(value == 0 for value in runtime["scheduler"]["allocated"].values() if isinstance(value, (int, float))), trial
    assert after_r == final_r and after_c == final_c, (trial, "reopen mutated settled state")
    assert before_r["instance_id"] == after_r["instance_id"] == after_c["runtime_instance_id"], trial
    assert len(after_c.get("outputs", [])) == 2, trial
    producer_output = next(item for item in after_c["outputs"] if item["task_id"] == "produce")
    assert producer_output["attempt_number"] == want_producer
    assert producer_output["artifacts"][0]["targets"] == ["consumer", "producer"]
    producer_body = output_body(after_c, "produce")
    consumed = (trial / "consumer-input.txt").read_text(encoding="utf-8")
    assert producer_body == FROZEN_BODY == consumed == output_body(after_c, "consume"), trial
    starts = collections.Counter(item["task_id"] for item in events if item["kind"] == "agent_started")
    assert starts == {"produce": want_producer, "consume": 1}, (trial, starts)
    if point != "none":
        initial_pid = controller["actions"][0]["pid"]
        assert not any(item["kind"] == "agent_started" and item["role"] == "consumer" and item["at"] < load(trial / "boundary.json")["at"] for item in events)
        assert initial_pid != controller["actions"][1]["pid"], trial
    requests = sum(item["kind"] == "publication_requested" and item["task_id"] == "produce" for item in events)
    assert requests == (2 if point == "published" else 1), (trial, requests)
    returned = [item for item in events if item["kind"] == "publication_returned" and item["task_id"] == "produce"]
    assert sum(not item["existing_before"] for item in returned) == 1, trial
    assert len({item["output"]["fingerprint"] for item in returned}) == 1, trial
    # Journals originate after production Store.Save returns. Inspect their
    # content to count durable insertion transitions, independent of callbacks.
    journals = sorted((trial / "journal").glob("context-*.json"), key=lambda path: load(path)["generation"])
    output_ids = [{item["task_id"] for item in load(path).get("outputs", [])} for path in journals]
    insertions = sum("produce" in current and "produce" not in previous for previous, current in zip([set()] + output_ids, output_ids))
    assert insertions == 1, (trial, insertions)
    opened = [item for item in events if item["kind"] == "runtime_opened" and item["phase"] == "recover"]
    assert len(opened) == 1 and opened[0]["report"]["loaded"], trial
    report = opened[0]["report"]
    assert report.get("requeued_task_ids", []) == (["produce"] if point == "running" else []), report
    cleanup = [item for item in events if item["kind"] == "recovery_cleanup"]
    assert len(cleanup) == (1 if point == "running" else 0), (trial, cleanup)
    if cleanup:
        assert cleanup[0]["attempt"] == 1 and cleanup[0]["kill_sent"], trial
    return {"trial": trial.name, "point": point, "producer_executions": starts["produce"],
            "consumer_executions": starts["consume"], "producer_publication_requests": requests,
            "durable_producer_insertions": insertions, "producer_body_sha256": hashlib.sha256(producer_body.encode()).hexdigest(),
            "recover_elapsed_ns": controller["actions"][1]["elapsed_ns"]}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    output = args.output.resolve()
    environment = load(output / "environment.json")
    rows = [verify_trial(trial.parent) for trial in sorted(output.glob("*/controller.json"))]
    assert len(rows) == environment["repeat"] * len(environment["points"]), "missing trials"
    summary = {"verification": "passed", "trial_count": len(rows), "points": environment["points"],
               "repetitions_per_point": environment["repeat"], "new_model_requests": 0,
               "assertions": ["OS hard kill at observed durable boundary", "statefile checksum integrity",
                   "saved state matches fault location", "correct retry or saved-output replay",
                   "one durable producer output", "one downstream execution and exact body",
                   "zero final reservations", "settled second reopen changes nothing"], "trials": rows}
    (output / "verification.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    with (output / "verified-metrics.csv").open("w", newline="", encoding="utf-8") as stream:
        writer = csv.DictWriter(stream, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)
    print(json.dumps({"verification": "passed", "trial_count": len(rows)}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
