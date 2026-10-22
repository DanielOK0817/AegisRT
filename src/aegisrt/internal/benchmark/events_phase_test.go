package benchmark

import (
	"strings"
	"testing"
	"time"
)

func TestBuildEventsPreservesWarmupTrialPhase(t *testing.T) {
	started := time.Unix(100, 0)
	trials := []trialObservation{
		{
			Phase: "warmup", CaseID: "F4", PairID: "warmup-1", Seed: 104701,
			StartedAt: started.Add(time.Second), FinishedAt: started.Add(2 * time.Second),
		},
		{
			Phase: "measurement", CaseID: "F4", PairID: "measured-1", Seed: 104729,
			StartedAt: started.Add(3 * time.Second), FinishedAt: started.Add(4 * time.Second),
		},
	}
	events := buildEvents("run-0123456789abcdef01234567", "fault-kernel", trials, started, started.Add(5*time.Second))
	if len(events) != 7 {
		t.Fatalf("events = %d, want 7", len(events))
	}
	for _, index := range []int{2, 3} {
		if events[index].Phase != "warmup" {
			t.Fatalf("event %d phase = %q, want warmup", index, events[index].Phase)
		}
	}
	for _, index := range []int{4, 5} {
		if events[index].Phase != "measurement" {
			t.Fatalf("event %d phase = %q, want measurement", index, events[index].Phase)
		}
	}
}

func TestBuildEventsDefaultsExistingTrialsToMeasurement(t *testing.T) {
	started := time.Unix(100, 0)
	events := buildEvents("run-0123456789abcdef01234567", "fault", []trialObservation{{
		CaseID: "F1-smoke", PairID: "pair-1", StartedAt: started, FinishedAt: started.Add(time.Second),
	}}, started, started.Add(2*time.Second))
	if events[2].Phase != "measurement" || events[3].Phase != "measurement" {
		t.Fatalf("legacy trial phases = %q/%q", events[2].Phase, events[3].Phase)
	}
}

func TestBuildEventsUsesRealTrialStatusButCompletesCollection(t *testing.T) {
	started := time.Unix(100, 0)
	events := buildEvents("run-0123456789abcdef01234567", "fault-kernel", []trialObservation{
		{Status: "FAILED", CaseID: "F4", PairID: "pair-1", StartedAt: started, FinishedAt: started.Add(time.Second)},
		{Status: "TIMED_OUT", CaseID: "F4", PairID: "pair-2", StartedAt: started, FinishedAt: started.Add(2 * time.Second)},
	}, started, started.Add(3*time.Second))
	if events[3].Status != "FAILED" || events[5].Status != "TIMED_OUT" {
		t.Fatalf("trial completion statuses=%q/%q", events[3].Status, events[5].Status)
	}
	if terminal := events[len(events)-1]; terminal.EventType != "run.completed" || terminal.Status != "SUCCEEDED" {
		t.Fatalf("collection terminal event=%#v", terminal)
	}
}

func TestExperimentEvidenceRejectsUnknownTrialStatus(t *testing.T) {
	evidence := experimentEvidence{
		Kind: "fault-kernel", PlanID: "plan", EvidenceScope: "scope", CaseIDs: []string{"F4"},
		WorkloadSHA256: strings.Repeat("a", 64), InputSHA256: strings.Repeat("b", 64),
		Artifacts: []artifactPayload{{Name: "data.json", Kind: "data", MediaType: "application/json", Records: 1, Data: []byte("{}")}},
		Trials:    []trialObservation{{Status: "SECRET"}}, RowCount: 1,
	}
	if err := validateExperimentEvidence(evidence); err == nil {
		t.Fatal("unknown trial status was accepted for event serialization")
	}
}
