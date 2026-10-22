"""Verify a fresh real round or retain failure evidence, with no model calls."""
import argparse
from datetime import datetime
import html
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tarfile
from workflow_common import (STAGES, inside, module, plan, profile_spent, read, require,
    revision, sha, usage, utc_now, validate_launch_configuration, write)


def artifacts(folder):
    return [path for path in sorted(folder.rglob("*")) if path.is_file()
        and not any(part.startswith(".control") for part in path.relative_to(folder).parts)
        and path.suffix in (".json", ".md", ".html", ".txt")
        and path.name not in ("agent.log", "coordinator.log")]


def export(stage, folder, launch, verification, adapter):
    files = artifacts(folder)
    # Private credential material is read only for exact-value scanning, never exported.
    adapter.load_environment(Path("/app/.env"))
    key = re.compile(r"API_?KEY|PASSWORD|SECRET|(?:^|_)(?:KEY|TOKEN)(?:$|_)", re.I)
    secrets = [value for name, value in os.environ.items() if key.search(name) and len(value) >= 3]
    token = folder / ".control/auth.token"
    if token.is_file():
        secrets.append(token.read_text().strip())
    require(not any(secret.encode() in path.read_bytes() for path in files for secret in secrets if secret),
            "Credential scan rejected evidence export")
    destination = inside(stage, stage / "verified-evidence" / launch["workflow_id"])
    require(not destination.exists(), "Evidence export already exists; preserve the prior export")
    destination.mkdir(parents=True)
    exported = []
    for source in files:
        relative = source.relative_to(folder)
        target = destination / "run" / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
        require(sha(target) == sha(source), "Export changed original bytes")
        exported.append({"path": "run/" + relative.as_posix(), "sha256": sha(target)})
    write(destination / "launch.json", launch)
    write(destination / "verification.json", verification)
    write(destination / "export-manifest.json", {"files": exported, "original_file_bytes_preserved": True,
                                                "model_calls_by_verifier": 0, "credential_scan_hits": 0})
    archive = destination.with_suffix(".tar.gz")
    with tarfile.open(archive, "w:gz") as target:
        for path in sorted(destination.rglob("*")):
            if path.is_file():
                target.add(path, arcname=path.relative_to(destination).as_posix())
    return {"archive": str(archive), "archive_sha256": sha(archive), "exported_files": len(exported),
            "credential_scan_hits": 0}


def rebuild_report_evidence(reporting, states, reports, comments, forum, max_content_length):
    schema = getattr(reporting, "REVIEW_INPUT_SCHEMA", None)
    if schema in ("bettafish.evidence-review-input/v2", "bettafish.evidence-review-input/v3"):
        budget = getattr(reporting, "model_evidence_budget", None)
        require(callable(budget), "Frozen v2/v3 review module requires model_evidence_budget")
        max_content_length = budget(max_content_length)
    else:
        require(schema == "bettafish.evidence-review-input/v1", "Frozen review input schema is unsupported")
    return reporting.build_evidence(states, reports, comments, forum, max_content_length)


def rebuild_report_review_input(adapter, reporting, candidate, bundle, launch, correction, sample_scope):
    """Replay the frozen adapter's complete envelope and bounded repair hints."""
    envelope = {"task": "paragraph-evidence-review", "topic": launch["topic"]}
    limit = launch["max_content_length"]
    bounded = getattr(adapter, "bounded_review_feedback", None)
    if hasattr(adapter, "bounded_review_feedback"):
        require(callable(bounded), "Frozen bounded review feedback helper is not callable")
        body_limit = limit - len(adapter.canonical(envelope)) + 1 - 500
    else:
        # Earlier frozen adapters used the full unbounded correction object and
        # reserved 500 characters before adding their task/topic envelope.
        body_limit = limit - 500
    options = {"sample_scope": sample_scope} if reporting.REVIEW_INPUT_SCHEMA == "bettafish.evidence-review-input/v3" else {}
    supplied = {**envelope, **reporting.build_review_input(candidate, bundle, body_limit, **options)}
    if correction is not None:
        feedback_limit = limit - len(adapter.canonical(supplied)) - len(',"format_correction":')
        supplied["format_correction"] = bounded(correction, feedback_limit) if bounded else correction
    require(len(adapter.canonical(supplied)) <= limit,
            "Independently rebuilt complete review envelope exceeds its frozen content budget")
    return supplied


def report_provider_outcome(attempt, call, budget):
    truncated = (attempt.get("status") == "provider_error" and
                 attempt.get("error_type") == "ModelOutputLimitExceeded")
    if truncated:
        rows = [row for row in budget.get("provider_attempts", [])
                if row.get("attempt") == attempt["attempt"]]
        require(len(rows) == 1 and rows[0].get("usage_call_id") == call.get("call_id") and
                rows[0].get("agent") == "report" and rows[0].get("status") == "error" and
                rows[0].get("failure_stage") == "response" and
                rows[0].get("error_type") == "ModelOutputLimitExceeded" and
                rows[0].get("retryable") is True and attempt.get("retryable") is True,
                "Truncated report retry lacks a matching rejected physical response")
    expected = "error" if attempt.get("status") == "provider_error" and not truncated else "completed"
    require(call.get("status") == expected, "Report request phase does not match its provider outcome")
    return truncated


