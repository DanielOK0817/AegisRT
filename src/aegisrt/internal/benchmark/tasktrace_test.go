package benchmark

import (
	"math"
	"testing"
)

func TestTaskTracesRecomputeEverySchedulerAggregate(t *testing.T) {
	evidence, err := SimulateSchedulerEvidence(SchedulerPlan{Seeds: []uint32{7, 11}, Repetitions: 2})
	if err != nil {
		t.Fatal(err)
	}
	records := evidence.TaskTraceRecords()
	if got, want := len(records), len(evidence.Result.Rows)*4; got != want {
		t.Fatalf("task traces = %d, want %d", got, want)
	}
	for _, row := range evidence.Result.Rows {
		trial := tracesForRow(records, row)
		derived, deriveErr := AggregateSchedulerTaskTraces(trial)
		if deriveErr != nil {
			t.Fatalf("aggregate %s/%s: %v", row.PairID, row.Policy, deriveErr)
		}
		assertFloat(t, "makespan", derived.MakespanMS, row.MakespanMS)
		assertFloat(t, "queue p50", derived.QueueP50MS, row.QueueP50MS)
		assertFloat(t, "queue p95", derived.QueueP95MS, row.QueueP95MS)
		assertFloat(t, "critical path segment", derived.CriticalPathMS, row.CriticalPathMS)
		assertFloat(t, "dependency unlock", derived.DependencyUnlockMS, row.DependencyUnlockMS)
		assertFloat(t, "throughput", derived.ThroughputTasksS, row.ThroughputTasksS)
		assertFloat(t, "Jain fairness", derived.JainFairness, row.JainFairness)
		if derived.AdmissionRejections != row.AdmissionRejections ||
			derived.StarvationCount != row.StarvationCount || derived.DecisionCount != row.DecisionCount {
			t.Fatalf("derived counts = %#v, row = %#v", derived, row)
		}
		if derived.CriticalPathMS == derived.MakespanMS {
			t.Fatalf("critical segment response unexpectedly equals makespan for %s/%s", row.PairID, row.Policy)
		}
	}
	records[0].TaskID = "mutated"
	if evidence.TaskTraceRecords()[0].TaskID == "mutated" {
		t.Fatal("task trace accessor exposed mutable evidence")
	}
}

func TestTaskTraceDispatchOrderShowsPolicyDifference(t *testing.T) {
	evidence, err := SimulateSchedulerEvidence(SchedulerPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	sequences := map[string]map[string]int{}
	for _, record := range evidence.TaskTraceRecords() {
		if record.PairID != "s1-smoke-7-0-"+record.Policy && record.Policy != "fifo" {
			continue
		}
		if sequences[record.Policy] == nil {
			sequences[record.Policy] = map[string]int{}
		}
		sequences[record.Policy][record.TaskID] = record.DispatchSequence
	}
	if sequences["fifo"]["alpha-aux"] >= sequences["fifo"]["beta-critical"] {
		t.Fatalf("FIFO order = %#v, want auxiliary before beta", sequences["fifo"])
	}
	for _, policy := range []string{"priority", "dag-drcfs"} {
		if sequences[policy]["beta-critical"] >= sequences[policy]["alpha-aux"] {
			t.Fatalf("%s order = %#v, want beta before auxiliary", policy, sequences[policy])
		}
	}
}

func TestAggregateSchedulerTaskTracesRejectsTampering(t *testing.T) {
	evidence, err := SimulateSchedulerEvidence(SchedulerPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	row := evidence.Result.Rows[0]
	valid := tracesForRow(evidence.TaskTraceRecords(), row)
	tests := []struct {
		name   string
		mutate func([]SchedulerTaskTrace) []SchedulerTaskTrace
	}{
		{name: "missing task", mutate: func(records []SchedulerTaskTrace) []SchedulerTaskTrace { return records[1:] }},
		{name: "invalid service", mutate: func(records []SchedulerTaskTrace) []SchedulerTaskTrace { records[0].ServiceMS++; return records }},
		{name: "mixed trial", mutate: func(records []SchedulerTaskTrace) []SchedulerTaskTrace {
			records[0].PairID = "other-pair"
			return records
		}},
		{name: "duplicate dispatch", mutate: func(records []SchedulerTaskTrace) []SchedulerTaskTrace {
			records[1].DispatchSequence = records[0].DispatchSequence
			return records
		}},
		{name: "missing segment", mutate: func(records []SchedulerTaskTrace) []SchedulerTaskTrace {
			for index := range records {
				records[index].CriticalSegmentOrder = -1
			}
			return records
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records := append([]SchedulerTaskTrace(nil), valid...)
			if _, err := AggregateSchedulerTaskTraces(test.mutate(records)); err == nil {
				t.Fatal("tampered task traces were accepted")
			}
		})
	}
}

func tracesForRow(records []SchedulerTaskTrace, row SchedulerRow) []SchedulerTaskTrace {
	result := make([]SchedulerTaskTrace, 0, row.TaskCount)
	for _, record := range records {
		if record.PairID == row.PairID && record.Policy == row.Policy {
			result = append(result, record)
		}
	}
	return result
}

func assertFloat(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %.12f, want %.12f", label, got, want)
	}
}
