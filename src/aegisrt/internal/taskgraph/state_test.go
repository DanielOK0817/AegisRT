package taskgraph

import (
	"reflect"
	"testing"

	"aegisrt/internal/domain"
)

func TestStateRoundTripPreservesVersionAndClones(t *testing.T) {
	graph, err := New(taskSpec("child", "root"), taskSpec("root"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Complete("root"); err != nil {
		t.Fatal(err)
	}

	state := graph.State()
	if state.Version != graph.Version() {
		t.Fatalf("state version = %d, want %d", state.Version, graph.Version())
	}
	if got := []string{state.Tasks[0].Spec.ID, state.Tasks[1].Spec.ID}; !reflect.DeepEqual(got, []string{"child", "root"}) {
		t.Fatalf("state task order = %v, want sorted ids", got)
	}

	restored, err := Restore(state)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.State(), state) {
		t.Fatalf("restored state = %#v, want %#v", restored.State(), state)
	}

	state.Tasks[0].Spec.Dependencies[0] = "changed"
	state.Tasks[0].Spec.Resources.ToolSlots["shell"] = 99
	view, _ := restored.Task("child")
	if view.Spec.Dependencies[0] != "root" || view.Spec.Resources.ToolSlots["shell"] != 1 {
		t.Fatal("Restore retained mutable fields owned by the caller")
	}

	exported := restored.State()
	exported.Tasks[0].Spec.Dependencies[0] = "changed-again"
	view, _ = restored.Task("child")
	if view.Spec.Dependencies[0] != "root" {
		t.Fatal("State exposed mutable graph fields")
	}
}

func TestRestoreRejectsInvalidState(t *testing.T) {
	root := TaskSnapshot{Spec: taskSpec("root"), Status: domain.TaskStatusSucceeded}
	child := TaskSnapshot{Spec: taskSpec("child", "root"), Status: domain.TaskStatusReady}
	tests := []struct {
		name  string
		state State
	}{
		{name: "tasks with zero version", state: State{Tasks: []TaskSnapshot{root}}},
		{name: "empty graph with nonzero version", state: State{Version: 1}},
		{name: "invalid spec", state: State{Version: 1, Tasks: []TaskSnapshot{{Spec: domain.TaskSpec{}, Status: domain.TaskStatusReady}}}},
		{name: "invalid status", state: State{Version: 1, Tasks: []TaskSnapshot{{Spec: taskSpec("root"), Status: "UNKNOWN"}}}},
		{name: "submitted is unreachable", state: State{Version: 1, Tasks: []TaskSnapshot{{Spec: taskSpec("root"), Status: domain.TaskStatusSubmitted}}}},
		{name: "duplicate id", state: State{Version: 1, Tasks: []TaskSnapshot{root, root}}},
		{name: "missing dependency", state: State{Version: 1, Tasks: []TaskSnapshot{{Spec: taskSpec("child", "missing"), Status: domain.TaskStatusWaitingDependency}}}},
		{name: "cycle", state: State{Version: 1, Tasks: []TaskSnapshot{
			{Spec: taskSpec("a", "b"), Status: domain.TaskStatusWaitingDependency},
			{Spec: taskSpec("b", "a"), Status: domain.TaskStatusWaitingDependency},
		}}},
		{name: "cross workflow", state: func() State {
			other := child
			other.Spec.WorkflowID = "other-workflow"
			return State{Version: 1, Tasks: []TaskSnapshot{root, other}}
		}()},
		{name: "ready with incomplete dependency", state: State{Version: 1, Tasks: []TaskSnapshot{
			{Spec: taskSpec("root"), Status: domain.TaskStatusFailed}, child,
		}}},
		{name: "recovering with incomplete dependency", state: State{Version: 1, Tasks: []TaskSnapshot{
			{Spec: taskSpec("root"), Status: domain.TaskStatusFailed},
			{Spec: taskSpec("child", "root"), Status: domain.TaskStatusRecovering},
		}}},
		{name: "waiting with complete dependencies", state: State{Version: 1, Tasks: []TaskSnapshot{
			root, {Spec: taskSpec("child", "root"), Status: domain.TaskStatusWaitingDependency},
		}}},
		{name: "root waiting", state: State{Version: 1, Tasks: []TaskSnapshot{
			{Spec: taskSpec("root"), Status: domain.TaskStatusWaitingDependency},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if graph, err := Restore(test.state); err == nil || graph != nil {
				t.Fatalf("Restore() = (%v, %v), want nil graph and error", graph, err)
			}
		})
	}
}

func TestRestoreAllowsTerminalTaskWithIncompleteDependencies(t *testing.T) {
	state := State{Version: 4, Tasks: []TaskSnapshot{
		{Spec: taskSpec("root"), Status: domain.TaskStatusFailed},
		{Spec: taskSpec("child", "root"), Status: domain.TaskStatusCanceled},
	}}
	graph, err := Restore(state)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Version() != 4 {
		t.Fatalf("version = %d, want 4", graph.Version())
	}
}