def verify_report_review(report, manifest, audit, candidate, bundle, launch, adapter, reporting, sample_scope=None):
    """Bind each automatic generation/review attempt to its own real usage record."""
    attempts = read(report / "report-validation-attempts.json")
    budget = read(report / "model-budget.json")
    require(2 <= len(attempts) == budget["attempts"] <= budget["limit"] == 5 and
            [row["attempt"] for row in attempts] == list(range(1, len(attempts) + 1)) and
            attempts[-1]["status"] == "passed" and attempts[-1]["phase"] == "evidence_review",
            "Report requires bounded automatic generation followed by evidence review")
    require(audit["validation_attempts"] == attempts and
            audit["validation"].get("model_evidence_review_performed") is True and
            audit["validation"].get("model_fact_check_performed") is False,
            "Report audit does not distinguish supplied-evidence review from fact verification")
    calls = sorted((read(path) for path in (report / "model-usage").glob("*.json")),
                   key=lambda row: (row["started_at"], row["call_id"]))
    require(len(calls) == len(attempts) and len({row["call_id"] for row in calls}) == len(calls),
            "Each report generation and review request requires its own usage record")
    current_candidate, generation_response, completed_review = None, None, None
    review_correction = None
    scoped_base, scoped_issues = None, None
    if getattr(reporting, "CORRECTION_SCHEMA", None) == "bettafish.scoped-report-correction/v1":
        require(audit.get("generation_policy") == reporting.CORRECTION_SCHEMA and
                audit.get("automatic_corrections") == [adapter.file_record(path) for path in sorted(report.glob("report-correction-*.json"))],
                "Scoped correction audit must bind all actual correction receipts")
    final_input = None
    for position, (attempt, call) in enumerate(zip(attempts, calls)):
        index, phase, status = attempt["attempt"], attempt["phase"], attempt["status"]
        require(call["agent"] == "report" and call["run_id"] == launch["workflow_id"] and
                call.get("status") in ("completed", "error") and "ended_at" in call,
                "Report usage identity or terminal status changed")
        truncated = report_provider_outcome(attempt, call, budget)
        if position:
            require(datetime.fromisoformat(calls[position - 1]["ended_at"]) <=
                    datetime.fromisoformat(call["started_at"]), "Report requests were not sequential")
        require(phase == ("evidence_review" if current_candidate is not None else "generation"),
                "Report skipped generation or reviewed a stale candidate")
        if phase == "evidence_review":
            final_input = report / f"report-review-input-{index}.json"
            supplied = read(final_input)
            expected = rebuild_report_review_input(adapter, reporting, current_candidate, bundle,
                                                   launch, review_correction, sample_scope)
            require({key: value for key, value in supplied.items() if key != "format_correction"} ==
                    {key: value for key, value in expected.items() if key != "format_correction"} and
                    set(supplied) <= set(expected) | {"format_correction"} and
                    len(adapter.canonical(supplied)) <= launch["max_content_length"],
                    "Automatic review did not receive the exact supplied cited excerpts for every paragraph")
            require(("format_correction" in supplied) == ("format_correction" in expected) and
                    supplied.get("format_correction") == expected.get("format_correction"),
                    "Review format correction differs from the independently rebuilt failed-review feedback")
        if status == "provider_error":
            if truncated:
                require(not (report / f"model-report-review-response-{index}.txt").exists() and
                        not (report / f"model-report-response-{index}.txt").exists(),
                        "Truncated response must never be retained as an accepted report response")
                if phase == "evidence_review":
                    review_correction = {
                        "validation_error": "上次审核输出达到长度上限，截断结果未被采纳。",
                        "instruction": "重新完整审核同一候选。每段和每句都须覆盖，保留全部编号与判定字段；reason用简短的一句话说明，不复述原句或证据全文，不输出额外分析。不能省略检查或将未检查项判为通过。"}
            continue
        if phase == "generation":
            require(status in ("candidate_validated", "rejected"), "Unexpected generation outcome")
            response = (report / f"model-report-response-{index}.txt").read_text(encoding="utf-8")
            resolution_path = report / f"report-correction-{index}.json"
            if scoped_base is not None:
                generation_input = read(report / f"report-generation-input-{index}.json")
                require(generation_input.get("correction", {}).get("editable_paths") ==
                        sorted({row["path"] for row in scoped_issues}) and
                        len(adapter.canonical(generation_input)) <= launch["max_content_length"],
                        "Scoped correction input does not match actual rejected paragraph paths")
            try:
                decoded = adapter.decode_model_json(response)
                resolution = None
                if scoped_base is not None:
                    decoded, resolution = reporting.apply_scoped_correction(scoped_base, decoded, scoped_issues)
            except (ValueError, adapter.AdapterError):
                require(status == "rejected" and not resolution_path.exists(),
                        "Malformed generation cannot have an accepted correction receipt")
                continue
            if resolution is not None:
                require(resolution_path.is_file() and read(resolution_path) == resolution,
                        "Scoped correction receipt differs from independently replayed raw responses")
            else:
                require(not resolution_path.exists(), "Unscoped generation has an unexpected correction receipt")
            if status == "candidate_validated":
                current_candidate = reporting.validate_report(decoded, bundle)
                generation_response = response
                review_correction = None
            else:
                try:
                    reporting.validate_report(decoded, bundle)
                except (ValueError, adapter.AdapterError):
                    pass
                else:
                    raise ValueError("Rejected generation has no independently reproducible validation error")
        else:
            require(status in ("passed", "evidence_rejected", "review_rejected"), "Unexpected evidence review outcome")
            response = (report / f"model-report-review-response-{index}.txt").read_text(encoding="utf-8")
            try:
                reviewed, issues = reporting.validate_evidence_review(adapter.decode_model_json(response), current_candidate, bundle)
            except (ValueError, adapter.AdapterError) as exc:
                require(status == "review_rejected", "A passing review response is malformed or incomplete")
                issues = getattr(exc, "issues", [{"code": "invalid_review_json", "path": "$", "message": str(exc)}])
                require(attempt.get("reason") == str(exc) and attempt.get("issues") == issues,
                        "Full rejected review reason and issues differ from the independently replayed raw response")
                review_correction = {"validation_error": str(exc), "issues": issues,
                    "instruction": "重新核对同一候选的所有给定段落，完整返回checks，仅使用每段真实引用的ID，supported须为布尔值。"}
                continue
            require(status != "review_rejected" and
                    read(report / f"report-evidence-review-{index}.json") == reviewed,
                    "Saved evidence review differs from its actual model response")
            if status == "evidence_rejected":
                require(bool(issues) and issues == attempt["issues"], "Unsupported paragraphs were not bound to correction issues")
                if getattr(reporting, "CORRECTION_SCHEMA", None) == "bettafish.scoped-report-correction/v1":
                    scoped_base, scoped_issues = current_candidate, issues
                current_candidate = None
                review_correction = None
            else:
                require(not issues and all(row["supported"] is True for row in reviewed["checks"]),
                        "Automatic final review contains unsupported paragraphs")
                completed_review = reviewed
    require(current_candidate == candidate and completed_review is not None and
            (report / "model-report-response.txt").read_text(encoding="utf-8") == generation_response,
            "Final report does not equal the automatically generated and reviewed candidate")
    record = read(adapter.checked_record(manifest["files"]["evidence_review"], report))
    input_record = manifest["files"]["review_input"]
    require(audit["evidence_review"] == manifest["files"]["evidence_review"] and
            record["review_input"] == input_record and adapter.checked_record(input_record, report) == final_input.resolve(),
            "Final review input or audit hash binding changed")
    candidate_hash = hashlib.sha256(adapter.canonical(candidate).encode("utf-8")).hexdigest()
    require(record["candidate_sha256"] == candidate_hash and record["review"] == completed_review and
            record["issues"] == [] and record["all_paragraphs_supported"] is True and
            record["scope"] == "support within supplied cited excerpts; not independent world fact verification",
            "Final review result is not bound to this candidate and its explicit evidence scope")
    reviewed, issues = reporting.validate_evidence_review(record["review"], candidate, bundle)
    require(not issues and reviewed == completed_review, "Final report paragraph review failed independent revalidation")
    return {"requests_verified": len(calls), "generation_requests": sum(row["phase"] == "generation" for row in attempts),
            "review_requests": sum(row["phase"] == "evidence_review" for row in attempts),
            "generation_json_rejections": sum(row["status"] == "rejected" for row in attempts),
            "review_json_rejections": sum(row["status"] == "review_rejected" for row in attempts),
            "unsupported_corrections": sum(row["status"] == "evidence_rejected" for row in attempts),
            "provider_errors": sum(row["status"] == "provider_error" for row in attempts),
            "paragraphs_supported": len(reviewed["checks"]), "candidate_sha256": candidate_hash,
            "review_input_sha256": input_record["sha256"], "review_sha256": manifest["files"]["evidence_review"]["sha256"],
            "independent_world_fact_verification": False}


PROVIDER_RETRY_V2 = "bettafish.provider-retry/v2"
PROVIDER_RETRY_V3 = "bettafish.provider-retry/v3"
PROVIDER_CANCELLATION_TYPES = {"BaseException", "KeyboardInterrupt", "SystemExit", "GeneratorExit", "CancelledError"}


def _provider_exception_metadata(row):
    """Validate recorded types and scope, without claiming to reconstruct objects."""
    keys = ("exception_chain_types", "exception_chain_truncated", "deadline_origin", "cancellation_in_chain")
    require(all(key in row for key in keys), "Provider exception-chain metadata is missing")
    chain = row["exception_chain_types"]
    require(isinstance(chain, list) and 1 <= len(chain) <= 32 and
            all(isinstance(name, str) and name.isidentifier() for name in chain) and
            (row.get("status") != "error" or chain[0] == row.get("error_type")) and
            type(row["exception_chain_truncated"]) is bool and
            type(row["cancellation_in_chain"]) is bool and
            row["deadline_origin"] in (None, "request", "enclosing"),
            "Provider exception-chain metadata is not bounded, typed or bound to its top error")
    enclosing = "ModelDeadlineExceeded" in chain
    request = "ModelRequestDeadlineExceeded" in chain
    expected_origin = "enclosing" if enclosing else "request" if request else None
    require(row["deadline_origin"] == expected_origin and
            (not PROVIDER_CANCELLATION_TYPES.intersection(chain) or row["cancellation_in_chain"]),
            "Provider exception-chain metadata hides a deadline origin or cancellation")
    return (not enclosing and not row["cancellation_in_chain"] and not row["exception_chain_truncated"])


