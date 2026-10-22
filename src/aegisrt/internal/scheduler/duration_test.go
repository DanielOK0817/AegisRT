package scheduler

import (
	"testing"
	"time"
)

func TestDurationAccountingDistinguishesLongAndShortReservations(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000}
	p, err := NewDurationDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := NewEngine(p, capacity)
	start := time.Unix(100, 0)
	for _, tc := range []struct {
		id       string
		duration time.Duration
	}{{"short", time.Second}, {"long", 10 * time.Second}} {
		c := Candidate{TaskID: tc.id, WorkflowID: tc.id, Resources: capacity}
		if _, err := e.DecideAndReserve([]Candidate{c}, start); err != nil {
			t.Fatal(err)
		}
		if err := e.MarkStarted(tc.id, start); err != nil {
			t.Fatal(err)
		}
		if err := e.CompleteAt(tc.id, capacity, start.Add(tc.duration)); err != nil {
			t.Fatal(err)
		}
	}
	if p.VRuntime("long") != 10*p.VRuntime("short") || p.VRuntime("short") != 1 {
		t.Fatalf("long=%v short=%v", p.VRuntime("long"), p.VRuntime("short"))
	}
}

func TestDurationAccountingRestoresStartAndRejectsBackwardClock(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000}
	p, _ := NewDurationDAGDRCFS(capacity, time.Second)
	e, _ := NewEngine(p, capacity)
	start := time.Unix(100, 0)
	_, _ = e.DecideAndReserve([]Candidate{{TaskID: "task", WorkflowID: "flow", Resources: capacity}}, start)
	if err := e.MarkStarted("task", start); err != nil {
		t.Fatal(err)
	}
	saved := e.State()
	restoredPolicy, _ := NewDurationDAGDRCFS(capacity, time.Second)
	restored, _ := NewEngine(restoredPolicy, capacity)
	if err := restored.Restore(saved); err != nil {
		t.Fatal(err)
	}
	if err := restored.CompleteAt("task", capacity, start.Add(-time.Second)); err == nil {
		t.Fatal("accepted backward accounting clock")
	}
	if restored.Allocated().IsZero() {
		t.Fatal("failed settlement released reservation")
	}
	if err := restored.CompleteAt("task", capacity, start.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if restoredPolicy.VRuntime("flow") != 3 {
		t.Fatal(restoredPolicy.VRuntime("flow"))
	}
	if err := restored.CompleteAt("task", capacity, start.Add(4*time.Second)); err == nil {
		t.Fatal("duplicate settlement")
	}
}

func TestDurationUnstartedReservationHasNoExecutedCharge(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000}
	p, _ := NewDurationDAGDRCFS(capacity, time.Second)
	e, _ := NewEngine(p, capacity)
	start := time.Unix(100, 0)
	_, _ = e.DecideAndReserve([]Candidate{{TaskID: "task", WorkflowID: "flow", Resources: capacity}}, start)
	if err := e.CompleteAt("task", capacity, start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p.VRuntime("flow") != 0 {
		t.Fatal("unstarted attempt charged")
	}
}

func TestDurationSelectionAcceptsQueuedObservationBeforeConcurrentStart(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000}
	p, _ := NewDurationDAGDRCFS(capacity, time.Second)
	e, _ := NewEngine(p, capacity)
	start := time.Unix(100, 0)
	first := Candidate{TaskID: "first", WorkflowID: "flow", Resources: ResourceVector{CPUMilli: 250}}
	if _, err := e.DecideAndReserve([]Candidate{first}, start); err != nil {
		t.Fatal(err)
	}
	if err := e.MarkStarted("first", start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	second := Candidate{TaskID: "second", WorkflowID: "other", Resources: first.Resources}
	if _, err := e.DecideAndReserve([]Candidate{second}, start); err != nil {
		t.Fatal(err)
	}
}
