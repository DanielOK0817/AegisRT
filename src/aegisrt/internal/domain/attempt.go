package domain

import (
	"fmt"
	"strings"
	"time"
)

// AgentAttempt captures one execution of one task. Attempt numbers start at 1.
type AgentAttempt struct {
	ID              string         `json:"id"`
	TaskID          string         `json:"task_id"`
	AgentID         string         `json:"agent_id"`
	Number          int            `json:"number"`
	Status          AttemptStatus  `json:"status"`
	WorkerID        string         `json:"worker_id,omitempty"`
	Allocated       ResourceVector `json:"allocated"`
	IdempotencyKey  string         `json:"idempotency_key"`
	LeaseExpiresAt  time.Time      `json:"lease_expires_at,omitempty"`
	LastHeartbeatAt time.Time      `json:"last_heartbeat_at,omitempty"`
	StartedAt       time.Time      `json:"started_at,omitempty"`
	FinishedAt      time.Time      `json:"finished_at,omitempty"`
	CheckpointRef   string         `json:"checkpoint_ref,omitempty"`
	Failure         string         `json:"failure,omitempty"`
}

func (a AgentAttempt) Validate() error {
	for _, item := range []struct{ field, value string }{
		{"attempt id", a.ID}, {"task id", a.TaskID}, {"agent id", a.AgentID},
		{"idempotency key", a.IdempotencyKey},
	} {
		if err := validateIdentifier(item.field, item.value); err != nil {
			return err
		}
	}
	if a.Number <= 0 {
		return fmt.Errorf("attempt number must be positive")
	}
	if err := a.Status.Validate(); err != nil {
		return err
	}
	if err := a.Allocated.Validate(); err != nil {
		return fmt.Errorf("allocated resources: %w", err)
	}
	if a.CheckpointRef != "" && strings.TrimSpace(a.CheckpointRef) != a.CheckpointRef {
		return fmt.Errorf("checkpoint_ref must not contain surrounding whitespace")
	}
	if err := a.validateRuntimeFields(); err != nil {
		return err
	}
	return validateAttemptTimes(a)
}

func (a AgentAttempt) validateRuntimeFields() error {
	requiresWorker := a.Status == AttemptStatusAdmitted || a.Status == AttemptStatusRunning ||
		a.Status == AttemptStatusSucceeded || a.Status == AttemptStatusFailed || a.Status == AttemptStatusLost
	if requiresWorker {
		if err := validateIdentifier("worker id", a.WorkerID); err != nil {
			return err
		}
	}
	if a.Status == AttemptStatusAdmitted || a.Status == AttemptStatusRunning {
		if a.LeaseExpiresAt.IsZero() {
			return fmt.Errorf("lease_expires_at is required for %s attempt", a.Status)
		}
	}
	if a.Status == AttemptStatusRunning && a.StartedAt.IsZero() {
		return fmt.Errorf("started_at is required for running attempt")
	}
	// Preparation can fail or lose its owner before a process ever starts.
	if a.Status == AttemptStatusSucceeded && a.StartedAt.IsZero() {
		return fmt.Errorf("started_at is required for %s attempt", a.Status)
	}
	if a.Status.IsTerminal() && a.FinishedAt.IsZero() {
		return fmt.Errorf("finished_at is required for terminal attempt")
	}
	if a.Status == AttemptStatusFailed || a.Status == AttemptStatusLost {
		if strings.TrimSpace(a.Failure) == "" {
			return fmt.Errorf("failure is required for %s attempt", a.Status)
		}
		if strings.TrimSpace(a.Failure) != a.Failure {
			return fmt.Errorf("failure must not contain surrounding whitespace")
		}
	}
	if a.Status == AttemptStatusSucceeded && a.Failure != "" {
		return fmt.Errorf("successful attempt must not contain failure")
	}
	return nil
}

func validateAttemptTimes(a AgentAttempt) error {
	if !a.StartedAt.IsZero() && !a.LeaseExpiresAt.IsZero() && a.LeaseExpiresAt.Before(a.StartedAt) {
		return fmt.Errorf("lease_expires_at must not precede started_at")
	}
	if !a.StartedAt.IsZero() && !a.LastHeartbeatAt.IsZero() && a.LastHeartbeatAt.Before(a.StartedAt) {
		return fmt.Errorf("last_heartbeat_at must not precede started_at")
	}
	if !a.StartedAt.IsZero() && !a.FinishedAt.IsZero() && a.FinishedAt.Before(a.StartedAt) {
		return fmt.Errorf("finished_at must not precede started_at")
	}
	if !a.LastHeartbeatAt.IsZero() && !a.FinishedAt.IsZero() && a.FinishedAt.Before(a.LastHeartbeatAt) {
		return fmt.Errorf("finished_at must not precede last_heartbeat_at")
	}
	return nil
}
