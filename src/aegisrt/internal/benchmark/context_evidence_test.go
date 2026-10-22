package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestContextCanonicalArtifactsAreReplayableAndContainNoContextBody(t *testing.T) {
	evidence, err := SimulateContextEvidence(ContextPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	workload := evidence.CanonicalWorkloadJSON()
	if !json.Valid(workload) || bytes.Contains(workload, []byte(`"content":`)) ||
		bytes.Contains(workload, []byte("shared deterministic context")) || bytes.Contains(workload, []byte("large optional context")) {
		t.Fatalf("canonical workload contains raw Context text: %s", workload)
	}
	var bundle contextWorkloadBundle
	if err := json.Unmarshal(workload, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.ContentGenerator != contextContentGenerator || len(bundle.Cases) != 3 {
		t.Fatalf("workload replay contract = %#v", bundle)
	}
	var input contextInputContract
	if err := json.Unmarshal(evidence.CanonicalInputJSON(), &input); err != nil {
		t.Fatal(err)
	}
	if input.PlanID != contextPlanID || input.CounterName != contextCounterName || input.ProviderCacheMode != "none" ||
		input.WarmupPairs != 0 || len(input.Modes) != 6 || input.WorkloadSHA256 != evidence.Result.WorkloadSHA256 {
		t.Fatalf("context input contract = %#v", input)
	}
	other, err := SimulateContextEvidence(ContextPlan{Seeds: []uint32{8}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	if other.Result.WorkloadSHA256 == evidence.Result.WorkloadSHA256 {
		t.Fatal("seed did not change the canonical workload")
	}
	workload[0] ^= 0xff
	if hashBytes(evidence.CanonicalWorkloadJSON()) != evidence.Result.WorkloadSHA256 {
		t.Fatal("canonical workload accessor exposed mutable bytes")
	}
	traces := evidence.TrialTraceRecords()
	traces[0].Dispatches[0].Pages[0].PageID = strings.Repeat("f", 64)
	if evidence.TrialTraceRecords()[0].Dispatches[0].Pages[0].PageID == strings.Repeat("f", 64) {
		t.Fatal("context trace accessor exposed nested mutable state")
	}
}

func TestContextEvidenceRejectsTraceAndPairTampering(t *testing.T) {
	evidence, err := SimulateContextEvidence(ContextPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	traces := evidence.TrialTraceRecords()
	brokenStore := cloneContextTrialTrace(traces[0])
	brokenStore.Stores[0].PhysicalTokens++
	if _, err := contextRowFromTrace(brokenStore); err == nil {
		t.Fatal("inconsistent store counters were accepted")
	}
	brokenReuse := cloneContextTrialTrace(traces[0])
	brokenReuse.Dispatches[0].ReusedRefTokens = 1
	if _, err := contextRowFromTrace(brokenReuse); err == nil {
		t.Fatal("unexpected duplicate-reference tokens were accepted")
	}
	brokenACL := cloneContextTrialTrace(traces[0])
	brokenACL.ACLProbes[0].PageID = brokenACL.Dispatches[1].Pages[0].PageID
	if _, err := contextRowFromTrace(brokenACL); err == nil {
		t.Fatal("ACL probe against a non-owner page was accepted")
	}
	rows := append([]ContextRow(nil), evidence.Result.Rows...)
	for index := range traces {
		if traces[index].CaseID == "C1-smoke" && traces[index].Role == "treatment" {
			traces[index].Dispatches[0].TextSHA256 = strings.Repeat("a", 64)
			rows[index], err = contextRowFromTrace(traces[index])
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if err := validateContextEvidencePairs(rows, traces); err == nil {
		t.Fatal("C1 assembled-text drift was accepted")
	}
}

func TestContextSimulationCancellationAndPlanBounds(t *testing.T) {
	for _, plan := range []ContextPlan{
		{},
		{Seeds: []uint32{1}, Repetitions: 0},
		{Seeds: []uint32{1, 1}, Repetitions: 1},
		{Seeds: []uint32{1, 2}, Repetitions: 65},
	} {
		if _, err := SimulateContextEvidence(plan); err == nil {
			t.Fatalf("invalid context plan succeeded: %#v", plan)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	clockCalls := 0
	clock := func() time.Time {
		clockCalls++
		cancel()
		return time.Date(2026, 7, 20, 1, 2, 3, clockCalls, time.UTC)
	}
	_, err := simulateContextEvidenceContext(ctx, ContextPlan{Seeds: []uint32{1, 2}, Repetitions: 2}, clock)
	if !errors.Is(err, context.Canceled) || clockCalls != 1 {
		t.Fatalf("cancellation = %v, clock calls = %d", err, clockCalls)
	}
}
