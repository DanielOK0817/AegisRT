# Experiment plan v2

This plan is fixed before measurement. Any protocol change creates a new plan
version and new runs; existing raw data is never rewritten.

## Common protocol

- Treatments in one cell use the same workload, input digest, resource
  capacity, timeout, seed, and repetition. Their shared `pair_id` is the unit of
  analysis.
- Run 3 warm-up pairs, then at least 30 measured pairs per cell. Warm-ups are
  tagged `phase=warmup` in events and excluded from measurement CSV files.
- Fixed measured seeds, in order, are: `104729`, `104743`, `104759`, `104761`,
  `104773`, `104779`, `104789`, `104801`, `104803`, `104827`, `104831`,
  `104849`, `104851`, `104869`, `104879`, `104891`, `104911`, `104917`,
  `104933`, `104947`, `104953`, `104959`, `104971`, `104987`, `104999`,
  `105019`, `105023`, `105031`, `105037`, and `105071`.
- Counterbalance order: even seed-list indexes run the baseline first; odd
  indexes run the treatment first. Record `trial_order` as `0` or `1`.
- Use monotonic time for executed-runtime durations. A predeclared deterministic
  simulation may use its logical clock instead, but its plan ID, workload shape,
  and documentation must label that boundary; UTC remains provenance only.
- One trial timeout is fixed before collection and recorded in the manifest.
  A timeout is a measured outcome, not a missing row. Do not retry inside a
  pair; a protocol-level rerun receives a new run and pair ID.
- Exactly one controlled variable changes within a pair. Build, configuration,
  workload digest, and environment remain constant.

Plan v2 fixes the S2 fairness denominator before formal collection. Plan v1
defined `x_i` as each workflow's total completed normalized service, which is
invariant to scheduling order when every fixed task eventually completes and
therefore cannot distinguish fairness. No formal scheduler run was collected
under that definition.

## Matrix

| ID | Area and workload | Baseline | Treatment(s) | Primary metrics | Required scope |
|---|---|---|---|---|---|
| S1 | Scheduler: deterministic diamond DAG under a single scheduler slot | `fifo` | `priority`, `dag-drcfs` | makespan, queue p50/p95, critical-path latency, dependency-unlock latency, decision count | either scope; the slot is a simulated scheduling constraint, not host CPU saturation |
| S2 | Scheduler: concurrent workflows competing across CPU, memory, tokens, and tool slots | `fifo` | `dag-drcfs` | throughput, Jain workflow fairness, p95 queue latency, admission rejections | either scope; kernel claims require openEuler |
| S3 | Scheduler: long non-critical work plus newly ready critical-path work | `priority` | `dag-drcfs` | critical-path latency, dependency unlock latency, starvation count | either scope |
| C1 | Context: repeated immutable pages dispatched to a diamond DAG | `copy-per-task` | `contextvm-dedup` | storage logical/physical/reused tokens, dispatched tokens, elapsed time | either scope |
| C2 | Context: deterministic token-pressure workload | `unbudgeted-logical` | `contextvm-budgeted` | dropped tokens, summary pages, target-budget exceedances, elapsed time | only when both modes are implemented and the counter is named |
| C3 | Context: cold-page compression under a fixed budget | `contextvm-drop` | `contextvm-compress` | summarized source tokens, summary tokens, dropped tokens, provenance completeness | functional evidence only until semantic quality is evaluated |
| F1 | Fault: deterministic Agent non-zero exit | no injection | `agent_exit` | detection latency, final status, unaffected-task success, duplicate artifacts | either scope |
| F2 | Fault: deterministic Agent deadline overrun | no injection | `agent_timeout` | detection/recovery latency, retries, survivor count | either scope |
| F3 | Fault: `runservice.Service` graceful stop during an active Attempt | uninterrupted service | service/statefile reopen | requeue latency, Attempt delta, final status, observed publisher-callback duplicates | `windows-functional`; not daemon hard-kill evidence |
| F4 | Fault: daemon hard kill with an active Attempt cgroup | uninterrupted | `daemon_sigkill` | orphan cleanup, restart result, recovery latency, isolation escape | `openeuler-kernel` only |
| F5 | Fault: corrupt or incompatible checkpoint | valid checkpoint | corrupted checkpoint | fail-closed result, mutation count after rejection | either scope |
| R1 | Resource: multi-resource admission at and above capacity | simulated capacity | identical requests | admitted/rejected counts, ledger invariants | `windows-functional` is functional only |
| R2 | Resource: CPU quota saturation | unrestricted cgroup | configured `cpu.max` | observed CPU, throttle events, elapsed time, PSI | `openeuler-kernel` only |
| R3 | Resource: memory ceiling and process ceiling | unrestricted cgroup | configured `memory.max` or `pids.max` | peak, OOM/PID events, containment, survivor count | `openeuler-kernel` only |

