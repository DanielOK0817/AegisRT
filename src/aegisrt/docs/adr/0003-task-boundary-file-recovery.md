# ADR-0003: Use a single-writer file checkpoint for task-boundary recovery

- Status: accepted
- Date: 2026-07-19

## Follow-up

This ADR remains the decision record for the Runtime checkpoint and
task-boundary recovery. Its original boundary deliberately left ContextVM out
of that checkpoint. Durable Context was subsequently added as a separately
owned repository; its file, Runtime-instance binding, ordering contract, and
limitations are recorded in
[ADR-0004](0004-durable-context-repository.md).

## Context

The daemon previously lost TaskGraph, scheduler, fairness, retry, and
cancellation state on restart. Persisting only task status would be unsafe:
the scheduler could forget an active reservation, reset workflow fairness, or
reuse an Attempt number. Treating daemon shutdown as user cancellation would
also terminate work that should be retried.

The Runtime is currently single-node and owns all mutations under one lock. A
database or distributed consensus layer would add schema and ownership
complexity before distributed Workers, durable Context, or an acknowledged
message bus exist.

## Decision

- Require exactly one startup mode: `-state-file <path>` or explicit
  `-ephemeral`.
- Store a versioned Runtime checkpoint inside a separate, size-bounded JSON
  envelope with an integrity checksum.
- Persist the complete TaskGraph, scheduler capacity/allocation/reservations,
  DAG-DRCFS virtual runtime, ready timestamps, Attempt counters, and explicit
  active-cancel intent.
- Bind restore to a configuration fingerprint covering policy, resources,
  Agent registry, Worker limit, cgroup identity, and isolation limits.
- Serialize in-memory mutation and checkpoint generation under the Runtime
  lock. Replace the file atomically and hold a process-level exclusive writer
  lock for the daemon lifetime.
- Roll back a mutation after a definite pre-replacement write failure. If
  replacement is visible but directory synchronization fails, retain the new
  generation, poison the Runtime, and stop scheduling rather than guessing.
- Record explicit user cancellation before signaling an active Worker. Treat
  daemon shutdown as interruption and retry only within `max_attempts`.
- Before normalizing a checkpoint that contains active work, require cleanup
  of the exact deterministic Attempt cgroup. Use a stable per-Runtime cgroup
  namespace derived from the canonical durable state-file identity.
- Provide at-least-once task-boundary recovery. Pass Attempt identity, number,
  and idempotency key to the Agent; do not claim exactly-once side effects.
- Keep ContextVM and EventLog outside this Runtime checkpoint until their own
  durable storage and lifecycle contracts are designed.

## Consequences

- Graceful local restart preserves scheduling fairness and retry policy and can
  requeue interrupted work without confusing it with explicit cancellation.
- A corrupt, incompatible, locked, or unsafe active checkpoint fails startup
  closed. A checkpoint write failure makes the running daemon not ready and
  causes mutation APIs to return `503 runtime_unavailable`.
- The state file contains TaskSpec metadata, including goals and idempotency
  keys. It requires local access protection and must not be committed.
- The checksum detects accidental corruption but is neither authentication nor
  encryption. The lock is local, not a distributed fencing lease.
- Active checkpoints cannot be safely migrated across hosts because the new
  host cannot prove that the prior Worker and cgroup are gone.
- Windows lacks a portable parent-directory `fsync` in this implementation.
  Real power-loss behavior, hard-crash orphan cleanup, and cgroup enforcement
  still require openEuler validation.
- At the time this ADR was accepted, restart reconstructed empty Context
  manifests and neither Context nor EventLog survived. ADR-0004 replaces the
  Context part of that historical boundary; EventLog remains process-local.