def _provider_timestamp(value):
    require(isinstance(value, str) and value, "Physical provider chronology timestamp is missing")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        raise ValueError("Physical provider chronology timestamp is invalid") from None
    require(parsed.utcoffset() is not None, "Physical provider chronology timestamp lacks its timezone")
    return parsed


def _provider_usage_times(row, call, *, request_role, safe_chain):
    """Use a local close only for an unopened, own-deadline SDK stream."""
    started = _provider_timestamp(call.get("started_at"))
    local_close = call.get("status") == "started"
    if local_close:
        require(request_role and row["status"] == "error" and row.get("failure_stage") == "request" and
                row.get("stream") is True and call.get("stream") is True and row.get("delivered_chunks") == 0 and
                row.get("deadline_origin") == "request" and safe_chain is True and row.get("retryable") is True and
                row.get("stream_closed") is True and "stream_closed_at" in row and
                "close_error_type" not in row and "close_error" not in row and "ended_at" not in call and
                call.get("usage_present") is False and
                all(call.get(key) is None for key in ("input_tokens", "output_tokens", "total_tokens")),
                "SDK started stream lacks exact own-request deadline and successful adapter local close evidence")
        ended = _provider_timestamp(row["stream_closed_at"])
    else:
        ended = _provider_timestamp(call.get("ended_at"))
    require(started <= ended, "Physical provider chronology ended before it started")
    return started, ended, local_close


