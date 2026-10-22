# CSV contract v1

Each `*.header.csv` contains the exact v1 column order. A collector must compare
its emitted header byte-for-byte before publishing a run. Examples live under
`../examples/` and are `NOT_EXECUTED` serialization fixtures, not measurements.

## Common columns

| Column | Contract |
|---|---|
| `schema_version` | Exact table identifier ending in `/v1`. |
| `run_id` | Opaque ID shared with the manifest; contains no identity or host data. |
| `status` | `SUCCEEDED`, `FAILED`, `TIMED_OUT`, `INVALID`, or `NOT_EXECUTED`. |
| `evidence_scope` | `windows-functional`, `openeuler-kernel`, or `not-executed`. |
| `case_id`, `pair_id` | Safe plan and paired-observation labels. |
| `seed`, `repetition` | Unsigned seed and zero-based measured repetition. |
| `trial_order` | `0` first or `1` second within the pair. |
| `treatment` | Exact plan treatment label. |
| `role` | `baseline` or `treatment`. |
| `analysis_eligible` | F4 only: identical within a pair; `false` for both rows when either row is `INVALID`. |
| `workload_sha256` | Lowercase SHA-256 of canonical workload input. |
| `timeout_ms` | Predeclared trial timeout, including timed-out rows. |
| `reason_code` | `NONE` or an allowlisted reason from the applicable trace schema. No raw error text. |

Integers are base-10 without separators. Ratios are decimal in `[0,1]`.
Durations are decimal milliseconds from a monotonic clock unless a case
explicitly declares a deterministic logical clock. Logical-clock durations are
functional scheduling evidence only and must not be reported as wall-clock
performance. Boolean cells are lowercase `true` or `false`. Empty cells are permitted only for
`NOT_EXECUTED`, `INVALID`, or metrics unavailable because a trial failed before
that measurement boundary. Never use `0` to mean missing and never use `NaN`,
`Inf`, or locale-specific decimal separators.

Warm-up rows do not enter these files. Every measured pair contributes both
rows, including timeout and failure outcomes. F4 retains each row's actual
status, but an invalid harness observation sets `analysis_eligible=false` on
both pair members so row-wise analysis cannot retain the unaffected partner.

## Scheduler table

Header: `scheduler-v1.header.csv`.

- Capacity fields are scheduler reservation units, not observed utilization.
- `workflow_shape` is a predeclared safe label such as `diamond-v1`.
- `jain_fairness` is higher-is-better; throughput is higher-is-better.
- In formal S2, `jain_fairness` uses each workflow's total normalized service
  divided by its logical completion time from the first dispatch. This avoids
  the invariant-total-service error in a fully completed fixed workload.
- In S1 smoke, `critical_path_ms` is the predeclared `beta-critical` segment
  from that task's `READY` boundary to its terminal boundary. It is not
  `makespan_ms`, which spans the first dispatch through the final terminal task.
- `scheduler-task-traces.jsonl` is the task-level recomputation source and each
  line follows `../schemas/scheduler-task-trace-v1.schema.json`.
- Makespan, queue, critical-segment, dependency-unlock, rejections, starvation, and
  decision count are lower-is-better unless the report predeclares otherwise.
- Formal `scheduler-analysis.json` keeps S1 FIFO/Priority, S1 FIFO/DAG-DRCFS,
  S2 FIFO/DAG-DRCFS, and S3 Priority/DAG-DRCFS as separate paired cells. Its
  positive `improvement_*` values always mean the treatment is better in the
  metric's declared direction.

## Context table

Header: `context-v1.header.csv`.

- The functional smoke plan is `context-dedup-budget-drop-smoke-v1`, with cases
  `C1-smoke` and `C2-smoke`. All of its rows use `counter_name=whitespace-v1`,
  `provider_cache_mode=none`, and `evidence_scope=windows-functional`.
- `context_mode` records the implementation mechanism. The only supported
  treatment/mode combinations are `copy-per-task` / `isolated-store-copy-v1`,
  `contextvm-dedup` / `shared-store-blob-dedup-v1`,
  `unbudgeted-logical` / `unbudgeted-assembly-v1`, and
  `contextvm-budgeted` / `budgeted-drop-no-summary-v1`.
