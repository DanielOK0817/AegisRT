# ADR-0005: Strict Agent output and durable ContextChannel

## Status

Accepted on 2026-07-20.

Amended on 2026-09-22. The original decision below records the July baseline.
Current behavior saves an attempt-bound completion intent before publication,
then replays saved output before scheduling after restart. Task input bindings
and direct dependency permissions filter the shared principal store; unrelated
tasks no longer inherit its full history. Context format-v2 persists these
bindings and rejects format-v1 pending explicit migration. See
[the current Agent protocol](../agent-protocol.md) for the implemented contract.

## Context

Process Agents previously had bounded stdout but no machine-verifiable result
contract. Free-form output could not be safely routed, recovered, deduplicated,
or distinguished from logs. Context manifests were durable, but a producing
task had no durable completion/output record from which a later dependant could
recover its inputs.

## Decision

1. A successful process emits empty stdout or one strict, bounded
   `aegisrt.agent-output/v1` JSON envelope. Diagnostics use stderr.
2. The Worker parses output before reporting success. Invalid, truncated, or
   oversized output fails the Attempt without copying raw content into errors.
3. The Coordinator publishes the envelope before completing the Runtime task.
4. ContextState commits the full envelope, delivery ledger entries, Context
   pages/manifests, and an output record in one Context checkpoint mutation.
5. An empty envelope still creates a durable completion marker.
6. Workflow artifacts can reach only the producer principal and Agent
   principals represented by direct DAG dependant tasks; private artifacts
   stay with the producer.
7. Durable output provenance enables idempotent delivery to Agent principals
   represented by direct dependant Tasks admitted after publication or restart.
   Missing dependency records fail closed.

## Consequences

This is a breaking process-Agent contract: any nonempty stdout that is not a
valid envelope now fails. It provides deterministic parsing, bounded fan-out,
content-addressed reuse, atomic Context publication, explicit empty results,
and restart-safe late hydration. Event payloads remain a safe metadata
projection and do not contain artifacts.

The design is not a general message bus. Output records are bounded but have no
GC; full Context snapshots and a global repository lock limit scale; structured
artifact provenance is not rendered into downstream Context text. Runtime and
Context checkpoints remain independently atomic, so a common generation token
or transactional backend is still required for startup-time cross-file
consistency. Until then, missing output markers are detected at dependant
resolution and fail closed. Context is principal-scoped rather than per-task,
so tasks that reuse one `(workflow_id, agent_id)` share the delivered page.
Older additive format-v1 Context files have no output markers and no automatic
backfill; unfinished DAGs should be drained or state backed up before upgrade.
At-least-once task recovery can rerun after output publication but before
Runtime completion; that retry must reproduce the same semantic envelope or it
fails with an idempotency conflict. No exactly-once side-effect claim follows
from Context delivery deduplication.
