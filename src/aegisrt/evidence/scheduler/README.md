# Formal scheduler evidence

> **EXECUTED DETERMINISTIC FORMAL PROFILE**

The fixed `scheduler-s1-s2-s3-formal-v1` profile was executed on 2026-07-22
from clean source commit `4eed73e4e42190a9c0be2a759eed5ace470c27e2`.
The sealed run ID is `run-4f302bae3565b29421ddc9da`. Its manifest records
`runtime_dirty=false`, 12 warm-up pairs, 120 measured pairs, 240 measured CSV
rows, and a passed anonymity scan.

## Published bundle

`formal-run-20260722-4eed73e/` contains the manifest, canonical workload and
input contract, measured CSV, one task-level recomputation trace for every
task, warm-up and measurement events, paired statistical analysis, environment
record, and `SHA256SUMS`. Every listed digest was rechecked after promotion.

The protocol uses three fixed warm-up seeds and 30 fixed measured seeds. S1
contains two independent cells (FIFO/Priority and FIFO/DAG-DRCFS), S2 compares
FIFO/DAG-DRCFS, and S3 compares Priority/DAG-DRCFS. Trial order alternates by
seed-list index. Warm-ups are retained in events and excluded from measurement
rows.

## Key paired results

All values below are medians over 30 matched pairs. Positive improvement means
the treatment is better in the metric's declared direction.

| Cell and metric | Baseline | DAG-DRCFS | Median improvement | Paired bootstrap 95% CI | Rank-biserial |
| --- | ---: | ---: | ---: | ---: | ---: |
| S1 dependency unlock, logical ms | 11 | 0 | 11 | [10, 11] | 1.0 |
| S2 Jain completion-rate fairness | 0.8262 | 0.9974 | 0.1708 | [0.1690, 0.1717] | 1.0 |
| S3 critical-path latency, logical ms | 184 | 59 | 126 | [122, 126] | 1.0 |
| S3 dependency unlock, logical ms | 157 | 31 | 126 | [122, 126] | 1.0 |
| S3 starvation count | 1 | 0 | 1 | [1, 1] | 1.0 |

The complete analysis also retains neutral and adverse metrics instead of
selecting only improvements. In particular, S1 exposes the queue-tail tradeoff
of prioritizing the critical branch. See `scheduler-analysis.json` for every
predeclared metric.

## Claim boundary

This is formal paired policy evidence from a predeclared deterministic
discrete-event logical clock. It demonstrates scheduling order, DAG response,
multi-workflow completion-rate fairness, resource-reservation behavior, and
starvation under the fixed workloads. It is not host wall-clock throughput,
openEuler kernel timing, LLM quality, provider cost, or large-cluster evidence.
The openEuler controller and hard-crash evidence is published separately under
`evidence/openeuler/`.