def _research_provider_trace(task, role, state, launch, adapter):
    """Bind every recorded provider attempt; legacy node archives keep their rules."""
    declared_policy = getattr(adapter, "PROVIDER_RETRY_SCHEMA", None)
    require(declared_policy in (None, PROVIDER_RETRY_V2, PROVIDER_RETRY_V3), "Frozen provider retry policy schema is unsupported")
    v2 = declared_policy in (PROVIDER_RETRY_V2, PROVIDER_RETRY_V3)
    v3 = declared_policy == PROVIDER_RETRY_V3
    budget_path = task / "model-budget.json"
    if not budget_path.is_file():
        require(not v2, "New research source is missing its provider trace budget")
        return None, {}
    budget = read(budget_path)
    require(budget.get("retry_policy_schema") == declared_policy,
            "Provider retry policy schema differs from its frozen adapter")
    if "provider_attempts" not in budget and "provider_retries" not in budget:
        require(not v2, "New research source is missing its provider trace arrays")
        return None, {}
    attempts, retries = budget.get("provider_attempts"), budget.get("provider_retries")
    require(isinstance(attempts, list) and isinstance(retries, list) and attempts and
            budget.get("request_hash_scope") == "recorded_completion_kwargs" and
            budget.get("retry_transient") is True, "Research provider trace or explicit retry policy is missing")
    require(type(budget.get("attempts")) is int and len(attempts) == budget["attempts"] <= budget["limit"] and
            [row.get("attempt") for row in attempts] == list(range(1, len(attempts) + 1)),
            "Provider physical attempt count, ordinals or budget changed")
    if v3:
        require(not budget.get("blocked_requests"), "Successful research contains a blocked required request")
    if v2:
        require(type(budget.get("request_timeout_seconds")) in (int, float) and
                budget["request_timeout_seconds"] == 120,
                "Provider v2 120/119-second timeout policy changed")
    usages = {}
    for path in (task / "model-usage").glob("*.json"):
        row = read(path)
        call_id = row.get("call_id")
        require(isinstance(call_id, str) and call_id and call_id == path.stem and call_id not in usages,
                "Provider usage call IDs are missing, duplicated or do not match their files")
        usages[call_id] = row
    require(len(usages) == len(attempts), "Each physical provider attempt must retain its own usage record")
    audit = state["research_audit"]
    inputs = {}
    for key in ("structure_requests", "decision_requests", "summary_requests"):
        records = audit.get(key, [])
        require(isinstance(records, list), "Node input audit must be a list")
        for record in records:
            path = adapter.checked_record(record, task)
            require(path.parent == task / "model-inputs" and path not in inputs,
                    "Node input is duplicated or outside the owned input folder")
            inputs[path] = record
    require(set((task / "model-inputs").glob("*.json")) == set(inputs),
            "A saved node input is missing from the provider audit")
    require(len(audit.get("summary_requests", [])) == len(audit.get("summary_responses", [])),
            "Summary inputs and raw responses no longer pair after transparent retry")
    require(len(list((task / "model-requests").glob("request-*.json"))) == len(attempts),
            "Physical request archive contains missing or untracked files")
    bound, seen_ids, agents = {}, set(), {}
    previous_usage, safe_chains, local_close_evidence = None, {}, []
    for row in attempts:
        ordinal, agent = row["attempt"], row.get("agent")
        require(agent in (role, "keyword_optimizer"), "Unknown provider agent appeared in a research task")
        if v3:
            required_after = row.get("required_calls_after")
            require(type(required_after) is int and 0 <= required_after <= budget["limit"] - ordinal and
                    row.get("research_step") in ("structure", "initial-decision", "reflection-decision",
                        "initial-summary", "reflection-summary", "initial-search", "reflection-search"),
                    "Provider required research capacity metadata is missing, ill typed or was spent")
            if agent == "keyword_optimizer":
                require(row["research_step"] in ("initial-search", "reflection-search"),
                        "Provider reservation step differs from its optimizer node input")
        if v2 and row.get("status") in ("error", "cancelled"):
            safe_chains[ordinal] = _provider_exception_metadata(row)
        require(row.get("status") in ("completed", "error"),
                "A started or cancelled physical request cannot pass research acceptance")
        call_id = row.get("usage_call_id")
        require(call_id in usages and call_id not in seen_ids, "Physical attempt lacks a unique real usage mapping")
        seen_ids.add(call_id)
        call = usages[call_id]
        require(call.get("agent") == agent and call.get("run_id") == launch["workflow_id"],
                "Physical usage identity or completed chronology changed")
        if v2:
            started, ended, local_close = _provider_usage_times(row, call,
                request_role=agent == role and role in adapter.RESEARCH_ROLES, safe_chain=safe_chains.get(ordinal))
            if local_close:
                local_close_evidence.append({"attempt": ordinal, "usage_call_id": call_id,
                    "stream_closed_at": row["stream_closed_at"],
                    "scope": "adapter local stream close; not SDK completion"})
            if previous_usage is not None:
                require(previous_usage <= started, "Physical provider attempts overlap or changed order")
            previous_usage = ended
        else:
            require(call.get("started_at") and call.get("ended_at") and call["started_at"] <= call["ended_at"],
                    "Physical usage identity or completed chronology changed")
            if previous_usage is not None:
                require(previous_usage["ended_at"] <= call["started_at"], "Physical provider attempts overlap or changed order")
            previous_usage = call
        agents[agent] = agents.get(agent, 0) + 1
        request_path = adapter.checked_record(row.get("request_record", {}), task)
        require(request_path == task / "model-requests" / f"request-{ordinal:03d}.json",
                "Physical request ordinal or owned path changed")
        kwargs = read(request_path)
        text = adapter.canonical(kwargs)
        digest = hashlib.sha256(text.encode("utf-8")).hexdigest()
        require(isinstance(kwargs, dict) and request_path.read_text(encoding="utf-8") == text and
                row.get("request_sha256") == row["request_record"].get("sha256") == digest,
                "Physical request canonical content or recorded kwargs hash changed")
        require(type(row.get("stream")) is bool and row["stream"] == bool(kwargs.get("stream")) and
                call.get("stream") is row["stream"] and
                type(row.get("delivered_chunks")) is int and row["delivered_chunks"] >= 0 and
                (row["stream"] or row["delivered_chunks"] == 0), "Physical streaming or delivered-chunk audit changed")
        timeout, tokens = kwargs.get("timeout"), kwargs.get("max_tokens")
        require(type(timeout) in (int, float) and 0 < timeout <= budget["request_timeout_seconds"] and
                type(tokens) is int and 0 < tokens <= launch["max_output_tokens"] and
                "max_completion_tokens" not in kwargs, "Physical request bypassed timeout or output token caps")
        if v2:
            require(type(row.get("absolute_timeout_seconds")) in (int, float) and
                    row["absolute_timeout_seconds"] == 120 and timeout == (119 if agent == role else 120),
                    "Provider v2 120/119-second saved request or absolute guard changed")
        if row["status"] == "completed":
            require(call.get("status") == "completed", "Completed provider attempt lacks a completed SDK usage record")
        else:
            require(isinstance(row.get("error_type"), str) and row["error_type"] and
                    type(row.get("retryable")) is bool and row.get("failure_stage") in ("request", "stream", "response", "cleanup"),
                    "Provider error stage, type or classified retryability is missing")
            # A non-stop response or EOF can be budget-error while SDK usage is
            # completed. Keep these two statuses distinct; only retry links below
            # can make a transient error part of successful research.
            require(call.get("status") in ("error", "completed", "started"), "Provider error has an inconsistent SDK usage status")
            if call.get("status") == "error":
                require(call.get("error_type") == row["error_type"] and call.get("http_status") == row.get("http_status"),
                        "Provider error differs from its actual SDK usage audit")
        input_record = row.get("input_record")
        require((agent == role and input_record is not None) or
                (agent == "keyword_optimizer" and input_record is None),
                "Research node input cannot be omitted or assigned to a keyword optimizer")
        if input_record is not None:
            input_path = adapter.checked_record(input_record, task)
            require(input_path in inputs and input_record == inputs[input_path] and agent == role,
                    "Physical provider input does not match its actual node record")
            if v3:
                if input_path.name.startswith("structure-"):
                    expected_steps = ("structure",)
                elif input_path.name.startswith("decision-"):
                    expected_steps = ("initial-decision", "reflection-decision")
                else:
                    expected_steps = ("initial-summary",) if input_path.name.endswith("-initial.json") else ("reflection-summary",)
                require(row["research_step"] in expected_steps,
                        "Provider reservation step differs from its archived node input")
            supplied = read(input_path)
            require(set(supplied) == {"system", "user"} and all(isinstance(value, str) for value in supplied.values()),
                    "Actual node input must contain the system and user strings")
            messages = kwargs.get("messages")
            require(isinstance(messages, list) and len(messages) == 2 and
                    messages[0].get("role") == "system" and messages[0].get("content") == supplied["system"] and
                    messages[1].get("role") == "user", "Recorded provider kwargs changed the node system contract")
            user = messages[1].get("content")
            prefix = r"今天的实际时间是\d{4}年\d{2}月\d{2}日\d{2}时\d{2}分\n"
            require(isinstance(user, str) and (user == supplied["user"] or
                    re.fullmatch(prefix + re.escape(supplied["user"]), user)),
                    "Recorded provider kwargs changed the actual node user data")
            bound.setdefault(input_path, []).append(row)
    require(seen_ids == set(usages) and agents == budget.get("agents") and
            budget.get("failed_attempts") == sum(row["status"] == "error" for row in attempts),
            "Usage, agent counts or failed physical attempts were removed from the budget")
    failed_links, retry_targets = set(), set()
    for event in retries:
        failed, retry = event.get("failed_attempt"), event.get("retry_attempt")
        require(type(failed) is int and type(retry) is int and 1 <= failed < retry <= len(attempts) and
                retry == failed + 1 and failed not in failed_links and retry not in retry_targets,
                "Provider retry links are duplicated, out of order or do not reference real attempts")
        first, second = attempts[failed - 1], attempts[retry - 1]
        status = first.get("http_status")
        transient = (status in (408, 409, 429) or 500 <= status <= 599) if type(status) is int else (
                     first.get("error_type") in ("TimeoutError", "ConnectionError", "APITimeoutError", "APIConnectionError"))
        if v2 and type(status) is not int and first.get("error_type") == "ModelRequestDeadlineExceeded":
            transient = first["deadline_origin"] == "request"
        if v2:
            transient = transient and safe_chains.get(failed) is True
        require(first["status"] == "error" and first.get("retryable") is True and transient and
                first.get("error_type") != "ModelDeadlineExceeded" and
                first.get("failure_stage") in ("request", "stream") and first["delivered_chunks"] == 0 and
                (first.get("failure_stage") != "stream" or first["stream"]),
                "Provider retry was not an allowed transient failure before any delivered chunk")
        require(first["agent"] == second["agent"] == event.get("agent") == role and role in adapter.RESEARCH_ROLES and
                first["request_sha256"] == second["request_sha256"] == event.get("request_sha256") and
                first.get("input_record") == second.get("input_record") == event.get("input_record") and
                first["usage_call_id"] == event.get("failed_usage_call_id") and
                second["usage_call_id"] == event.get("retry_usage_call_id"),
                "Provider retry changed its agent, input, physical kwargs or usage identity")
        if v3:
            require(first["required_calls_after"] == second["required_calls_after"] and
                    first["research_step"] == second["research_step"] and
                    "retry_blocked_reason" not in first and "retry_blocked_reason" not in second,
                    "Provider retry changed or bypassed its required capacity reservation")
        failed_links.add(failed)
        retry_targets.add(retry)
    require(not failed_links & retry_targets and
            failed_links == {row["attempt"] for row in attempts if row["status"] == "error"},
            "A repeated retry or unrecovered provider error cannot pass research acceptance")
    completed = {}
    require(set(bound) == set(inputs), "Actual node inputs are not all bound to provider usage")
    for path, rows in bound.items():
        require(1 <= len(rows) <= 2 and rows[-1]["status"] == "completed" and
                (len(rows) == 1 or rows[0]["attempt"] in failed_links and rows[1]["attempt"] in retry_targets),
                "Each logical node input needs one completed call and at most one transparent retry")
        completed[path] = usages[rows[-1]["usage_call_id"]]
    metrics = {"provider_attempts_verified": len(attempts), "transient_retries_verified": len(retries),
        "failed_provider_attempts_preserved": budget["failed_attempts"],
        "calls_missing_usage_preserved": sum(not row.get("usage_present") for row in usages.values()),
        "recorded_completion_kwargs_hashes_verified": True, "request_hash_scope": "recorded_completion_kwargs",
        "exception_cause_chains_independently_reconstructed": False, "actual_sdk_messages_hash_recorded": False}
    if v2:
        metrics.update(retry_policy_schema_verified=declared_policy, deadline_origin_metadata_verified=True,
            adapter_closed_started_usage_records=len(local_close_evidence),
            adapter_closed_started_usage_end_evidence=local_close_evidence)
    if v3:
        metrics.update(required_capacity_metadata_verified=True,
                       required_step_counts_independently_reconstructed=False)
    return metrics, completed


