package domain

import (
	"testing"
	"time"
)

func validAttempt() AgentAttempt {
	return AgentAttempt{
		ID:             "attempt-1",
		TaskID:         "task-1",
		AgentID:        "agent-1",
		Number:         1,
		Status:         AttemptStatusPending,
		IdempotencyKey: "attempt-key-1",
		Allocated:      ResourceVector{CPUMilli: 100},
	}
}

func TestAgentAttemptValidate(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*AgentAttempt)
		valid  bool
	}{
		{name: "pending", mutate: func(*AgentAttempt) {}, valid: true},
		{name: "admitted", mutate: func(a *AgentAttempt) {
			a.Status = AttemptStatusAdmitted
			a.WorkerID = "worker-1"
			a.LeaseExpiresAt = now.Add(time.Minute)
		}, valid: true},
		{name: "running", mutate: func(a *AgentAttempt) {
			a.Status = AttemptStatusRunning
			a.WorkerID = "worker-1"
			a.StartedAt = now
			a.LastHeartbeatAt = now.Add(time.Second)
			a.LeaseExpiresAt = now.Add(time.Minute)
		}, valid: true},
		{name: "succeeded", mutate: func(a *AgentAttempt) {
			a.Status = AttemptStatusSucceeded
			a.WorkerID = "worker-1"
			a.StartedAt = now
			a.FinishedAt = now.Add(time.Second)
		}, valid: true},
		{name: "canceled before start", mutate: func(a *AgentAttempt) { a.Status = AttemptStatusCanceled; a.FinishedAt = now }, valid: true},
		{name: "missing id", mutate: func(a *AgentAttempt) { a.ID = "" }},
		{name: "zero number", mutate: func(a *AgentAttempt) { a.Number = 0 }},
		{name: "unknown status", mutate: func(a *AgentAttempt) { a.Status = AttemptStatus("UNKNOWN") }},
		{name: "bad allocation", mutate: func(a *AgentAttempt) { a.Allocated.MemoryMiB = -1 }},
		{name: "admitted missing worker", mutate: func(a *AgentAttempt) { a.Status = AttemptStatusAdmitted; a.LeaseExpiresAt = now.Add(time.Minute) }},
		{name: "admitted missing lease", mutate: func(a *AgentAttempt) { a.Status = AttemptStatusAdmitted; a.WorkerID = "worker-1" }},
		{name: "running missing start", mutate: func(a *AgentAttempt) {
			a.Status = AttemptStatusRunning
			a.WorkerID = "worker-1"
			a.LeaseExpiresAt = now.Add(time.Minute)
		}},
		{name: "terminal missing finish", mutate: func(a *AgentAttempt) { a.Status = AttemptStatusSucceeded; a.WorkerID = "worker-1"; a.StartedAt = now }},
		{name: "failed missing reason", mutate: func(a *AgentAttempt) {
			a.Status = AttemptStatusFailed
			a.WorkerID = "worker-1"
			a.StartedAt = now
			a.FinishedAt = now.Add(time.Second)
		}},
		{name: "success with failure", mutate: func(a *AgentAttempt) {
			a.Status = AttemptStatusSucceeded
			a.WorkerID = "worker-1"
			a.StartedAt = now
			a.FinishedAt = now.Add(time.Second)
			a.Failure = "unexpected"
		}},
		{name: "heartbeat before start", mutate: func(a *AgentAttempt) {
			a.Status = AttemptStatusRunning
			a.WorkerID = "worker-1"
			a.StartedAt = now
			a.LastHeartbeatAt = now.Add(-time.Second)
			a.LeaseExpiresAt = now.Add(time.Minute)
		}},
		{name: "finish before heartbeat", mutate: func(a *AgentAttempt) {
			a.Status = AttemptStatusSucceeded
			a.WorkerID = "worker-1"
			a.StartedAt = now
			a.LastHeartbeatAt = now.Add(2 * time.Second)
			a.FinishedAt = now.Add(time.Second)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempt := validAttempt()
			test.mutate(&attempt)
			if err := attempt.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() error = %v, valid = %v", err, test.valid)
			}
		})
	}
}
