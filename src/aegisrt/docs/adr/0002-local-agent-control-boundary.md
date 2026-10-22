# ADR-0002: Separate local Agent authority from the HTTP task control plane

- Status: accepted
- Date: 2026-07-19
- Supersedes: the ADR-0001 statement that applications submit Agent
  specifications through the API

## Context

The M0 API allowed a remote client to advance scheduler ticks and report task
completion. Connecting those mutations to a real Worker would allow a client to
reserve work without launching it or race the Coordinator that owns the
Attempt. Accepting executable paths, arguments, or environment through the same
HTTP boundary would also turn task submission into remote command execution.

The current daemon exposes plain HTTP only on loopback. Task snapshots contain
goals and idempotency keys that may be application-sensitive, so loopback alone
is not an authorization boundary.

## Decision

- Load Agent specifications once from a bounded, permission-checked local JSON
  file before opening the listener.
- Permit only the `process` runtime and absolute local executable paths; allow
  optional confinement beneath an administrator-selected Agent root.
- Require a restricted local token file and Bearer authentication for task
  graph submission, task listing, and cancellation.
- Store only a SHA-256 token digest in the HTTP server and compare digests in
  constant time.
- Restrict the listener to loopback until TLS and a remote identity model are
  designed.
- Let RunService and Coordinator exclusively own scheduling, process execution,
  completion, failure, and final resource release.
- Expose cancellation as intent. Active cancellation returns before the Worker
  exits, while the reservation remains held until terminal cleanup.
- Remove manual tick and completion routes.

## Consequences

- An authenticated client can select only a pre-registered `agent_id`; it
  cannot choose a command or host environment.
- Agent configuration changes require a daemon restart in the current release.
- Existing M0 clients must add Bearer authentication, stop calling tick and
  completion routes, and use the cancellation endpoint when needed.
- The API is safe for the current local control plane but is not yet suitable
  for direct remote exposure.
- A future Agent registration service requires a separate signed artifact,
  policy, and administrator-authority design rather than reopening task HTTP
  as a command boundary.
