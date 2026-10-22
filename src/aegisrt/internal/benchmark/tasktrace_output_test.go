package benchmark

import "testing"

func TestPrepareSchedulerTaskTracesRejectsIncompleteOrInconsistentEvidence(t *testing.T) {
	const runID = "run-0123456789abcdef01234567"
	evidence, err := SimulateSchedulerEvidence(SchedulerPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	validResult := evidence.Result
	for index := range validResult.Rows {
		validResult.Rows[index].RunID = runID
	}
	validRecords := evidence.TaskTraceRecords()
	if _, err := prepareSchedulerTaskTraces(runID, validResult, validRecords); err != nil {
		t.Fatalf("valid traces rejected: %v", err)
	}
	tests := []struct {
		name   string
		result SchedulerResult
		record []SchedulerTaskTrace
	}{
		{name: "missing trace", result: validResult, record: validRecords[1:]},
		{name: "orphan trace", result: validResult, record: mutateTrace(validRecords, func(record *SchedulerTaskTrace) { record.PairID = "orphan-pair" })},
		{name: "conflicting run", result: validResult, record: mutateTrace(validRecords, func(record *SchedulerTaskTrace) { record.RunID = "run-aaaaaaaaaaaaaaaaaaaaaaaa" })},
		{name: "aggregate drift", result: mutateResult(validResult, func(row *SchedulerRow) { row.MakespanMS++ }), record: validRecords},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := prepareSchedulerTaskTraces(runID, test.result, test.record); err == nil {
				t.Fatal("invalid task-trace evidence was accepted")
			}
		})
	}
}

func TestValidateCanonicalSchedulerInputsRejectsByteDrift(t *testing.T) {
	evidence, err := SimulateSchedulerEvidence(SchedulerPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	workload, input := evidence.CanonicalWorkloadJSON(), evidence.CanonicalInputJSON()
	if err := validateCanonicalSchedulerInputs(evidence.Result, workload, input); err != nil {
		t.Fatalf("canonical inputs rejected: %v", err)
	}
	workload = append(workload, ' ')
	if err := validateCanonicalSchedulerInputs(evidence.Result, workload, input); err == nil {
		t.Fatal("workload byte drift was accepted")
	}
}

func mutateTrace(records []SchedulerTaskTrace, mutate func(*SchedulerTaskTrace)) []SchedulerTaskTrace {
	result := append([]SchedulerTaskTrace(nil), records...)
	mutate(&result[0])
	return result
}

func mutateResult(result SchedulerResult, mutate func(*SchedulerRow)) SchedulerResult {
	copy := result
	copy.Rows = append([]SchedulerRow(nil), result.Rows...)
	mutate(&copy.Rows[0])
	return copy
}
