package taskgraph

import (
	"errors"
	"reflect"
	"testing"

	"aegisrt/internal/domain"
)

func TestTransitionLifecycleAndUnlock(t *testing.T) {
	graph, err := New(taskSpec("root"), taskSpec("child", "root"))
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		status domain.TaskStatus
		want   []string
	}{
		{status: domain.TaskStatusAdmitted},
		{status: domain.TaskStatusRunning},
		{status: domain.TaskStatusSucceeded, want: []string{"child"}},
	}
	for _, step := range steps {
		unlocked, err := graph.Transition("root", step.status)
		if err != nil {
			t.Fatalf("Transition(%s) error = %v", step.status, err)
		}
		if got := taskIDs(unlocked); !reflect.DeepEqual(got, step.want) {
			t.Fatalf("Transition(%s) unlocked = %v, want %v", step.status, got, step.want)
		}
	}
}

func TestTransitionRejectsInvalidMoves(t *testing.T) {
	tests := []struct {
		name   string
		taskID string
		next   domain.TaskStatus
		want   error
	}{
		{name: "skip admitted", taskID: "root", next: domain.TaskStatusRunning, want: ErrInvalidTransition},
		{name: "dependency not done", taskID: "child", next: domain.TaskStatusReady, want: ErrDependenciesIncomplete},
		{name: "unknown status", taskID: "root", next: domain.TaskStatus("UNKNOWN"), want: ErrInvalidTransition},
		{name: "unknown task", taskID: "missing", next: domain.TaskStatusAdmitted, want: ErrTaskNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			graph, err := New(taskSpec("root"), taskSpec("child", "root"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := graph.Transition(test.taskID, test.next); !errors.Is(err, test.want) {
				t.Fatalf("Transition() error = %v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}

func TestLostTaskCanRecoverToReady(t *testing.T) {
	graph, err := New(taskSpec("root"))
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []domain.TaskStatus{
		domain.TaskStatusAdmitted,
		domain.TaskStatusRunning,
		domain.TaskStatusLost,
		domain.TaskStatusRecovering,
		domain.TaskStatusReady,
	} {
		if _, err := graph.Transition("root", next); err != nil {
			t.Fatalf("Transition(%s) error = %v", next, err)
		}
	}
	if got := taskIDs(graph.ReadyTasks()); !reflect.DeepEqual(got, []string{"root"}) {
		t.Fatalf("ready tasks = %v, want root", got)
	}
}

func TestCompleteRejectsBlockedAndFailedTasks(t *testing.T) {
	graph, err := New(taskSpec("root"), taskSpec("child", "root"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Complete("child"); !errors.Is(err, ErrDependenciesIncomplete) {
		t.Fatalf("blocked completion error = %v", err)
	}
	if _, err := graph.Transition("root", domain.TaskStatusFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Complete("root"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("failed completion error = %v", err)
	}
}