An unimplemented matrix row stays planned. It receives no synthetic metric and
must not be described as a result. Each report names the exact rows executed.

## Functional smoke profiles

Smoke profiles are implementation gates outside the formal repetition protocol
above. Their plan and case IDs include `smoke`, their manifests record zero
warm-up pairs and their actual requested repetitions, and their measurements
must not be pooled with or substituted for formal C1/C2/C3, F4, or R2/R3
results.

The implemented Context profile is
`context-dedup-budget-compression-smoke-v2`. `C1-smoke` performs four deterministic
Agent-scoped Context assemblies. Its baseline uses one isolated store per Agent;
its treatment uses one shared store whose content-addressed blob payload is
deduplicated while page IDs and ACL ownership remain distinct. The smoke case
does not execute the planned diamond dependency graph and therefore does not
measure scheduling or graph latency. Equal assembled-text hashes and an
allowlisted cross-Agent `ACCESS_DENIED` probe are correctness gates.

`C2-smoke` uses one deterministic manifest with three distinct workflow pages,
including one pinned page, and a six-token comparison target. The baseline
applies the full twelve-token logical budget. The treatment applies the target,
drops one unpinned six-token page, and preserves the pinned page. Its summarizer
is disabled, so `summary_pages`, summarized-source tokens, and summary tokens
must remain zero. This is a drop-policy test, not a summarization or semantic-
compression experiment.

`C3-smoke` reuses the C2 source pages with an eight-token target. Its baseline
drops the cold six-token page. Its treatment invokes the deterministic
extractive reference summarizer, retains five summarized tokens with a
source-to-summary digest link, and drops the unrelated optional three-token
page. The treatment includes the persisted summary page in storage counters.
This validates bounded transactional summary mechanics, not semantic fidelity.

All Context smoke cases use `whitespace-v1`, `provider_cache_mode=none`, fresh
stores per trial, and `windows-functional` evidence. Their monotonic elapsed
times are diagnostics only: zero warm-ups and the small functional workload do
not support performance claims. They also make no LLM call, observe no provider
KV cache, and measure neither process memory nor end-task quality.

## Formal scheduler profile

The fixed profile is `scheduler-s1-s2-s3-formal-v1`. It uses warm-up seeds
`104701`, `104707`, and `104711`, the 30 common measured seeds, one repetition,
and a deterministic logical clock. It publishes only measured rows; warm-up
trials appear in `events.jsonl` with `phase=warmup`. The canonical workload and
input artifacts bind all three cases, policy parameters, capacities, service
times, dependency graphs, starvation limits, and critical segments.

- S1 uses one workflow and one scheduler slot. It compares FIFO separately
  with Priority and DAG-DRCFS on the same diamond DAG.
- S2 uses three concurrent workflows and CPU, memory, token, and tool-slot
  reservations. It compares FIFO with DAG-DRCFS.
- S3 makes a critical task dynamically ready while a high-priority non-critical
  chain remains runnable. It compares Priority with DAG-DRCFS.

`scheduler-analysis.json` reports each treatment cell independently. It uses
the predeclared improvement direction, paired median difference, 10,000-sample
paired percentile-bootstrap 95% interval with seed `424242`, and paired
rank-biserial effect size. Logical-clock throughput and latency are scheduler
simulation results, not host wall-clock or kernel performance.

The implemented Fault profile is `fault-containment-restart-smoke-v1`.
`F1-smoke` records a deterministic non-zero Agent exit and `F2-smoke` records a
configured Agent deadline overrun. Both are terminal containment cases: the
target fails without retry and the unaffected task still completes.
`F3-service-smoke` gracefully cancels an in-process `runservice.Service`,
after an explicit Agent readiness marker, requeues the interrupted Attempt,
reopens the state file and Runtime, and completes one retry. The reopen loads
an already persisted `READY` task (`recovery_performed_on_open=false`); it does
not recover an active Attempt while opening. It is not the external daemon
hard-kill/cgroup case F4.

The Fault smoke profile uses a local monotonic harness clock and tracked child
processes on Windows. It cannot support claims about cgroup cleanup, heartbeat
or lease-expiry recovery, power-loss durability, or exactly-once publication.
Its duplicate metric counts repeated in-memory publisher callback observations,
not durable Context commits.

## F4 openEuler kernel profiles

F4 is a separate kernel experiment and must not be merged with the Windows F1/F2/F3 evidence.
Its smoke plan is
`f4-daemon-sigkill-cgroup-smoke-v1` with case `F4-kernel-smoke`; its formal plan
is `f4-daemon-sigkill-cgroup-v1` with case `F4`. Both require
`evidence_scope=openeuler-kernel`, delegated cgroup v2 `cpu`, `memory`, and
`pids` controllers, and available PSI files. A Windows run cannot be relabeled
as either profile.

