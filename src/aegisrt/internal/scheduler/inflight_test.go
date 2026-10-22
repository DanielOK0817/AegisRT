package scheduler

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestInFlightReservationsAffectFairnessAndSurviveRestore(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000}
	policy, _ := NewDAGDRCFS(capacity, time.Second)
	engine, _ := NewEngine(policy, capacity)
	now := time.Unix(100, 0)
	a1 := Candidate{TaskID: "a1", WorkflowID: "a", Resources: ResourceVector{CPUMilli: 250}}
	a2 := Candidate{TaskID: "a2", WorkflowID: "a", Resources: ResourceVector{CPUMilli: 250}}
	b1 := Candidate{TaskID: "b1", WorkflowID: "b", Resources: ResourceVector{CPUMilli: 250}}
	if _, err := engine.DecideAndReserve([]Candidate{a1}, now); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	for _, restore := range []bool{false, true} {
		if restore {
			policy, _ = NewDAGDRCFS(capacity, time.Second)
			engine, _ = NewEngine(policy, capacity)
			if err := engine.Restore(state); err != nil {
				t.Fatal(err)
			}
		}
		decision, err := engine.DecideAndReserve([]Candidate{a2, b1}, now)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Candidate.TaskID != "b1" {
			t.Fatalf("restore=%v: selected %s while a1 is still in flight", restore, decision.Candidate.TaskID)
		}
		if err := engine.Complete("b1", ResourceVector{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := engine.Complete("a1", ResourceVector{CPUMilli: 125}); err != nil {
		t.Fatal(err)
	}
	if got := policy.VRuntime("a"); got != 0.125 {
		t.Fatalf("settled charge = %v; reservation must not be charged twice", got)
	}
	if err := engine.Complete("a1", ResourceVector{}); !errors.Is(err, ErrNotReserved) {
		t.Fatalf("duplicate release: %v", err)
	}
	if !engine.Allocated().IsZero() {
		t.Fatal("reservation leaked")
	}
}

func TestInFlightFairnessAcrossFourSlotsAndDynamicArrival(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000}
	policy, _ := NewDAGDRCFS(capacity, time.Second)
	engine, _ := NewEngine(policy, capacity)
	var candidates []Candidate
	for _, workflow := range []string{"a", "b"} {
		for i := 0; i < 4; i++ {
			candidates = append(candidates, Candidate{TaskID: fmt.Sprintf("%s%d", workflow, i), WorkflowID: workflow, Resources: ResourceVector{CPUMilli: 250}})
		}
	}
	selected := make(map[string]int)
	for range 4 {
		decision, err := engine.DecideAndReserve(candidates, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		selected[decision.Candidate.WorkflowID]++
		for i, candidate := range candidates {
			if candidate.TaskID == decision.Candidate.TaskID {
				candidates = append(candidates[:i], candidates[i+1:]...)
				break
			}
		}
	}
	if selected["a"] != 2 || selected["b"] != 2 {
		t.Fatalf("four concurrent admissions=%v, want a:2 b:2", selected)
	}
	if err := engine.Complete("a0", ResourceVector{CPUMilli: 250}); err != nil {
		t.Fatal(err)
	}
	candidates = append(candidates, Candidate{TaskID: "c0", WorkflowID: "c", Resources: ResourceVector{CPUMilli: 250}})
	decision, err := engine.DecideAndReserve(candidates, time.Now())
	if err != nil || decision.Candidate.TaskID != "c0" {
		t.Fatalf("new workflow admission=%+v err=%v", decision, err)
	}
	if engine.Allocated().CPUMilli != 1000 {
		t.Fatal("dynamic admission changed capacity invariant")
	}
}
