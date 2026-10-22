"""Summarize sanitized run archives offline; never extracts or starts processes.

Supply archives oldest first. A newer copy of the same member replaces its
earlier snapshot. Read only budgets, usage metadata, summaries and task states;
do not read credentials, Agent logs, report content or raw model responses.
"""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import tarfile


ROLES = ("query", "media", "insight", "forum", "report")
MAX_JSON_BYTES = 4 * 1024 * 1024


def accepted_member(name):
    parts = PurePosixPath(name).parts
    if not parts or PurePosixPath(name).is_absolute() or ".." in parts:
        return False
    if len(parts) == 3 and parts[0] == "runs":
        return parts[-1] in {"summary.json", "task-snapshot.json"}
    if len(parts) == 4 and parts[0] == "runs":
        return parts[-1] == "model-budget.json"
    return len(parts) == 5 and parts[0] == "runs" and parts[-2] == "model-usage" and parts[-1].endswith(".json")


def load_archives(paths):
    documents, sources, replacements = {}, [], []
    for archive_index, path in enumerate(paths):
        digest = hashlib.sha256()
        with path.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                digest.update(chunk)
        source = {"path": str(path.resolve()), "sha256": digest.hexdigest(), "accepted_members": 0}
        with tarfile.open(path, mode="r:*") as archive:
            seen = set()
            for member in archive:
                name = PurePosixPath(member.name).as_posix()
                if not member.isfile() or not accepted_member(name):
                    continue
                if name in seen:
                    raise ValueError("Duplicate accepted member within one archive: " + name)
                seen.add(name)
                if member.size > MAX_JSON_BYTES:
                    raise ValueError("Metadata member exceeds size limit: " + name)
                with archive.extractfile(member) as handle:
                    raw = handle.read(MAX_JSON_BYTES + 1)
                if len(raw) > MAX_JSON_BYTES:
                    raise ValueError("Metadata member exceeds read limit: " + name)
                document = json.loads(raw.decode("utf-8-sig"))
                if not isinstance(document, dict):
                    raise ValueError("Metadata member is not a JSON object: " + name)
                if name in documents:
                    replacements.append({"member": name, "old_archive_index": documents[name]["archive_index"],
                                         "new_archive_index": archive_index})
                documents[name] = {"document": document, "sha256": hashlib.sha256(raw).hexdigest(),
                                   "archive_index": archive_index}
                source["accepted_members"] += 1
        sources.append(source)
    return documents, sources, replacements


def role_for_task(task_id):
    return next((role for role in ROLES if task_id.endswith("-" + role)), task_id)


def numeric(value):
    return type(value) in (int, float)


