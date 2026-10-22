package domain

import "fmt"

type TaskStatus string

const (
	TaskStatusSubmitted         TaskStatus = "SUBMITTED"
	TaskStatusWaitingDependency TaskStatus = "WAITING_DEPENDENCY"
	TaskStatusReady             TaskStatus = "READY"
	TaskStatusAdmitted          TaskStatus = "ADMITTED"
	TaskStatusRunning           TaskStatus = "RUNNING"
	TaskStatusSucceeded         TaskStatus = "SUCCEEDED"
	TaskStatusFailed            TaskStatus = "FAILED"
	TaskStatusLost              TaskStatus = "LOST"
	TaskStatusRecovering        TaskStatus = "RECOVERING"
	TaskStatusCanceled          TaskStatus = "CANCELED"
)

func (s TaskStatus) Validate() error {
	if !s.IsValid() {
		return fmt.Errorf("invalid task status %q", s)
	}
	return nil
}

func (s TaskStatus) IsValid() bool {
	switch s {
	case TaskStatusSubmitted, TaskStatusWaitingDependency, TaskStatusReady,
		TaskStatusAdmitted, TaskStatusRunning, TaskStatusSucceeded,
		TaskStatusFailed, TaskStatusLost, TaskStatusRecovering, TaskStatusCanceled:
		return true
	default:
		return false
	}
}

func (s TaskStatus) IsTerminal() bool {
	return s == TaskStatusSucceeded || s == TaskStatusFailed || s == TaskStatusCanceled
}

func (s TaskStatus) CanTransitionTo(next TaskStatus) bool {
	if !s.IsValid() || !next.IsValid() || s == next {
		return false
	}
	switch s {
	case TaskStatusSubmitted:
		return next == TaskStatusWaitingDependency || next == TaskStatusReady ||
			next == TaskStatusFailed || next == TaskStatusCanceled
	case TaskStatusWaitingDependency:
		return next == TaskStatusReady || next == TaskStatusFailed || next == TaskStatusCanceled
	case TaskStatusReady:
		return next == TaskStatusAdmitted || next == TaskStatusFailed || next == TaskStatusCanceled
	case TaskStatusAdmitted:
		return next == TaskStatusRunning || next == TaskStatusReady ||
			next == TaskStatusFailed || next == TaskStatusLost || next == TaskStatusCanceled
	case TaskStatusRunning:
		return next == TaskStatusSucceeded || next == TaskStatusFailed ||
			next == TaskStatusLost || next == TaskStatusCanceled
	case TaskStatusFailed, TaskStatusLost:
		return next == TaskStatusRecovering || next == TaskStatusCanceled
	case TaskStatusRecovering:
		return next == TaskStatusReady || next == TaskStatusFailed || next == TaskStatusCanceled
	default:
		return false
	}
}

type AttemptStatus string

const (
	AttemptStatusPending   AttemptStatus = "PENDING"
	AttemptStatusAdmitted  AttemptStatus = "ADMITTED"
	AttemptStatusRunning   AttemptStatus = "RUNNING"
	AttemptStatusSucceeded AttemptStatus = "SUCCEEDED"
	AttemptStatusFailed    AttemptStatus = "FAILED"
	AttemptStatusLost      AttemptStatus = "LOST"
	AttemptStatusCanceled  AttemptStatus = "CANCELED"
)

func (s AttemptStatus) Validate() error {
	if !s.IsValid() {
		return fmt.Errorf("invalid attempt status %q", s)
	}
	return nil
}

func (s AttemptStatus) IsValid() bool {
	switch s {
	case AttemptStatusPending, AttemptStatusAdmitted, AttemptStatusRunning,
		AttemptStatusSucceeded, AttemptStatusFailed, AttemptStatusLost, AttemptStatusCanceled:
		return true
	default:
		return false
	}
}

func (s AttemptStatus) IsTerminal() bool {
	return s == AttemptStatusSucceeded || s == AttemptStatusFailed ||
		s == AttemptStatusLost || s == AttemptStatusCanceled
}
