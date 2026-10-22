package scheduler

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestEngineStateRoundTripClonesReservationsAndFairness(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000, ToolSlots: map[string]int64{"shell": 2}}
	policy, err := NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(policy, capacity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	for _, candidate := range []Candidate{
		{TaskID: "z", WorkflowID: "workflow-z", ReadyAt: now, Resources: ResourceVector{CPUMilli: 200, ToolSlots: map[string]int64{"shell": 1}}},
		{TaskID: "a", WorkflowID: "workflow-a", ReadyAt: now, Resources: ResourceVector{CPUMilli: 300}},
	} {
		if _, err := engine.DecideAndReserve([]Candidate{candidate}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := policy.Account("workflow-a", ResourceVector{CPUMilli: 400}, 1); err != nil {
		t.Fatal(err)
	}

	state := engine.State()
	if got := []string{state.Reservations[0].TaskID, state.Reservations[1].TaskID}; !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatalf("reservation order = %v, want sorted ids", got)
	}
	if got := state.PolicyState.WorkflowVRuntime[0]; got.WorkflowID != "workflow-a" || got.VRuntime != 0.2 {
		t.Fatalf("policy state = %#v, want workflow-a vruntime 0.2", state.PolicyState)
	}

	restoredPolicy, _ := NewDAGDRCFS(capacity, time.Second)
	restored, _ := NewEngine(restoredPolicy, capacity)
	if err := restored.Restore(state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.State(), state) {
		t.Fatalf("restored state = %#v, want %#v", restored.State(), state)
	}

	state.Capacity.ToolSlots["shell"] = 99
	state.Reservations[1].Resources.ToolSlots["shell"] = 99
	state.PolicyState.WorkflowVRuntime[0].VRuntime = 99
	view := restored.State()
	if view.Capacity.ToolSlots["shell"] != 2 || view.Reservations[1].Resources.ToolSlots["shell"] != 1 ||
		view.PolicyState.WorkflowVRuntime[0].VRuntime != 0.2 {
		t.Fatal("Restore retained mutable state owned by the caller")
	}
}

func TestEngineRestoreRejectsInvalidStateTransactionally(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000}
	policy, _ := NewDAGDRCFS(capacity, time.Second)
	engine, _ := NewEngine(policy, capacity)
	reserved := Candidate{TaskID: "original", WorkflowID: "workflow", Resources: ResourceVector{CPUMilli: 100}}
	if _, err := engine.DecideAndReserve([]Candidate{reserved}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := policy.Account("workflow", ResourceVector{CPUMilli: 100}, 0); err != nil {
		t.Fatal(err)
	}
	before := engine.State()
	valid := State{
		PolicyName: "dag-drcfs",
		Capacity:   capacity,
		Allocated:  ResourceVector{CPUMilli: 200},
		Reservations: []Candidate{{
			TaskID: "replacement", WorkflowID: "workflow-2", Resources: ResourceVector{CPUMilli: 200},
		}},
		PolicyState: PolicyState{WorkflowVRuntime: []WorkflowVRuntime{{WorkflowID: "workflow-2", VRuntime: 0.25}}},
	}
	tests := []struct {
		name   string
		mutate func(*State)
	}{
		{name: "policy mismatch", mutate: func(s *State) { s.PolicyName = "fifo" }},
		{name: "capacity mismatch", mutate: func(s *State) { s.Capacity.CPUMilli = 999 }},
		{name: "negative allocated", mutate: func(s *State) { s.Allocated.CPUMilli = -1 }},
		{name: "allocated does not equal reservations", mutate: func(s *State) { s.Allocated.CPUMilli = 201 }},
		{name: "reservation exceeds capacity", mutate: func(s *State) {
			s.Reservations[0].Resources.CPUMilli = 1001
			s.Allocated.CPUMilli = 1001
		}},
		{name: "invalid reservation", mutate: func(s *State) { s.Reservations[0].WorkflowID = "" }},
		{name: "duplicate reservation", mutate: func(s *State) { s.Reservations = append(s.Reservations, s.Reservations[0]) }},
		{name: "duplicate workflow runtime", mutate: func(s *State) {
			s.PolicyState.WorkflowVRuntime = append(s.PolicyState.WorkflowVRuntime, s.PolicyState.WorkflowVRuntime[0])
		}},
		{name: "negative workflow runtime", mutate: func(s *State) { s.PolicyState.WorkflowVRuntime[0].VRuntime = -1 }},
		{name: "nan workflow runtime", mutate: func(s *State) { s.PolicyState.WorkflowVRuntime[0].VRuntime = math.NaN() }},
		{name: "infinite workflow runtime", mutate: func(s *State) { s.PolicyState.WorkflowVRuntime[0].VRuntime = math.Inf(1) }},
		{name: "blank workflow id", mutate: func(s *State) { s.PolicyState.WorkflowVRuntime[0].WorkflowID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cloneSchedulerState(valid)
			test.mutate(&state)
			if err := engine.Restore(state); err == nil {
				t.Fatal("Restore() error = nil, want rejection")
			}
			if got := engine.State(); !reflect.DeepEqual(got, before) {
				t.Fatalf("failed restore mutated engine: got %#v, want %#v", got, before)
			}
		})
	}
}

func TestEngineRestoreRejectsPolicyStateForStatelessPolicy(t *testing.T) {
	engine, _ := NewEngine(FIFO{}, ResourceVector{CPUMilli: 1000})
	state := engine.State()
	state.PolicyState.WorkflowVRuntime = []WorkflowVRuntime{{WorkflowID: "workflow", VRuntime: 1}}
	if err := engine.Restore(state); err == nil {
		t.Fatal("Restore() accepted fairness state for FIFO")
	}
}

func TestDAGDRCFSAccountRejectsNonFiniteVRuntime(t *testing.T) {
	policy, err := NewDAGDRCFS(ResourceVector{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Account("workflow", ResourceVector{CPUMilli: 1}, 0); err == nil {
		t.Fatal("Account() accepted usage that produces infinite vruntime")
	}
	if got := policy.FairnessState(); len(got.WorkflowVRuntime) != 0 {
		t.Fatalf("rejected accounting mutated fairness state: %#v", got)
	}
}

func cloneSchedulerState(state State) State {
	clone := state
	clone.Capacity = state.Capacity.Clone()
	clone.Allocated = state.Allocated.Clone()
	clone.Reservations = make([]Candidate, len(state.Reservations))
	for i, reservation := range state.Reservations {
		clone.Reservations[i] = reservation
		clone.Reservations[i].Resources = reservation.Resources.Clone()
	}
	clone.PolicyState.WorkflowVRuntime = append([]WorkflowVRuntime(nil), state.PolicyState.WorkflowVRuntime...)
	return clone
}
