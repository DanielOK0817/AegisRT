# ADR-0001: Build a standalone user-space Runtime

- Status: accepted
- Date: 2026-07-19

The Agent-registration portion of this decision is superseded by
[ADR-0002](0002-local-agent-control-boundary.md); task applications now refer
to startup-loaded local Agent specifications by ID.

## Context

The competition asks for unified scheduling, resource management, context
optimization, recovery, communication, and observability. Embedding these
mechanisms inside a specific multi-agent application would make the Runtime
hard to validate independently and could confuse application orchestration with
system-level execution support.

## Decision

Build AegisRT as a clean, standalone Go daemon with a versioned API and a
separate Python SDK. Applications submit generic agent specifications and task
graphs. The first release is single-node and user-space, integrating openEuler
through cgroup v2 and PSI rather than modifying the Linux kernel.

## Consequences

- Core packages remain independent of business-specific agent roles.
- Deterministic simulators can validate scheduling without external LLM noise.
- A separate reference application is required to prove end-to-end usability.
- Distributed scheduling, physical KV-cache control, and arbitrary preemption
  are explicitly outside the initial scope.