- `page_count` is the sum of `StoreStats.UniquePages` across the fresh stores in
  one trial. Every page ID is put exactly once in this workload.
- `storage_logical_tokens`, `storage_physical_tokens`, and
  `storage_reused_tokens` are respectively the sums of the same-named
  `StoreStats` counters across those stores. The collector requires
  `storage_logical_tokens = storage_physical_tokens + storage_reused_tokens`.
  Physical means unique CAS blob payload in counter-equivalent tokens; it does
  not mean process memory, provider KV cache, model input, or billed tokens.
- `dispatch_input_tokens` is the sum of
  `AssemblyMetrics.PhysicalTokens` over dispatches. It counts the assembled text
  with `whitespace-v1`; the smoke harness does not send that text to an LLM.
- `dropped_tokens` and `summary_pages` are sums of the corresponding
  `AssemblyMetrics` fields. Both cases run with the summarizer disabled, so
  `summary_pages` must be zero. C2 treatment drops one unpinned page; this is
  budget enforcement, not summarization or semantic compression.
- `target_budget_exceedances` counts dispatches whose assembled tokens exceed
  the canonical comparison target. C2 baseline intentionally has one such
  exceedance because its applied budget is the logical total; this does not
  mean that the baseline violated its own applied budget.
- `manifest_operations` counts successful manifest-backed assemblies.
  `manifest_conflicts` counts observed conflicts. The smoke workload performs no
  competing mutation, so its zero conflicts are not conflict-handling evidence.
- `elapsed_ms` is the monotonic duration of the local trial boundary. The smoke
  profile has no warm-up and is functional evidence only; it does not support a
  performance comparison.
- `context-trial-traces.jsonl` is the one-record-per-row recomputation source.
  Each line follows `../schemas/context-trial-trace-v1.schema.json` and retains
  only hashes, allowlisted identities, counters, page outcomes, and ACL result
  codes—not raw Context bodies.

## Fault table

Header: `fault-v1.header.csv`.

- The functional profile is `fault-containment-restart-smoke-v1` with
  `F1-smoke`, `F2-smoke`, and `F3-service-smoke`. Fault mode, injection
  boundary, isolation mode, and expected recovery are fixed in
  `fault-input.json` before execution.
- `input_sha256` binds rows to that closed input contract. `clock_mode` is
  `harness-monotonic-v1`; all smoke durations are diagnostics rather than
  performance evidence.
- `attempt_before` and `attempt_after` are ordinal counts, not Runtime IDs.
  `retries` is their difference. F1/F2 treatments are terminal containment and
  therefore have zero retries. Only the F3 service treatment has one retry.
- Baseline injection, detection, requeue, and recovery latencies are empty,
  never zero. Treatment latency cells are populated only when their matching
  `*_observed` flag is true. Censoring flags distinguish missing observations
  from zero-duration observations.
- F3 `requeue_ms` is the injection-to-`READY` duration derived from
  `requeued_ns - injected_ns`; it is not a synthetic difference between two
  identical classification timestamps.
- F1 derives its injection milestone from the process runner's actual
  `ProcessStartedAt` after command start succeeds. F2 uses the executor's actual
  `DeadlineAt`; its absolute monotonic milestone includes process startup and
  is not fixed at the configured 500 ms duration from trial origin.
- F1/F2 run through the production Coordinator, Worker Manager, and process
  runner. F3 gracefully cancels an in-process `runservice.Service`, reopens its
  state file and Runtime, and completes the requeued Attempt. Its trace requires
  a non-null Agent readiness marker no later than injection. The reopen loads
  the already persisted `READY` state and records
  `recovery_performed_on_open=false`; it is not recovery performed by the open
  operation. F3 is not an external daemon restart, hard kill, crash, or cgroup
  cleanup test.
- `orphan_processes` counts only tracked child processes after the declared
  cleanup boundary. `orphan_cgroups` is empty for `windows-functional`; an
  empty value must not be interpreted as zero orphan cgroups.
- `duplicate_artifacts` counts publisher callback observations beyond the first
  for the same `(task_id, artifact_key)`. It is not a count of durable Context
  commits and does not establish exactly-once publication.