def verify_research_structure(task, role, state, launch, adapter, initial_state=None):
    """Bind saved node inputs and safe responses; SDK message hashes do not exist."""
    audit = state["research_audit"]
    requests, responses = audit.get("structure_requests", []), audit.get("structure_responses", [])
    input_files = sorted((task / "model-inputs").glob("structure-*.json"))
    response_files = sorted((task / "model-responses").glob("structure-*.txt"))
    expected_count = launch["max_paragraphs"]
    require(type(expected_count) is int and expected_count in (2, 5), "Acceptance outline size must be exactly two or five")
    structure = [{"title": row.get("title"), "content": row.get("content")} for row in state["paragraphs"]]
    provider_trace, provider_calls = _research_provider_trace(task, role, state, launch, adapter)
    require(len(structure) == expected_count, "Outline state paragraph count changed")
    if audit["phase"] == "refinement":
        require(not requests and not responses and not input_files and not response_files,
                "Refinement generated a new model outline instead of preserving initial state")
        require(initial_state is not None and structure == [
            {"title": row.get("title"), "content": row.get("content")} for row in initial_state["paragraphs"]],
            "Refinement titles or content differ from the initial model outline")
        return {"structure_requests_verified": 0, "initial_outline_preserved": True,
                "actual_sdk_messages_hash_recorded": False, **(provider_trace or {})}
    require(audit["phase"] == "initial" and 1 <= len(requests) == len(responses) <= 2,
            "Initial outline requires one or two matching stored requests and responses")
    require(len(input_files) == len(requests) and len(response_files) == len(responses),
            "Outline archive contains missing or untracked node inputs or responses")
    calls = sorted((read(path) for path in (task / "model-usage").glob("*.json")),
                   key=lambda row: (row["started_at"], row["call_id"]))
    require(len(calls) >= len(requests) and len({row["call_id"] for row in calls}) == len(calls),
            "Each model outline request needs its own real usage record")
    previous_text, previous_error, initial_system = None, None, None
    previous_call = None
    unredacted, redacted = 0, 0
    for index, (request_record, response_record) in enumerate(zip(requests, responses), 1):
        request_path = adapter.checked_record(request_record, task)
        response_path = adapter.checked_record(response_record, task)
        require(request_path.resolve() == (task / "model-inputs" / f"structure-{index:03d}.json").resolve() and
                response_path.resolve() == (task / "model-responses" / f"structure-{index:03d}.txt").resolve(),
                "Outline node archive ordinal or owned path changed")
        supplied = read(request_path)
        require(isinstance(supplied, dict) and set(supplied) == {"system", "user"} and
                all(isinstance(supplied[key], str) and supplied[key].strip() for key in supplied),
                "Stored outline input must contain exactly the actual system and user strings")
        if index == 1:
            require(supplied["user"] == launch["topic"], "Stored outline query differs from the original topic")
            initial_system = supplied["system"]
        else:
            require(supplied["system"] == initial_system, "Outline correction silently changed the system contract")
            try:
                correction_input = json.loads(supplied["user"])
            except (TypeError, ValueError) as exc:
                raise ValueError("Outline correction user input is not complete JSON") from exc
            require(isinstance(correction_input, dict) and
                    set(correction_input) == {"topic", "requested_paragraphs", "format_correction"} and
                    correction_input["topic"] == launch["topic"] and
                    type(correction_input["requested_paragraphs"]) is int and
                    correction_input["requested_paragraphs"] == expected_count,
                    "Outline correction is not bound to the original topic and exact paragraph count")
            correction = correction_input["format_correction"]
            require(isinstance(correction, dict) and set(correction) == {
                "validation_error", "instruction", "previous_response_excerpt", "previous_response_truncated"} and
                correction["validation_error"] == previous_error and
                isinstance(correction["instruction"], str) and correction["instruction"].strip() and
                correction["previous_response_excerpt"] == previous_text[:16000] and
                type(correction["previous_response_truncated"]) is bool and
                correction["previous_response_truncated"] == (len(previous_text) > 16000),
                "Outline correction does not carry the actual prior rejection and bounded saved response")
        call = provider_calls[request_path] if provider_trace is not None else calls[index - 1]
        require(call.get("agent") == role and call.get("run_id") == launch["workflow_id"] and
                call.get("status") == "completed" and call.get("ended_at") and
                call["started_at"] <= call["ended_at"], "Outline usage identity, order or completed status changed")
        if previous_call is not None:
            require(previous_call["ended_at"] <= call["started_at"], "Outline requests overlap or changed order")
        previous_call = call
        text = response_path.read_text(encoding="utf-8")
        raw_hash = response_record.get("raw_response_sha256")
        was_redacted = response_record.get("sensitive_values_redacted")
        require(isinstance(raw_hash, str) and re.fullmatch(r"[a-f0-9]{64}", raw_hash) and
                type(was_redacted) is bool, "Outline original response hash or redaction marker is absent")
        safe_hash = hashlib.sha256(text.encode("utf-8")).hexdigest()
        if was_redacted:
            require(raw_hash != safe_hash, "Redacted outline incorrectly claims unchanged original bytes")
            redacted += 1
        else:
            require(raw_hash == safe_hash, "Unredacted outline original response hash changed")
            unredacted += 1
        try:
            decoded = adapter.decode_research_structure(text, expected_count)
        except adapter.AdapterError as exc:
            require(index < len(responses) and response_record.get("validation") == "rejected",
                    "Final outline is malformed or a rejection was relabeled as accepted")
            previous_error = str(exc)
        else:
            require(index == len(responses) and response_record.get("validation") == "accepted",
                    "Accepted outline was unnecessarily regenerated or relabeled")
            require(decoded == structure, "Accepted model outline differs from final state titles or content")
        previous_text = text
    return {"structure_requests_verified": len(requests), "paragraphs_bound_to_state": expected_count,
            "stored_inputs_bind_original_query": True, "actual_sdk_messages_hash_recorded": False,
            "unredacted_original_response_hashes_verified": unredacted, "redacted_response_records": redacted,
            "redacted_original_response_reversible": False, "format_corrections": len(requests) - 1,
            **(provider_trace or {})}


