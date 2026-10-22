# ADR-0004: Persist Context in a Runtime-bound repository

- Status: accepted
- Date: 2026-07-19
- Implementation anchor: `1b95170`

## Context

Task-boundary recovery from ADR-0003 could restore scheduling state but not the
Context that a recovered Agent was meant to consume. Reconstructing an empty
manifest avoided a missing-head error while silently changing task input.
Context writes also need access control, conditional update, idempotency, and
bounded storage behavior before they can be exposed through the daemon.

The Runtime and Context have different invariants and failure domains. Folding
Context into the Runtime checkpoint would enlarge every scheduler mutation and
couple execution-state recovery to content storage. Introducing a database or
directory CAS now would add a schema and lifecycle contract before the bounded
single-node behavior has experimental evidence.

## Decision

- Store Context in a separate `<state-file>.context` file using the existing
  versioned, checksummed, size-bounded envelope and an independent lifetime
  exclusive-writer lock.
- Persist content blobs once by SHA-256 hash while keeping ACL-bearing page
  identity separate. Persist the complete immutable manifest history, current
  `(workflow_id, agent_id)` heads, and a bounded idempotency ledger.
- Bind the Context checkpoint to the Runtime checkpoint's random, durable
  instance ID. Also fingerprint Agent IDs, token counter, summarizer policy,
  and all Context limits. Reject a mismatched or copied Context file.
- Revalidate content hashes, token counts, page ownership, source references,
  manifest IDs and lineage, head ownership, quotas, ledger results, and the
  canonical idempotency sequence before publishing restored state.
- Use repository-level copy-on-write. Build and validate a complete candidate,
  encode it, save it, and only then publish it in memory. A definite save
  failure retains the old generation. An uncertain post-replacement failure
  retains the candidate generation but poisons readiness.
- Reject a candidate larger than 12 MiB before calling the file store. This is
  a normal capacity error: it neither publishes the candidate nor poisons the
  repository.
- Provision and persist the Context head before committing a task-graph patch.
  Roll back only a newly created pristine head after an ordinary Runtime patch
  rejection. Retain it after an unavailable or uncertain Runtime commit because
  the Runtime file may already contain the task.
- Derive Context ownership from the referenced local Task. Use the Task ID as
  the request-to-principal binding, not as a terminal-state write lock. Require
  a strong manifest precondition and idempotency key for appends.
- Gate scheduling on Context health. If the repository fails after scheduling
  but before Worker launch, release the reservation and restore the Attempt
  counter with `RequeueUnstarted`; do not spend retry budget on infrastructure
  failure.
- Keep persistent summarization disabled until summaries and their source
  lineage can be committed transactionally with the repository.
- Support Runtime checkpoint v1 only through an explicit two-phase migration.
  First persist a v2 Runtime checkpoint with
  `legacy_context_migration_pending`, then create empty Context heads, then
  durably clear the marker. A restart without explicit migration fails closed;
  retrying the explicit migration is idempotent. Historical Context cannot be
  reconstructed by this process.

## Consequences

- Restart restores the exact Context text, manifest revision, and idempotency
  result used by the daemon instead of silently substituting an empty manifest.
- A Context file from another Runtime is rejected even when both daemons have
  identical Agents and limits.
- Task admission and Context mutation have safe ordering but not a cross-file
  atomic transaction. Runtime and Context replacement can succeed
  independently. Normal task-patch recovery relies on retained Context-first
  commits, idempotency, and fail-closed head validation; the pending marker is
  specific to the explicit legacy migration.
- The current file is a full JSON snapshot. Every immutable manifest revision
  is retained and contains its complete reference list, so repeated appends can
  produce quadratic manifest-history growth. The 12 MiB precheck bounds this
  failure mode but does not make it scalable.
- There is no garbage collection, compaction, directory/object CAS, or shared
  storage protocol yet. Those require an explicit lifecycle and ownership
  design before replacing the file repository.
- Context content is plaintext. Restricted file permissions and checksums
  provide local access control and accidental-corruption detection, not
  encryption, authentication, or strong Windows ACL enforcement.
- `WhitespaceCounter` is deterministic and restorable but is not a provider
  tokenizer. Persistent summarization and physical provider KV-cache control
  are not claimed.
- EventLog remains bounded and process-local; this ADR does not make events a
  durable acknowledged bus.
