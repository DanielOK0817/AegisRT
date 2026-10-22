package scheduler

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFIFOFiltersInfeasibleCandidates(t *testing.T) {
	now := time.Unix(100, 0)
	candidates := []Candidate{
		{TaskID: "too-large", WorkflowID: "w", ReadyAt: now.Add(-time.Minute), Resources: ResourceVector{CPUMilli: 2000}},
		{TaskID: "fits", WorkflowID: "w", ReadyAt: now, Resources: ResourceVector{CPUMilli: 100}},
	}
	decision, err := (FIFO{}).Select(candidates, ResourceVector{CPUMilli: 1000}, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Candidate.TaskID != "fits" {
		t.Fatalf("selected %q, want fits", decision.Candidate.TaskID)
	}
}

func TestPriorityUsesDAGSignalsAndAging(t *testing.T) {
	now := time.Unix(1000, 0)
	policy := Priority{AgingInterval: 10 * time.Second}
	candidates := []Candidate{
		{TaskID: "new-high", WorkflowID: "w", Priority: 5, ReadyAt: now, Resources: ResourceVector{CPUMilli: 1}},
		{TaskID: "old", WorkflowID: "w", Priority: 1, ReadyAt: now.Add(-5 * time.Minute), Resources: ResourceVector{CPUMilli: 1}},
	}
	decision, err := policy.Select(candidates, ResourceVector{CPUMilli: 10}, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Candidate.TaskID != "old" {
		t.Fatalf("selected %q, want aged task", decision.Candidate.TaskID)
	}
}

func TestDAGDRCFSFairnessAcrossWorkflows(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000}
	policy, err := NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	first, err := policy.Select([]Candidate{
		{TaskID: "a1", WorkflowID: "a", ReadyAt: now, Resources: ResourceVector{CPUMilli: 500}},
		{TaskID: "b1", WorkflowID: "b", ReadyAt: now, Resources: ResourceVector{CPUMilli: 500}},
	}, capacity, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Candidate.WorkflowID != "a" {
		t.Fatalf("first workflow = %q, want deterministic a", first.Candidate.WorkflowID)
	}
	if err := policy.Account("a", ResourceVector{CPUMilli: 500}, 0); err != nil {
		t.Fatal(err)
	}
	second, err := policy.Select([]Candidate{
		{TaskID: "a2", WorkflowID: "a", ReadyAt: now, Resources: ResourceVector{CPUMilli: 500}},
		{TaskID: "b1", WorkflowID: "b", ReadyAt: now, Resources: ResourceVector{CPUMilli: 500}},
	}, capacity, now)
	if err != nil {
		t.Fatal(err)
	}
	if second.Candidate.WorkflowID != "b" {
		t.Fatalf("second workflow = %q, want b after accounting", second.Candidate.WorkflowID)
	}
	if !strings.Contains(second.Reason, "minimum vruntime") {
		t.Fatalf("decision reason is not explanatory: %q", second.Reason)
	}
}

func TestDAGDRCFSIsDeterministic(t *testing.T) {
	policy, err := NewDAGDRCFS(ResourceVector{CPUMilli: 10}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	candidates := []Candidate{
		{TaskID: "z", WorkflowID: "w", ReadyAt: now, Resources: ResourceVector{CPUMilli: 1}},
		{TaskID: "a", WorkflowID: "w", ReadyAt: now, Resources: ResourceVector{CPUMilli: 1}},
	}
	for range 10 {
		decision, selectErr := policy.Select(candidates, ResourceVector{CPUMilli: 10}, now)
		if selectErr != nil {
			t.Fatal(selectErr)
		}
		if decision.Candidate.TaskID != "a" {
			t.Fatalf("selected %q, want stable task a", decision.Candidate.TaskID)
		}
	}
}

func TestPolicyReturnsNoCandidateWhenResourcesAreExhausted(t *testing.T) {
	_, err := (FIFO{}).Select([]Candidate{
		{TaskID: "task", WorkflowID: "w", Resources: ResourceVector{CPUMilli: 2}},
	}, ResourceVector{CPUMilli: 1}, time.Now())
	if !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("error = %v, want ErrNoCandidate", err)
	}
}
