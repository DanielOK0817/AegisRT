package runservice

import (
	"errors"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
	"aegisrt/internal/eventlog"
)

var ErrInvalidOutcomeDetails = errors.New("invalid outcome details")

func enrichOutcomePayload(outcome Outcome, payload *safeOutcomePayload) error {
	d := outcome.Result.Decision
	if d.Policy != "" && d.Policy != "fifo" && d.Policy != "priority" && d.Policy != "dag-drcfs" {
		return ErrInvalidOutcomeDetails
	}
	status := outcome.Result.Execution.Attempt.Status
	expected := map[string]domain.AttemptStatus{
		EventTypeRunSucceeded:   domain.AttemptStatusSucceeded,
		EventTypeRunFailed:      domain.AttemptStatusFailed,
		EventTypeRunCanceled:    domain.AttemptStatusCanceled,
		EventTypeRunTimedOut:    domain.AttemptStatusFailed,
		EventTypeRunInterrupted: domain.AttemptStatusLost,
	}[eventTypeForOutcome(outcome)]
	if status != "" && status != expected || status == "" && outcome.Err == nil {
		return ErrInvalidOutcomeDetails
	}
	if d.Candidate.TaskID != "" {
		payload.Scheduling = &eventlog.SchedulingPayload{
			Priority: d.Candidate.Priority, InheritedPriority: d.Candidate.InheritedPriority,
			CriticalPath: d.Candidate.CriticalPath, UnlockCount: d.Candidate.UnlockCount,
			WorkflowVRuntime: d.WorkflowVRuntime, WorkflowReservedCharge: d.WorkflowReservedCharge,
			DominantShare: d.DominantShare,
		}
		if !payload.Scheduling.Valid() {
			return ErrInvalidOutcomeDetails
		}
	}
	p := outcome.Result.Execution.Process
	if !p.PreparationStartedAt.IsZero() || !p.ProcessStartedAt.IsZero() || !p.WaitCompletedAt.IsZero() {
		phase := "before_start"
		if p.ProcessStarted {
			phase = "process_started"
		}
		if p.WaitCompleted {
			phase = "process_exited"
		}
		payload.Lifecycle = &eventlog.LifecyclePayload{
			Phase: phase, PreparationStartedAt: optionalTime(p.PreparationStartedAt),
			ProcessStartedAt: optionalTime(p.ProcessStartedAt), WaitCompletedAt: optionalTime(p.WaitCompletedAt),
		}
		if !payload.Lifecycle.Valid(outcome.FinishedAt) {
			return ErrInvalidOutcomeDetails
		}
		if outcome.Err == nil && phase != "process_exited" {
			return ErrInvalidOutcomeDetails
		}
	}
	execution := outcome.Result.Execution
	if outcome.Err == nil && execution.Attempt.Status == domain.AttemptStatusSucceeded && execution.Output.Version == agentprotocol.VersionV1 {
		payload.Output = &eventlog.OutputPayload{ArtifactCount: len(execution.Output.Artifacts)}
		for _, artifact := range execution.Output.Artifacts {
			payload.Output.ContentBytes += int64(len(artifact.Content))
		}
	}
	return nil
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}
