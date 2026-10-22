package domain

import "testing"

func TestTaskStatusValidationAndTransitions(t *testing.T) {
	statuses := []TaskStatus{
		TaskStatusSubmitted, TaskStatusWaitingDependency, TaskStatusReady,
		TaskStatusAdmitted, TaskStatusRunning, TaskStatusSucceeded,
		TaskStatusFailed, TaskStatusLost, TaskStatusRecovering, TaskStatusCanceled,
	}
	for _, status := range statuses {
		if err := status.Validate(); err != nil {
			t.Errorf("status %q should be valid: %v", status, err)
		}
	}
	if err := TaskStatus("UNKNOWN").Validate(); err == nil {
		t.Fatal("unknown status should fail validation")
	}

	tests := []struct {
		name string
		from TaskStatus
		to   TaskStatus
		want bool
	}{
		{name: "dependency becomes ready", from: TaskStatusWaitingDependency, to: TaskStatusReady, want: true},
		{name: "ready admitted", from: TaskStatusReady, to: TaskStatusAdmitted, want: true},
		{name: "running succeeds", from: TaskStatusRunning, to: TaskStatusSucceeded, want: true},
		{name: "lost recovers", from: TaskStatusLost, to: TaskStatusRecovering, want: true},
		{name: "recovering requeues", from: TaskStatusRecovering, to: TaskStatusReady, want: true},
		{name: "skip admission", from: TaskStatusReady, to: TaskStatusRunning},
		{name: "terminal immutable", from: TaskStatusSucceeded, to: TaskStatusReady},
		{name: "same state", from: TaskStatusRunning, to: TaskStatusRunning},
		{name: "unknown target", from: TaskStatusRunning, to: TaskStatus("UNKNOWN")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.from.CanTransitionTo(test.to); got != test.want {
				t.Fatalf("CanTransitionTo() = %v, want %v", got, test.want)
			}
		})
	}
	if !TaskStatusSucceeded.IsTerminal() || TaskStatusLost.IsTerminal() {
		t.Fatal("task terminal-state classification is incorrect")
	}
}

func TestAttemptStatusValidation(t *testing.T) {
	for _, status := range []AttemptStatus{
		AttemptStatusPending, AttemptStatusAdmitted, AttemptStatusRunning,
		AttemptStatusSucceeded, AttemptStatusFailed, AttemptStatusLost, AttemptStatusCanceled,
	} {
		if err := status.Validate(); err != nil {
			t.Errorf("status %q should be valid: %v", status, err)
		}
	}
	if AttemptStatusPending.IsTerminal() || !AttemptStatusLost.IsTerminal() {
		t.Fatal("attempt terminal-state classification is incorrect")
	}
	if err := AttemptStatus("").Validate(); err == nil {
		t.Fatal("empty status should fail validation")
	}
}
