package benchmark

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPrepareContextEvidenceRejectsMissingOrConflictingRecords(t *testing.T) {
	const runID = "run-1234567890abcdef12345678"
	evidence, err := SimulateContextEvidence(ContextPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	valid := evidence.TrialTraceRecords()
	if _, _, err := prepareContextEvidence(runID, evidence.Result, valid); err != nil {
		t.Fatalf("valid context evidence rejected: %v", err)
	}
	tests := []struct {
		name   string
		traces []ContextTrialTrace
	}{
		{name: "missing trace", traces: valid[1:]},
		{name: "conflicting run", traces: mutateContextTrace(valid, func(trace *ContextTrialTrace) { trace.RunID = "run-aaaaaaaaaaaaaaaaaaaaaaaa" })},
		{name: "duplicate trial", traces: mutateContextTrace(valid, func(trace *ContextTrialTrace) { *trace = cloneContextTrialTrace(valid[1]) })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := prepareContextEvidence(runID, evidence.Result, test.traces); err == nil {
				t.Fatal("invalid context evidence was accepted")
			}
		})
	}
}

func TestValidateCanonicalContextInputsRejectsHashDriftAndRawContent(t *testing.T) {
	evidence, err := SimulateContextEvidence(ContextPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCanonicalContextInputs(evidence.Result, evidence.CanonicalWorkloadJSON(), evidence.CanonicalInputJSON()); err != nil {
		t.Fatal(err)
	}
	drifted := append(evidence.CanonicalWorkloadJSON(), ' ')
	if err := validateCanonicalContextInputs(evidence.Result, drifted, evidence.CanonicalInputJSON()); err == nil {
		t.Fatal("canonical workload byte drift was accepted")
	}
	raw := []byte(`{"content":"synthetic but disallowed"}`)
	result := evidence.Result
	result.WorkloadSHA256 = hashBytes(raw)
	if err := validateCanonicalContextInputs(result, raw, evidence.CanonicalInputJSON()); err == nil {
		t.Fatal("raw Context body field was accepted")
	}
}

func TestContextTracePagesAreBoundToCanonicalWorkload(t *testing.T) {
	evidence, err := SimulateContextEvidence(ContextPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	traces := evidence.TrialTraceRecords()
	if err := validateContextTracesAgainstWorkload(context.Background(), traces, evidence.CanonicalWorkloadJSON()); err != nil {
		t.Fatalf("valid trace binding rejected: %v", err)
	}
	for index := range traces {
		if traces[index].CaseID == "C2-smoke" {
			traces[index].Dispatches[0].Pages[0].ContentSHA256 = strings.Repeat("f", 64)
			break
		}
	}
	if err := validateContextTracesAgainstWorkload(context.Background(), traces, evidence.CanonicalWorkloadJSON()); err == nil {
		t.Fatal("trace content hash drift from canonical workload was accepted")
	}
}

func TestMarshalContextTrialTracesContainsNoRawContextOrArbitraryErrors(t *testing.T) {
	evidence, err := SimulateContextEvidence(ContextPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	data, err := marshalContextTrialTraces(evidence.TrialTraceRecords())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "shared deterministic context") || strings.Contains(string(data), "large optional context") {
		t.Fatal("trace contains raw Context body")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatal(err)
		}
		if _, exists := decoded["error"]; exists {
			t.Fatal("trace contains an arbitrary error field")
		}
	}
}

func mutateContextTrace(records []ContextTrialTrace, mutate func(*ContextTrialTrace)) []ContextTrialTrace {
	result := make([]ContextTrialTrace, len(records))
	for index := range records {
		result[index] = cloneContextTrialTrace(records[index])
	}
	mutate(&result[0])
	return result
}