- `fault-trial-traces.jsonl` is the one-record-per-row recomputation source and
  each line follows `../schemas/fault-trial-trace-v1.schema.json`.
- Lower observed diagnostic latency and zero tracked duplicates/orphans are
  desirable. Binary success/containment is analyzed with paired methods, but
  no kernel, durability, or performance claim follows from this smoke profile.

## Resource table

Current header: `resource-v2.header.csv`. `resource-v1.header.csv` is retained
only to identify the superseded pre-execution contract.

- `backend=simulated` supports admission logic only;
  `backend=cgroup-v2` requires `openeuler-kernel` evidence.
- `requested_limit` and `observed_value` share `unit`; the source/controller is
  recorded explicitly. `observation_statistic=mean_over_trial` is used for CPU
  because cgroup `cpu.stat` accounting can be batch-refreshed. Memory and PID
  observations use `observation_statistic=peak`.
- PSI fields are kernel values from the declared sampling point, never scheduler
  reservations. Empty PSI is invalid for an openEuler case that claims PSI.

## F4 openEuler kernel fault table

Header: `fault-kernel-v1.header.csv`. This table is independent from
`fault-v1.header.csv`; it cannot contain Windows F1/F2/F3 rows.

- Plans are `f4-daemon-sigkill-cgroup-smoke-v1` / `F4-kernel-smoke` and
  `f4-daemon-sigkill-cgroup-v1` / `F4`. Every executed row uses
  `evidence_scope=openeuler-kernel`, `clock_mode=harness-monotonic-v1`,
  `isolation_mode=cgroup-v2-clone-placement-v1`, and
  `daemon_control=direct-child-process-v1`.
- Metrics use exact monotonic formulas:
  `daemon_exit_ms=(daemon_exited_ns-injected_ns)/1e6`,
  `cleanup_ms=(cleanup_complete_ns-daemon_exited_ns)/1e6`,
  `retry_start_ms=(attempt2_ready_ns-injected_ns)/1e6`,
  `recovery_ms=(recovered_ns-injected_ns)/1e6`, and
  `elapsed_ms=(finished_ns-started_ns)/1e6`. The daemon-exit boundary requires a
  process wait result that confirms signal exit. For `TIMED_OUT`, raw
  `elapsed_ms` includes bounded cleanup; duration analysis right-censors that
  row at `timeout_ms` instead of treating cleanup time as task-completion time.
- Baseline injection-related durations are empty, never zero. Treatment
  durations exist only after their corresponding observation gate succeeds.
- A successful treatment requires an observed populated old Attempt cgroup
  after daemon exit and before restart. This prevents systemd cleanup from
  being attributed to Runtime recovery.
- A successful treatment requires `old_processes_after_cleanup` and
  `old_cgroups_after_cleanup` to be observed and zero. Failed rows retain the
  last observed count, including zero; the CSV cell is empty when that boundary
  was never observed, with the trace's corresponding `*_observed=false`. Never
  interpret an empty cell as zero. `isolation_escape_count` covers only the
  deterministic fixture's known process tree; it is not a malicious-Agent
  sandbox claim.
- `duplicate_artifacts` counts durable output artifacts beyond the expected
  single artifact for each successfully completed task. It is observed through
  the content-free output-summary API after recovery; it is not a callback
  invocation count and does not prove exactly-once external side effects. The
  CSV field is empty when a failed trial ended before this count was observed;
  its trace records `duplicate_artifacts_observed=false` instead of treating an
  unknown value as zero.
- `fault-kernel-trial-traces.jsonl` and `cgroup-samples.jsonl` are the
  recomputation sources. Samples contain counts and controller readback only;
  they never contain PID values or cgroup paths.
- After a real `attempt1_ready` proof, `FAILED`, `TIMED_OUT`, and `INVALID`
  rows retain only milestones and samples actually observed. Missing duration
  boundaries remain empty. Cleanup must append a real absent `trial_terminal`
  sample; otherwise the collection fails closed instead of publishing that row.
- Every planned baseline/treatment pair remains present. `run.completed` means
  collection and sealing completed and is not an all-trials-success assertion.