The experiment runs in a dedicated delegated systemd service whose main process
is the benchmark supervisor. The Runtime daemon is a direct child of that
supervisor. The treatment sends `SIGKILL` only to the child daemon it created
and for which it retains the process handle; it never targets the production
`aegisrt.service`, an arbitrary PID, unit, or cgroup path. Keeping the supervisor
alive prevents systemd restart cleanup from being mistaken for Runtime cleanup.
The service's final control-group teardown remains a safety backstop after the
trial.

Each pair uses one deterministic target Agent tree and one completed survivor.
The baseline lets Attempt 1 complete normally. The treatment waits for Attempt
1 readiness and `cgroup.events populated=1`, confirms its known descendants are
inside the exact Attempt cgroup, hard-kills the daemon, and proves the old
cgroup remains populated before restart. It then starts an identical daemon
with the same state and delegated root. Startup recovery must load the
checkpoint, clean the old Attempt, requeue exactly one target, observe Attempt
2 readiness, and finish both tasks as `SUCCEEDED`. Final old-process and
old-cgroup counts, isolation escapes, and duplicate durable artifact observations must
all be zero.

The smoke profile has no warm-up and permits at most four measured
seed/repetition cells. The formal profile fixes warm-up seeds `104701`,
`104707`, and `104711`, uses the 30 common measured seeds above, and fixes one
repetition. Every measured row is backed by a closed trial trace plus anonymous
cgroup samples at `attempt1_ready`, `post_daemon_exit`,
`post_runtime_restart`, and `trial_terminal`. Formal results are publishable
only from a clean Runtime commit; smoke results are implementation gates, not a
substitute for formal evidence.

## R2/R3 openEuler resource-controller smoke

The implemented `resource-cgroup-pressure-smoke-v1` plan is a separate
one-seed, one-repetition kernel gate. It requires a clean Runtime build,
`evidence_scope=openeuler-kernel`, cgroup v2 delegation for `cpu`, `memory`, and
`pids`, and available PSI. It produces one baseline/treatment pair for R2 and
one pair for each R3 controller:

- R2 compares an unrestricted cgroup with `cpu.max=25000 100000` while both
  sides perform 700 process CPU milliseconds of the same deterministic work.
- R3-memory compares an unrestricted 256 MiB allocation fixture with
  `memory.max=67108864` and `memory.swap.max=0`.
- R3-pids compares an unrestricted bounded child-process fixture with
  `pids.max=12`; the kernel count includes threads.

CPU `observed_value` is cumulative `cpu.stat usage_usec` divided by the whole
trial duration and tagged `mean_over_trial`; sampling-window peaks are not used
because kernel accounting may refresh in batches. Memory and PID values are
tagged `peak`. A treatment passes only when the corresponding throttle, OOM
kill, or PID-limit counter is observed. Expected fixture termination at the
memory/PID boundary is containment evidence, while unexpected baseline or CPU
fixture failure remains a failed row. The sealed smoke run verifies controller
mechanics but is not a substitute for the 30-pair formal protocol.

## Metric rules

- `makespan_ms`: last terminal time minus first dispatch time for the measured
  workflow set, using either the runtime monotonic clock or the predeclared
  deterministic logical clock.
- `queue_ms`: dispatch time minus ready time. Report p50 and p95 using nearest-
  rank percentiles over all measured tasks, and retain task-level raw samples.
- `critical_path_ms`: terminal time of the last task minus ready time of the
  first task in the predeclared critical path segment. The complete longest
  path, measured segment, and metric semantic are canonical workload metadata,
  never inferred after seeing results.
- `dependency_unlock_ms`: dispatch time minus ready time of the predeclared
  dependency-unlock target.
- `decision_count`: successful scheduler reservations in the trial. A temporary
  no-feasible-candidate result is not a decision or an admission rejection.
- `throughput_tasks_s`: terminal tasks divided by the declared elapsed seconds.
  For a deterministic scheduler simulation this uses logical elapsed time and
  must not be reported as host or Runtime wall-clock throughput.
- `admission_rejections`: tasks permanently refused by the declared admission
  policy. A temporarily infeasible scheduling attempt while resources are
  reserved leaves the task queued and is not an admission rejection.
- `jain_fairness`: `(sum(x_i)^2) / (n * sum(x_i^2))`, where `x_i` is the
  workflow's total dominant-resource-normalized service divided by its logical
  completion time from the first dispatch. This is a completion-rate fairness
  measure for the fixed, fully completed S2 workload. Record zero-service
  workflows; do not drop them.