def verify_research_searches(task, role, state, adapter):
    """Verify archived requests, one shared transient retry and honest date recovery."""
    require(role in ("query", "media"), "Provider search audit only applies to Query and Media")
    audit = state["research_audit"]
    require(audit.get("provider_search_failure_boundary") is True,
            "Provider search failure decorator was not actually unwrapped")
    records = audit.get("search_responses", [])
    require(isinstance(records, list) and records and
            len(list((task / "search-responses").glob("search-*.json"))) == len(records),
            "Provider search archive is missing or contains untracked attempts")
    groups, current = [], []
    for ordinal, record in enumerate(records, 1):
        path = adapter.checked_record(record, task)
        require(path.resolve() == (task / "search-responses" / f"search-{ordinal:03d}.json").resolve(),
                "Provider search archive ordinal or owned path changed")
        value = read(path)
        index = record.get("paragraph_index")
        require(type(index) is int and 0 <= index < len(state["paragraphs"]), "Search paragraph identity changed")
        require(isinstance(value, dict) and type(value.get("request_attempt")) is int and
                type(value.get("tool_attempt")) is int and isinstance(value.get("query"), str) and value["query"].strip() and
                isinstance(value.get("tool"), str) and isinstance(value.get("parameters"), dict),
                "Provider search request identity or attempt fields are absent")
        if value["request_attempt"] == 1:
            if current:
                groups.append(current)
            current = []
        require(value["request_attempt"] == len(current) + 1 <= 3 and
                (not current or current[0][0]["paragraph_index"] == index),
                "Provider search request attempts have gaps or cross paragraph boundaries")
        errors = bool(value.get("error_message") or value.get("error_type") or value.get("response_query") == "搜索失败")
        require(record.get("tool") == value["tool"] and
                record.get("result_count") == value.get("result_count") and
                record.get("date_constraints_relaxed") is value.get("date_constraints_relaxed", False) and
                record.get("provider_error_present") is errors,
                "Search archive and audit disagree about an error, result count or date relaxation")
        current.append((record, value, errors))
    if current:
        groups.append(current)
    fallback_tools = ({"search_news_by_date": "basic_search_news", "search_news_last_24_hours": "basic_search_news",
                       "search_news_last_week": "basic_search_news"} if role == "query" else
                      {"search_last_24_hours": "comprehensive_search", "search_last_week": "comprehensive_search"})
    calls_by_paragraph = [0] * len(state["paragraphs"])
    groups_by_paragraph = [0] * len(state["paragraphs"])
    retries, relaxed_groups = 0, []
    for group in groups:
        first_record, first, _ = group[0]
        index = first_record["paragraph_index"]
        require(first["tool_attempt"] == 1 and not first.get("date_constraints_relaxed", False),
                "Search group must start with the original tool and first attempt")
        error_count, relaxed = 0, False
        for offset, (_record, value, error) in enumerate(group):
            calls_by_paragraph[index] += 1
            require(value["query"] == first["query"], "Search retry or fallback changed the actual model-selected query")
            if offset:
                previous = group[offset - 1][1]
                if group[offset - 1][2]:
                    require(value["tool"] == previous["tool"] and value["parameters"] == previous["parameters"] and
                            value["tool_attempt"] == previous["tool_attempt"] + 1 and
                            value.get("date_constraints_relaxed", False) is previous.get("date_constraints_relaxed", False),
                            "Transient search retry changed the tool, query, parameters or date scope")
                else:
                    require(not relaxed and previous.get("result_count") == 0 and previous.get("results") == [] and
                            previous.get("response_query") == previous["query"] and not previous.get("error_message") and
                            fallback_tools.get(first["tool"]) == value["tool"] and
                            value["parameters"] == {} and value["tool_attempt"] == 1 and
                            value.get("date_constraints_relaxed") is True,
                            "Date fallback was not based on a genuine successful empty query")
                    relaxed = True
            if error:
                error_count += 1
                require(error_count <= 1 and offset < len(group) - 1 and
                        value.get("retryable") is True and isinstance(value.get("error_type"), str) and
                        isinstance(value.get("error_module"), str) and
                        value.get("response_query") is None and value.get("results") is None and value.get("result_count") is None,
                        "Search retries bypassed the shared one-error bound or disguised a failure as empty success")
                status = value.get("http_status")
                classified = ((type(status) is int and (status in (408, 409, 429) or 500 <= status <= 599)) or
                    (status is None and ((value["error_module"] == "builtins" and value["error_type"] in ("TimeoutError", "ConnectionError")) or
                     (value["error_module"] == "requests.exceptions" and value["error_type"] in (
                         "Timeout", "ReadTimeout", "ConnectTimeout", "ConnectionError", "ProxyError", "SSLError")))))
                require(classified, "A permanent or unknown provider failure was relabeled as transient")
            else:
                require(isinstance(value.get("results"), list) and type(value.get("result_count")) is int and
                        value["result_count"] == len(value["results"]) and not value.get("error_message") and
                        value.get("response_query") != "搜索失败", "Search success lacks its actual response records")
        require(not group[-1][2] and group[-1][1]["result_count"] > 0,
                "Completed search group ends with a failure sentinel or no evidence")
        retries += error_count
        groups_by_paragraph[index] += 1
        if relaxed:
            relaxed_groups.append((index, first, group[-1][1], len(group), error_count))
    for index, paragraph in enumerate(audit["paragraphs"]):
        require(paragraph["index"] == index and paragraph["search_calls"] == calls_by_paragraph[index],
                "Paragraph search call count does not match every archived provider attempt")
        expected_groups = 1 if audit["phase"] == "refinement" else 1 + paragraph["reflections_executed"]
        require(groups_by_paragraph[index] == expected_groups, "Required initial, reflection or feedback searches are missing")
    changes = audit.get("search_scope_changes", [])
    require(len(changes) == len(relaxed_groups), "Date relaxation audit omitted or invented a recovery")
    for scope, (index, first, final, attempts, errors) in zip(changes, relaxed_groups):
        require(scope.get("paragraph_index") == index and scope.get("requested_tool") == first["tool"] and
                scope.get("effective_tool") == final["tool"] and scope.get("query") == first["query"] and
                scope.get("requested_start_date") == first["parameters"].get("start_date") and
                scope.get("requested_end_date") == first["parameters"].get("end_date") and
                scope.get("date_constraints_relaxed") is True and scope.get("search_attempts") == attempts and
                scope.get("transient_retries") == errors,
                "Date recovery scope differs from the actual bounded archived attempts")
    return {"requests_verified": len(records), "search_groups_verified": len(groups), "transient_retries": retries,
            "date_relaxations": len(relaxed_groups), "provider_search_failure_boundary": True,
            "all_completed_groups_have_real_sources": True}


def verify_research_decisions(task, role, state, launch, adapter, initial_state=None):
    """Replay saved node decisions; actual SDK messages were not hashed."""
    audit = state["research_audit"]
    provider_trace, provider_calls = _research_provider_trace(task, role, state, launch, adapter)
    requests, responses = audit.get("decision_requests", []), audit.get("decision_responses", [])
    expected = [(index, value) for index, paragraph in enumerate(audit["paragraphs"])
                for value in paragraph["queries"]]
    for paragraph in audit["paragraphs"]:
        kinds = (["feedback"] if audit["phase"] == "refinement" else
                 ["initial"] + ["reflection"] * paragraph["reflections_executed"])
        require([value["kind"] for value in paragraph["queries"]] == kinds,
                "Decision kinds do not preserve initial, reflection or feedback order")
    require(len(requests) == len(responses) and len(expected) <= len(requests) <= 2 * len(expected),
            "Decision requests and responses do not cover each bounded logical decision")
    require(len(list((task / "model-inputs").glob("decision-*.json"))) == len(requests) and
            len(list((task / "model-responses").glob("decision-*.txt"))) == len(responses),
            "A saved decision input or response is missing from the audit")
    summaries = [adapter.decode_research_summary(adapter.checked_record(record, task).read_text(encoding="utf-8"))
                 for record in audit.get("summary_responses", []) if record.get("validation") == "accepted"]
    require(len(summaries) == len(expected), "Accepted paragraph summaries do not match the logical decisions")
    decoder = adapter.decode_query_search_decision if role == "query" else adapter.decode_research_search
    cursor, corrections = 0, 0
    for group, (index, expected_decision) in enumerate(expected):
        paragraph = state["paragraphs"][index]
        kind = expected_decision["kind"]
        base, previous_text, previous_error = None, None, None
        for attempt in range(2):
            require(cursor < len(requests), "A logical decision ended without an accepted response")
            request_record, response_record = requests[cursor], responses[cursor]
            ordinal = cursor + 1
            request_path, response_path = adapter.checked_record(request_record, task), adapter.checked_record(response_record, task)
            require(request_path == task / "model-inputs" / f"decision-{ordinal:03d}.json" and
                    response_path == task / "model-responses" / f"decision-{ordinal:03d}.txt",
                    "Saved decision node ordinals or owned paths changed")
            supplied, text = read(request_path), response_path.read_text(encoding="utf-8")
            if provider_trace is not None:
                require(request_path in provider_calls and provider_calls[request_path].get("agent") == role,
                        "Decision response is not bound to its successful physical provider call")
            require(set(supplied) == {"system", "user"} and isinstance(supplied["system"], str) and supplied["system"].strip() and
                    isinstance(supplied["user"], str), "Decision input lacks the actual node system and user text")
            data = json.loads(supplied["user"])
            require(isinstance(data, dict) and supplied["user"] == json.dumps(data, ensure_ascii=False) and
                    data.get("topic") == launch["topic"] and data.get("title") == paragraph["title"] and
                    data.get("content") == paragraph["content"], "Decision node input changed the original topic or paragraph")
            if audit["phase"] == "refinement":
                feedback = data.get("forum_feedback")
                require(isinstance(feedback, str) and hashlib.sha256(feedback.encode()).hexdigest() == audit["forum_feedback_sha256"],
                        "Decision input did not bind the validated forum feedback")
            else:
                require("forum_feedback" not in data, "Initial decision was supplied unbound forum feedback")
            if kind in ("reflection", "feedback"):
                previous_summary = (initial_state["paragraphs"][index]["research"]["latest_summary"]
                                    if kind == "feedback" else summaries[group - 1])
                require(data.get("paragraph_latest_state") == previous_summary,
                        "Reflection decision did not consume the preceding real paragraph summary")
            else:
                require(kind == "initial" and "paragraph_latest_state" not in data,
                        "Initial decision was mislabeled as reflection or feedback")
            if attempt == 0:
                require("format_correction" not in data, "First decision request already contains an unbound correction")
                base = {"system": supplied["system"], "data": data}
            else:
                correction = data.get("format_correction", {})
                require(supplied["system"] == base["system"] and
                        {key: value for key, value in data.items() if key != "format_correction"} == base["data"] and
                        correction.get("validation_error") == previous_error and
                        correction.get("previous_response_excerpt") == previous_text[:16000] and
                        correction.get("previous_response_truncated") is (len(previous_text) > 16000) and
                        isinstance(correction.get("instruction"), str) and correction["instruction"].strip(),
                        "Decision correction is not bound to the preceding rejection and unchanged input")
                corrections += 1
            require(type(response_record.get("sensitive_values_redacted")) is bool and
                    (response_record["sensitive_values_redacted"] or response_record.get("raw_response_sha256") == sha(response_path)),
                    "Decision original response hash or redaction declaration changed")
            cursor += 1
            try:
                decision = decoder(text)
            except adapter.AdapterError as exc:
                require(response_record.get("validation") == "rejected" and attempt == 0,
                        "Completed logical decision has a second rejected or mislabeled raw response")
                previous_text, previous_error = text, str(exc)
            else:
                require(response_record.get("validation") == "accepted" and
                        decision == {key: value for key, value in expected_decision.items() if key != "kind"},
                        "Accepted decision differs from the actual raw model response or recorded research query")
                break
    require(cursor == len(requests), "Extra decision requests were hidden after the last accepted logical decision")
    if role in ("query", "media"):
        searches = [(record["paragraph_index"], read(adapter.checked_record(record, task)))
                    for record in audit.get("search_responses", [])]
        starts = [(index, value) for index, value in searches if value.get("request_attempt") == 1]
        require(len(starts) == len(expected), "Model decisions do not match every actual search group")
        for (index, decision), (searched_index, value) in zip(expected, starts):
            require(index == searched_index and value.get("query") == decision["search_query"] and
                    ("search_tool" not in decision or value.get("tool") == decision["search_tool"]),
                    "Actual search tool or query differs from the accepted model decision")
    for index, paragraph in enumerate(state["paragraphs"]):
        group = max(position for position, (number, _) in enumerate(expected) if number == index)
        require(summaries[group] == paragraph["research"]["latest_summary"],
                "Final paragraph state differs from its accepted raw summary")
    return {"node_requests_verified": cursor, "logical_decisions_verified": len(expected),
            "format_corrections": corrections, "stored_inputs_bind_original_query": True,
            "actual_sdk_messages_hash_recorded": False, "sdk_operation_labels_recorded": False,
            **(provider_trace or {})}


