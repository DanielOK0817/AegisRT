package scheduler

import (
	"errors"
	"testing"
	"time"
)

var errAccountingRejected = errors.New("accounting rejected")

func TestEnginePreventsResourceOvercommitAndReleases(t *testing.T) {
	engine, err := NewEngine(FIFO{}, ResourceVector{CPUMilli: 1000})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	first := Candidate{TaskID: "one", WorkflowID: "w", ReadyAt: now, Resources: ResourceVector{CPUMilli: 700}}
	second := Candidate{TaskID: "two", WorkflowID: "w", ReadyAt: now, Resources: ResourceVector{CPUMilli: 700}}
	if _, err = engine.DecideAndReserve([]Candidate{first}, now); err != nil {
		t.Fatal(err)
	}
	if got := engine.Allocated().CPUMilli; got != 700 {
		t.Fatalf("allocated CPU = %d, want 700", got)
	}
	if _, err = engine.DecideAndReserve([]Candidate{second}, now); !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("second reservation error = %v, want ErrNoCandidate", err)
	}
	if err = engine.Complete("one", ResourceVector{CPUMilli: 650}); err != nil {
		t.Fatal(err)
	}
	if got := engine.Allocated().CPUMilli; got != 0 {
		t.Fatalf("allocated CPU after completion = %d, want 0", got)
	}
	if _, err = engine.DecideAndReserve([]Candidate{second}, now); err != nil {
		t.Fatalf("reservation after release failed: %v", err)
	}
}

func TestEngineAccountsActualUsage(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000}
	policy, err := NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(policy, capacity)
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{TaskID: "one", WorkflowID: "w", Priority: 1, Resources: ResourceVector{CPUMilli: 500}}
	if _, err = engine.DecideAndReserve([]Candidate{candidate}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = engine.Complete("one", ResourceVector{CPUMilli: 400}); err != nil {
		t.Fatal(err)
	}
	if got := policy.VRuntime("w"); got != 0.2 {
		t.Fatalf("vruntime = %v, want 0.2", got)
	}
}

func TestEngineRetainsReservationWhenAccountingFails(t *testing.T) {
	policy := &rejectingAccountingPolicy{reject: true}
	engine, err := NewEngine(policy, ResourceVector{CPUMilli: 1000})
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{
		TaskID: "one", WorkflowID: "workflow-1",
		Resources: ResourceVector{CPUMilli: 400},
	}
	if _, err := engine.DecideAndReserve([]Candidate{candidate}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Complete(candidate.TaskID, candidate.Resources); !errors.Is(err, errAccountingRejected) {
		t.Fatalf("Complete error = %v, want accounting rejection", err)
	}
	if got := engine.Allocated().CPUMilli; got != 400 {
		t.Fatalf("failed accounting released reservation: allocated=%d", got)
	}
	policy.reject = false
	if err := engine.Complete(candidate.TaskID, candidate.Resources); err != nil {
		t.Fatalf("reservation could not be completed after retry: %v", err)
	}
	if got := engine.Allocated().CPUMilli; got != 0 {
		t.Fatalf("allocated CPU after retry = %d", got)
	}
}

type rejectingAccountingPolicy struct {
	reject bool
}

func (*rejectingAccountingPolicy) Name() string { return "rejecting-accounting" }

func (*rejectingAccountingPolicy) Select(candidates []Candidate, available ResourceVector, now time.Time) (Decision, error) {
	return (FIFO{}).Select(candidates, available, now)
}

func (p *rejectingAccountingPolicy) Account(string, ResourceVector, int) error {
	if p.reject {
		return errAccountingRejected
	}
	return nil
}