- Context `storage_logical_tokens`, `storage_physical_tokens`, and
  `storage_reused_tokens` are sums of `StoreStats` across fresh trial stores;
  require `storage_logical_tokens = storage_physical_tokens +
  storage_reused_tokens`. They describe unique CAS blob payload in the declared
  deterministic counter, not RAM, LLM input, billed tokens, or provider KV
  cache. `dispatch_input_tokens` separately sums
  `AssemblyMetrics.PhysicalTokens` for locally assembled text.
- `target_budget_exceedances` counts dispatches whose assembled input exceeds
  the canonical comparison target. An unbudgeted baseline may intentionally
  exceed that target while still respecting its larger applied budget.
- Context `dropped_tokens` and `summary_pages` are sums of the corresponding
  `AssemblyMetrics` fields. A dropped page with `summary_pages=0` must not be
  described as summarized or semantically compressed.
- Context `manifest_operations` counts successful manifest-backed assemblies.
  A zero `manifest_conflicts` value with no competing mutation is not evidence
  of conflict handling.
- Fault `detection_ms` starts at the recorded injection boundary and ends at
  the harness-observed classification boundary. In F3 service smoke,
  `requeue_ms` also starts at injection and ends when the interrupted task is observed `READY`, and
  `recovery_ms` ends when the retried target reaches `SUCCEEDED`. In F1/F2,
  recovery means contained terminal failure, not a retry. These observations
  are not described as durable unless a separate protocol proves durability.
- F1's injection boundary is the process runner's actual `ProcessStartedAt`
  after command start succeeds. F2's boundary is the executor's actual
  `DeadlineAt`, not a fixed duration from trial origin. Thus the F2 absolute
  milestone normally exceeds 500 ms even though the configured deadline
  duration is 500 ms.
- Resource observations come from the named kernel/controller source. CPU uses
  the declared whole-trial mean; memory and PID use peaks. Scheduler
  reservations are not CPU or memory utilization.
- Scheduler runs retain canonical workload input plus task-level READY,
  dispatch, terminal, service, and dispatch-order records so every aggregate
  duration and count can be independently recomputed.
- Context runs retain a canonical replay specification without raw Context
  bodies, the complete input policy, and one closed trial trace per CSV row.
  Store snapshots, page hashes and outcomes, assembled-text hashes, pinned-page
  gates, and stable ACL result codes must reproduce every aggregate counter.
- Fault runs retain a canonical safe-process workload, a closed input contract,
  and one trace per CSV row. Attempt status, exit/timeout classification,
  service-level statefile reopen, tracked-child cleanup, and publisher callback
  observations must reproduce each aggregate Fault row.
- F4 kernel runs additionally retain component hashes and PID-free, path-free
  cgroup samples. Their daemon-exit, cleanup, retry-start, and recovery metrics
  are recomputed from one supervisor monotonic clock. `orphan_observed_before_restart`
  is a required treatment gate that attributes cleanup to Runtime restart rather
  than systemd teardown.
- F4 failures after the real `attempt1_ready` gate remain in the complete paired
  matrix as `FAILED`, `TIMED_OUT`, or `INVALID` with an allowlisted reason code.
  Preserve every structurally valid sample actually observed, prove terminal
  Attempt-cgroup absence during cleanup, and continue the remaining matrix.
  Failures before that gate, or cleanup failures that cannot form a valid
  anonymous trace, abort collection before publication. A `run.completed` event
  means the planned collection was sealed, not that every trial succeeded.

## Statistical analysis

For each cell and metric, publish baseline and treatment `n`, failures/timeouts,
p50, p95, median, and IQR. Analyze the paired difference in the direction marked
in `csv/README.md`; publish its median and a paired percentile-bootstrap 95% CI
using 10,000 resamples and bootstrap seed `424242`. Also publish the paired
rank-biserial effect size. A two-sided Wilcoxon signed-rank p-value may be shown
as supporting evidence; apply Holm correction across metrics in the same matrix
row and never present it without effect size and interval.

For binary containment/success outcomes, publish counts and Wilson 95% intervals
per treatment, plus exact McNemar results for paired discordant outcomes. No
imputation is allowed. Raw `elapsed_ms` retains actual supervisor time,
including bounded cleanup after a timeout. Duration analysis treats a
`TIMED_OUT` row as right-censored at its configured `timeout_ms` and counts it
separately; it must not use cleanup-inclusive elapsed time as task-completion
time. Exclusion is permitted only for a predeclared harness failure; keep the
affected row as `INVALID`, record an allowlisted reason code, and set
`analysis_eligible=false` for both members of that pair. Analysis must filter
on this pair-level marker, never on one row's status alone.

Do not pool Windows and openEuler observations, different commits, dirty and
clean builds, architectures, kernel versions, or changed resource capacities.
Charts and tables are derived from sealed raw rows and must identify the run IDs
and analysis code hash used.