def verify_success(stage, folder, launch, summary, adapter, reporting):
    expected = {launch["workflow_id"] + "-" + suffix for suffix in STAGES}
    snapshot = read(folder / "task-snapshot.json")["tasks"]
    require(len(snapshot) == 9 and {row["task"]["id"] for row in snapshot} == expected and
            all(row["status"] == "SUCCEEDED" for row in snapshot), "Nine exact tasks did not all succeed")
    submitted = read(folder / "submitted-tasks.json")["tasks"]
    require(len(submitted) == 9 and {row["id"] for row in submitted} == expected and
            all(row["max_attempts"] == 1 for row in submitted), "Submitted task identities or retry policy changed")
    pending, visited = {row["id"]: set(row["dependencies"]) for row in submitted}, set()
    while pending:
        ready = {key for key, deps in pending.items() if deps <= visited}
        require(bool(ready), "Submitted dependency graph contains a cycle or foreign ID")
        visited.update(ready)
        for key in ready:
            pending.pop(key)
    events = {row["id"]: row for page in read(folder / "events.json")["pages"] for row in page["events"]}
    successful = [row for row in events.values() if row["type"] == "run.succeeded"]
    require(len(successful) == 9 and {row["attempt_id"] for row in successful} ==
            {identifier + "-attempt-1" for identifier in expected}, "A task was retried or a successful event is missing")
    by_task = {row["task_id"]: row for row in successful}
    edges = 0
    for task in submitted:
        for dependency in task["dependencies"]:
            parent = by_task[dependency]["payload"]["timing"]["finished_at"]
            child = by_task[task["id"]]["payload"]["timing"]["started_at"]
            require(datetime.fromisoformat(parent.replace("Z", "+00:00")) <=
                    datetime.fromisoformat(child.replace("Z", "+00:00")), "A dependent task began before upstream completion")
            edges += 1
    require("resume_manifest_sha256" not in summary, "Summary reused a research checkpoint")
    registry = read(folder / ".control/agents.json")
    expected_adapter = (stage / "revisions" / launch["code_revision"] / "integration/adapter.py").resolve()
    require(len(registry["agents"]) == 5 and all(Path(row["args"][0]).resolve() == expected_adapter and
        "--resume-dir" not in row["args"] for row in registry["agents"]), "Registry used an alternate or resumed Agent")
    goals = {row["id"]: adapter.validate_request({"workflow_id": launch["workflow_id"], "task_id": row["id"], "goal": row["goal"]})
             for row in submitted}
    states, manifests, research = {}, {}, {}
    file_count = 0
    for suffix in STAGES:
        task = folder / (launch["workflow_id"] + "-" + suffix)
        manifest, state = read(task / "result.json"), read(task / "state.json")
        require(manifest["schema"] == adapter.SCHEMA and manifest["workflow_id"] == launch["workflow_id"] and
                manifest["task_id"] == task.name and manifest["mode"] == "live", "Result identity changed")
        require(state["status"] == "completed" and state["parameters"] == goals[task.name] and
                "resumed_from" not in state, "Task completion, parameters or fresh scope changed")
        require(not manifest.get("saved_model_response_reused") and not state.get("editorial_review_applied"), "Task used retained or edited output")
        for record in manifest["files"].values():
            adapter.checked_record(record, task)
            file_count += 1
        budget = read(task / "model-budget.json")
        require(budget["limit"] == goals[task.name]["max_model_requests"] and
                0 < budget["attempts"] <= budget["limit"], "Real task usage exceeded or bypassed its declared bound")
        states[suffix], manifests[suffix] = state, manifest
        role = suffix.removesuffix("-feedback")
        if role not in adapter.RESEARCH_ROLES:
            continue
        if hasattr(adapter, "model_input_context"):
            require("provider_attempts" in budget and "provider_retries" in budget and
                    budget.get("request_hash_scope") == "recorded_completion_kwargs" and
                    budget.get("retry_transient") is True,
                    "New research source omitted its physical provider trace or retry policy")
        detail, audit = state["research_state"], state["research_state"]["research_audit"]
        feedback = suffix.endswith("-feedback")
        reflections = 1 if feedback else launch["insight_max_reflections"] if role == "insight" else launch["max_reflections"]
        require(detail["is_completed"] and len(detail["paragraphs"]) == launch["max_paragraphs"] and
                len(audit["paragraphs"]) == launch["max_paragraphs"], "Research did not complete the declared paragraph count")
        require(audit["phase"] == ("refinement" if feedback else "initial") and
                all(row["reflections_executed"] == reflections for row in audit["paragraphs"]), "Research reflection depth was reduced")
        if feedback:
            require(audit["forum_feedback_sha256"] == manifests["forum"]["files"]["report"]["sha256"] and
                    audit["initial_research_sha256"] == manifests[role]["files"]["state"]["sha256"], "Feedback consumed different initial research or forum")
            require(all(row["feedback_consumed"] and row["source_records_after"] > row["source_records_before"] and
                        row["summary_sha256_after"] != row["summary_sha256_before"] for row in audit["paragraphs"]),
                    "Feedback did not add evidence and revise every paragraph")
        outline = verify_research_structure(task, role, detail, launch, adapter,
            states[role]["research_state"] if feedback else None)
        research[suffix] = {"paragraphs": len(detail["paragraphs"]), "reflections": reflections,
                            "model_requests": budget["attempts"], "feedback_consumed": feedback,
                            "outline": outline}
        research[suffix]["decisions"] = verify_research_decisions(task, role, detail, launch, adapter,
            states[role]["research_state"] if feedback else None)
        if role in ("query", "media"):
            research[suffix]["provider_search"] = verify_research_searches(task, role, detail, adapter)
    report = folder / (launch["workflow_id"] + "-report")
    audit = read(report / "report-audit.json")
    sample = module("automatic_verified_samples", expected_adapter.with_name("sample_inputs.py")).load_sample_inputs(folder / "inputs", launch["topic"])
    comments = adapter.normalize_comments(sample["comments"])
    require(audit["evidence"] == comments and audit["raw_sample_count"] == len(comments) == launch["sample_count"], "Original comments changed")
    require(audit["execution_scope"] == {"resume_used": False, "fresh_from_zero": True} and
            not audit.get("editorial_review_applied") and "retained_response" not in audit, "Report was not a fresh automatic generation")
    states_for_report = {role: states[role + "-feedback"]["research_state"] for role in adapter.RESEARCH_ROLES}
    reports_for_report = {role: adapter.checked_record(manifests[role + "-feedback"]["files"]["report"]).read_text(encoding="utf-8")
                          for role in adapter.RESEARCH_ROLES}
    forum = adapter.checked_record(manifests["forum-final"]["files"]["report"]).read_text(encoding="utf-8")
    rebuilt = rebuild_report_evidence(reporting, states_for_report, reports_for_report, comments, forum, launch["max_content_length"])
    evidence_record = manifests["report"]["files"]["evidence"]
    bundle = read(adapter.checked_record(evidence_record, report))
    require(audit["evidence_bundle"]["sha256"] == evidence_record["sha256"] and
            bundle == rebuilt, "Saved complete evidence bundle differs from unchanged dependency inputs")
    candidate = reporting.validate_report(read(report / "report-content.json"), bundle)
    require(len(candidate["sections"]) == 7 and audit["sections"] == candidate["sections"], "Comprehensive report sections changed")
    expected_html = reporting.render_report(launch["topic"], candidate, bundle, launch["workflow_id"], "live", audit["sample_scope"])
    actual_html = (report / "report.html").read_text(encoding="utf-8")
    require(actual_html == expected_html, "Report HTML differs from the direct automatic evidence renderer")
    require("<script" not in actual_html.lower() and all(html.escape(row["content"]) in actual_html for row in comments.values()), "Rendered original comments changed or contain executable content")
    sample_scope = None
    if reporting.REVIEW_INPUT_SCHEMA == "bettafish.evidence-review-input/v3":
        sample_scope = adapter.file_sample_report_scope(manifests["insight-feedback"]["files"], launch["topic"])
        require(audit["sample_scope"] == sample_scope, "Report sampling metadata differs from verified original provenance")
    review_verification = verify_report_review(report, manifests["report"], audit, candidate, bundle, launch, adapter, reporting, sample_scope)
    attempts = read(report / "report-validation-attempts.json")
    return {"dependency_edges_checked": edges, "file_hashes_verified": file_count, "research": research,
        "report": {"sections": [row["key"] for row in candidate["sections"]], "validation_attempts": attempts,
            "validation_retries": sum(row["status"] in ("rejected", "review_rejected", "evidence_rejected", "provider_error") for row in attempts),
            "automatic_evidence_review": review_verification, "original_comments_exact": len(comments),
            "external_sources": len(bundle["sources"]), "coverage": bundle["coverage"], "sha256": sha(report / "report.html")}}


