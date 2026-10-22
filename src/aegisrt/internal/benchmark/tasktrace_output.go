package benchmark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
)

func prepareSchedulerTaskTraces(runID string, result SchedulerResult, records []SchedulerTaskTrace) ([]SchedulerTaskTrace, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	rows := make(map[string]SchedulerRow, len(result.Rows))
	for _, row := range result.Rows {
		key := schedulerTrialKey(row.PairID, row.Policy)
		if _, exists := rows[key]; exists {
			return nil, fmt.Errorf("duplicate scheduler row for %s", key)
		}
		rows[key] = row
	}
	prepared := append([]SchedulerTaskTrace(nil), records...)
	groups := make(map[string][]SchedulerTaskTrace, len(rows))
	for index := range prepared {
		if prepared[index].RunID != "" && prepared[index].RunID != runID {
			return nil, fmt.Errorf("task trace %d has conflicting run id", index)
		}
		prepared[index].RunID = runID
		key := schedulerTrialKey(prepared[index].PairID, prepared[index].Policy)
		row, exists := rows[key]
		if !exists {
			return nil, fmt.Errorf("task trace %d has no scheduler row", index)
		}
		if !schedulerTraceMatchesRow(prepared[index], row, result.InputSHA256) {
			return nil, fmt.Errorf("task trace %d does not match its scheduler row", index)
		}
		groups[key] = append(groups[key], prepared[index])
	}
	for key, row := range rows {
		records := groups[key]
		if len(records) != row.TaskCount {
			return nil, fmt.Errorf("scheduler row %s has %d task traces, want %d", key, len(records), row.TaskCount)
		}
		metrics, err := AggregateSchedulerTaskTraces(records)
		if err != nil {
			return nil, fmt.Errorf("recompute scheduler row %s: %w", key, err)
		}
		if !schedulerMetricsMatch(metrics, row) {
			return nil, fmt.Errorf("scheduler row %s cannot be reproduced from task traces", key)
		}
	}
	return prepared, nil
}

func validateCanonicalSchedulerInputs(result SchedulerResult, workloadJSON, inputJSON []byte) error {
	if len(workloadJSON) == 0 || hashBytes(workloadJSON) != result.WorkloadSHA256 {
		return fmt.Errorf("canonical workload does not reproduce workload hash")
	}
	if len(inputJSON) == 0 || hashBytes(inputJSON) != result.InputSHA256 {
		return fmt.Errorf("canonical scheduler input does not reproduce input hash")
	}
	if !json.Valid(workloadJSON) || !json.Valid(inputJSON) {
		return fmt.Errorf("canonical scheduler input artifacts must be valid JSON")
	}
	return nil
}

func marshalSchedulerTaskTraces(records []SchedulerTaskTrace) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for index, record := range records {
		if err := encoder.Encode(record); err != nil {
			return nil, fmt.Errorf("encode scheduler task trace %d: %w", index, err)
		}
	}
	return output.Bytes(), nil
}

func schedulerTraceMatchesRow(record SchedulerTaskTrace, row SchedulerRow, inputHash string) bool {
	return record.SchemaVersion == schedulerTaskTraceSchemaV1 && record.RunID == row.RunID &&
		record.EvidenceScope == row.EvidenceScope && record.CaseID == row.CaseID &&
		record.PairID == row.PairID && record.Seed == row.Seed && record.Repetition == row.Repetition &&
		record.TrialOrder == row.TrialOrder && record.Treatment == row.Treatment && record.Role == row.Role &&
		record.Policy == row.Policy && record.WorkflowShape == row.WorkflowShape &&
		record.WorkloadSHA256 == row.WorkloadSHA256 && record.InputSHA256 == inputHash &&
		record.TrialTimeoutMS == row.TimeoutMS && record.Status == row.Status
}

func schedulerMetricsMatch(metrics SchedulerTraceMetrics, row SchedulerRow) bool {
	return closeMetric(metrics.MakespanMS, row.MakespanMS) &&
		closeMetric(metrics.QueueP50MS, row.QueueP50MS) && closeMetric(metrics.QueueP95MS, row.QueueP95MS) &&
		closeMetric(metrics.CriticalPathMS, row.CriticalPathMS) &&
		closeMetric(metrics.DependencyUnlockMS, row.DependencyUnlockMS) &&
		closeMetric(metrics.ThroughputTasksS, row.ThroughputTasksS) &&
		closeMetric(metrics.JainFairness, row.JainFairness) &&
		metrics.AdmissionRejections == row.AdmissionRejections && metrics.StarvationCount == row.StarvationCount &&
		metrics.DecisionCount == row.DecisionCount
}

func closeMetric(left, right float64) bool {
	return math.Abs(left-right) <= 1e-9*math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
}

func schedulerTrialKey(pairID, policy string) string {
	return pairID + "/" + policy
}