def summarize(paths):
    documents, sources, replacements = load_archives(paths)
    grouped = {}
    for name, record in documents.items():
        workflow = PurePosixPath(name).parts[1]
        grouped.setdefault(workflow, {})[name] = record
    result = {"version": "aegisrt.bettafish.archive-statistics/v1", "offline_only": True,
              "sources": sources, "member_replacements": replacements, "runs": []}
    global_call_ids = {}
    for workflow, records in grouped.items():
        summary_record = records.get(f"runs/{workflow}/summary.json", {})
        summary = summary_record.get("document", {})
        snapshot = records.get(f"runs/{workflow}/task-snapshot.json", {}).get("document", {})
        runtime_statuses = {role_for_task(row["task"]["id"]): row["status"] for row in snapshot.get("tasks", [])}
        budgets, usage = {}, {}
        for name, record in records.items():
            parts, document = PurePosixPath(name).parts, record["document"]
            role = role_for_task(parts[2]) if len(parts) >= 4 else None
            if name.endswith("/model-budget.json"):
                attempts = document.get("attempts")
                if type(attempts) is not int or attempts < 0:
                    raise ValueError("Invalid attempt counter: " + name)
                budgets[role] = {"attempts": attempts, "limit": document.get("limit"),
                                 "failed_attempts": document.get("failed_attempts"), "metadata_sha256": record["sha256"]}
            elif "/model-usage/" in name:
                call_id = document.get("call_id")
                if not isinstance(call_id, str) or not call_id:
                    raise ValueError("Usage metadata has no call ID: " + name)
                if call_id in usage or call_id in global_call_ids:
                    raise ValueError("One provider call ID occurs in multiple members: " + call_id)
                if document.get("run_id") != workflow:
                    raise ValueError("Usage metadata belongs to a different workflow: " + name)
                usage[call_id] = {"agent": document.get("agent"), "status": document.get("status"),
                                  "usage_present": document.get("usage_present") is True,
                                  "total_tokens": document.get("total_tokens"), "metadata_sha256": record["sha256"],
                                  "error_type": document.get("error_type"), "elapsed_seconds": document.get("elapsed_seconds")}
                global_call_ids[call_id] = workflow
        rows = list(usage.values())
        tokens = [row["total_tokens"] for row in rows if row["usage_present"] and numeric(row["total_tokens"])]
        attempts = sum(item["attempts"] for item in budgets.values())
        warnings = []
        if not summary:
            warnings.append("Archive has no final coordinator summary; this is an in-progress snapshot.")
        if any(row.get("status") not in {"SUCCEEDED", "FAILED", "CANCELED"} for row in snapshot.get("tasks", [])):
            warnings.append("Runtime snapshot contains nonterminal tasks.")
        if attempts != len(rows):
            warnings.append("Budget attempts and persisted provider-call records differ; do not infer exact HTTP calls from either alone.")
        if any(row["status"] == "started" for row in rows):
            warnings.append("Some provider records are still started; cancellation or partial archival may omit final usage.")
        result["runs"].append({
            "workflow_id": workflow, "mode": summary.get("mode", "live" if "-live-" in workflow else "unknown"),
            "coordinator_status": summary.get("status", "snapshot_only"), "error_type": summary.get("error_type"),
            "runtime_statuses": runtime_statuses, "per_role_budgets": budgets, "budget_attempts": attempts,
            "logged_provider_calls": len(rows), "call_ids": sorted(usage), "calls": usage,
            "calls_with_numeric_total_tokens": len(tokens), "calls_missing_total_token_usage": len(rows) - len(tokens),
            "reported_total_tokens_sum": sum(tokens) if tokens else None,
            "known_provider_errors": sum(row["status"] == "error" for row in rows),
            "unfinished_provider_records": sum(row["status"] == "started" for row in rows),
            "warnings": warnings,
        })
    result["runs"].sort(key=lambda run: (run["mode"] != "live", int(match.group(1)) if (match := re.search(r"-v(\d+)$", run["workflow_id"])) else 1))
    live_runs = [run for run in result["runs"] if run["mode"] == "live"]
    token_runs = [run for run in live_runs if run["reported_total_tokens_sum"] is not None]
    result["live_totals"] = {
        "budget_attempts": sum(run["budget_attempts"] for run in live_runs),
        "logged_provider_calls": sum(run["logged_provider_calls"] for run in live_runs),
        "reported_total_tokens_sum": sum(run["reported_total_tokens_sum"] for run in token_runs) if token_runs else None,
        "calls_missing_total_token_usage": sum(run["calls_missing_total_token_usage"] for run in live_runs),
        "initial_request_cap": 22,
        "attempts_over_initial_cap": max(0, sum(run["budget_attempts"] for run in live_runs) - 22),
        "token_sum_is_complete": all(run["budget_attempts"] == run["calls_with_numeric_total_tokens"] for run in live_runs),
        "billing_cost_calculated": False,
    }
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, action="append", required=True, help="Sanitized archives, oldest first")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    result = summarize(args.archive)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    for run in result["runs"]:
        print(json.dumps({key: run[key] for key in ("workflow_id", "coordinator_status", "budget_attempts", "logged_provider_calls", "reported_total_tokens_sum", "calls_missing_total_token_usage", "warnings")}, ensure_ascii=False))
    print(json.dumps({"live_totals": result["live_totals"], "output": str(args.output.resolve())}, ensure_ascii=False))


if __name__ == "__main__":
    main()