def main():
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--workflow", required=True)
    cli.add_argument("--stage-dir", type=Path, default=Path(__file__).resolve().parent)
    cli.add_argument("--export", action="store_true")
    cli.add_argument("--check-launch", action="store_true", help="Read-only profile and original launch verification; never business acceptance")
    options = cli.parse_args()
    require(re.fullmatch(r"bf-auto-(?:small|default)-[a-f0-9]{18}", options.workflow) is not None, "Only new automatic-round IDs are allowed")
    stage = options.stage_dir.resolve()
    launch_path = stage / "private/rounds" / (options.workflow + ".json")
    launch = read(launch_path)
    require(launch["workflow_id"] == options.workflow and launch["started_with_no_task_cache"] and not launch["resume_used"], "Launch was not a fresh round")
    configuration, legacy = validate_launch_configuration(stage, launch_path)
    if options.check_launch:
        require(not options.export, "Read-only launch verification cannot export or alter saved records")
        existing_path = stage / "private/runs" / options.workflow / "verification.json"
        existing = read(existing_path) if existing_path.exists() else {}
        print(json.dumps({"workflow_id": options.workflow, "code_revision": launch["code_revision"],
            "status": "launch_configuration_validated_only", "max_model_requests": launch["max_model_requests"],
            "legacy_original_launch_sha256_verified": legacy,
            "profile_configuration_sha256": configuration["profile_configuration_sha256"],
            "existing_business_verification_status": existing.get("status", "not_verified"),
            "existing_verification_bytes_changed": False, "business_acceptance_passed": False,
            "actual_sdk_messages_hash_recorded": False, "model_calls_by_verifier": 0}, ensure_ascii=False))
        return 0
    require(not legacy, "Legacy launches only support --check-launch; preserve their original failed verification")
    require(launch["code_revision"] == read(stage / "stage-manifest.json")["current_revision"],
            "Previous revisions only support --check-launch; preserve their original verification and export")
    folder = inside(stage, stage / "private/runs" / options.workflow)
    summary = read(folder / "summary.json")
    cleanup = summary["cleanup"]
    require(summary["workflow_id"] == options.workflow and summary["mode"] == "live" and summary["status"] in ("completed", "error"), "Round has not terminated")
    require(cleanup.get("tasks_terminal") and cleanup.get("daemon_stopped") and not cleanup.get("error_types"), "Owned runtime cleanup is incomplete")
    require(sha(folder / "inputs/manifest.json") == launch["input_manifest_sha256"], "Frozen input manifest changed")
    counted = usage(folder)
    require(counted["provider_attempts"] == summary["model_usage"]["provider_attempts"] <= launch["max_model_requests"], "Round usage count or bound changed")
    require(counted["logged_calls"] == counted["provider_attempts"], "A provider attempt lacks its own usage audit")
    total = profile_spent(stage, launch["profile"])
    require(total <= launch["aggregate_limit"], "Profile aggregate bound was exceeded")
    _, frozen, _, _ = revision(stage, launch["code_revision"])
    adapter = module("automatic_verified_adapter", frozen / "integration/adapter.py")
    reporting = module("automatic_verified_reporting", frozen / "integration/comprehensive_report.py")
    verification = {"schema": "bettafish.automatic-acceptance/v1", "workflow_id": options.workflow,
        "profile": launch["profile"], "code_revision": launch["code_revision"], "verified_at": utc_now(),
        "status": "failed_run" if summary["status"] == "error" else "pending_validation", "fresh_from_zero": True,
        "resume_used": False, "saved_model_response_reused": False, "editorial_review_applied": False,
        "elapsed_seconds": summary["elapsed_seconds"], "usage": counted, "max_model_requests": launch["max_model_requests"],
        "profile_aggregate_requests": total, "aggregate_limit": launch["aggregate_limit"], "cleanup": cleanup,
        "model_calls_by_verifier": 0, "production_entry_changed_by_verifier": False}
    if summary["status"] == "completed":
        try:
            verification.update(verify_success(stage, folder, launch, summary, adapter, reporting))
            verification["status"] = "passed"
        except Exception as exc:
            verification.update(status="failed_validation", validation_error_type=type(exc).__name__, validation_error=str(exc))
    write(folder / "verification.json", verification)
    if options.export:
        verification["export"] = export(stage, folder, launch, verification, adapter)
        write(folder / "verification.json", verification)
    print(json.dumps(verification, ensure_ascii=False))
    return 0 if verification["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
