package runservice

import (
	"encoding/json"
	"errors"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/eventlog"
)

const (
	EventTypeRunSucceeded   = "run.succeeded"
	EventTypeRunFailed      = "run.failed"
	EventTypeRunCanceled    = "run.canceled"
	EventTypeRunTimedOut    = "run.timed_out"
	EventTypeRunInterrupted = "run.interrupted"
)

var ErrEventLogRequired = errors.New("event log is required")

var ErrInvalidOutcomeTiming = errors.New("invalid outcome timing")

// EventObserver records a deliberately small, safe projection of Outcome.
// Free-form output, errors, scheduler reasons, and process arguments are never
// copied into either the event metadata or payload.
type EventObserver struct {
	log *eventlog.Log
}

var _ Observer = (*EventObserver)(nil)

func NewEventObserver(log *eventlog.Log) (*EventObserver, error) {
	if log == nil {
		return nil, ErrEventLogRequired
	}
	return &EventObserver{log: log}, nil
}

type safeOutcomePayload struct {
	Policy        string                      `json:"policy,omitempty"`
	ExitCode      int                         `json:"exit_code"`
	TimedOut      bool                        `json:"timed_out"`
	Canceled      bool                        `json:"canceled"`
	AttemptStatus string                      `json:"attempt_status,omitempty"`
	Context       *safeContextPayload         `json:"context,omitempty"`
	Timing        *safeTimingPayload          `json:"timing,omitempty"`
	Scheduling    *eventlog.SchedulingPayload `json:"scheduling,omitempty"`
	Lifecycle     *eventlog.LifecyclePayload  `json:"lifecycle,omitempty"`
	Output        *eventlog.OutputPayload     `json:"output,omitempty"`
}

type safeTimingPayload struct {
	ReadyAt     time.Time `json:"ready_at"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	QueueMillis int64     `json:"queue_ms"`
	RunMillis   int64     `json:"run_ms"`
}

type safeContextPayload struct {
	ManifestID     string `json:"manifest_id"`
	RefCount       int    `json:"ref_count"`
	LogicalTokens  int    `json:"logical_tokens"`
	PhysicalTokens int    `json:"physical_tokens"`
	ReusedTokens   int    `json:"reused_tokens"`
	DroppedTokens  int    `json:"dropped_tokens"`
	SummaryPages   int    `json:"summary_pages"`
}

func (o *EventObserver) Observe(outcome Outcome) error {
	if o == nil || o.log == nil {
		return ErrEventLogRequired
	}
	execution := outcome.Result.Execution
	safePayload := safeOutcomePayload{
		Policy:        outcome.Result.Decision.Policy,
		ExitCode:      execution.Process.ExitCode,
		TimedOut:      execution.Process.TimedOut,
		Canceled:      execution.Process.Canceled,
		AttemptStatus: string(execution.Attempt.Status),
	}
	timing, err := safeTimingForOutcome(outcome)
	if err != nil {
		return err
	}
	safePayload.Timing = timing
	if err := enrichOutcomePayload(outcome, &safePayload); err != nil {
		return err
	}
	if contextResult := outcome.Result.Context; contextResult != nil {
		safePayload.Context = &safeContextPayload{
			ManifestID: contextResult.ManifestID, RefCount: contextResult.RefCount,
			LogicalTokens:  contextResult.Metrics.LogicalTokens,
			PhysicalTokens: contextResult.Metrics.PhysicalTokens,
			ReusedTokens:   contextResult.Metrics.ReusedTokens,
			DroppedTokens:  contextResult.Metrics.DroppedTokens,
			SummaryPages:   contextResult.Metrics.SummaryPages,
		}
	}
	payload, err := json.Marshal(safePayload)
	if err != nil {
		return err
	}
	eventType := eventTypeForOutcome(outcome)
	attemptID := execution.Attempt.ID
	if attemptID == "" {
		attemptID = execution.Process.AttemptID
	}
	_, err = o.log.Append(eventlog.Event{
		Type:       eventType,
		Time:       outcome.FinishedAt.UTC(),
		WorkflowID: outcome.Result.Decision.Candidate.WorkflowID,
		TaskID:     outcome.Result.Decision.Candidate.TaskID,
		AttemptID:  attemptID,
		Payload:    payload,
	})
	return err
}

func safeTimingForOutcome(outcome Outcome) (*safeTimingPayload, error) {
	attempt := outcome.Result.Execution.Attempt
	readyAt := outcome.Result.Decision.Candidate.ReadyAt
	if attempt.ID == "" {
		return nil, nil
	}
	if readyAt.IsZero() || attempt.FinishedAt.IsZero() || attempt.FinishedAt.Before(readyAt) {
		return nil, ErrInvalidOutcomeTiming
	}
	if outcome.FinishedAt.IsZero() || outcome.FinishedAt.Before(attempt.FinishedAt) {
		return nil, ErrInvalidOutcomeTiming
	}
	if attempt.StartedAt.IsZero() {
		if attempt.Status == domain.AttemptStatusSucceeded || !attempt.Status.IsTerminal() {
			return nil, ErrInvalidOutcomeTiming
		}
		return nil, nil
	}
	if attempt.StartedAt.Before(readyAt) || attempt.FinishedAt.Before(attempt.StartedAt) {
		return nil, ErrInvalidOutcomeTiming
	}
	return &safeTimingPayload{
		ReadyAt: readyAt.UTC(), StartedAt: attempt.StartedAt.UTC(), FinishedAt: attempt.FinishedAt.UTC(),
		QueueMillis: attempt.StartedAt.Sub(readyAt).Milliseconds(),
		RunMillis:   attempt.FinishedAt.Sub(attempt.StartedAt).Milliseconds(),
	}, nil
}

func eventTypeForOutcome(outcome Outcome) string {
	switch {
	case outcome.Err == nil:
		return EventTypeRunSucceeded
	case tickInterrupted(outcome.Result, outcome.Err):
		return EventTypeRunInterrupted
	case tickTimedOut(outcome.Result, outcome.Err):
		return EventTypeRunTimedOut
	case tickCanceled(outcome.Result, outcome.Err):
		return EventTypeRunCanceled
	default:
		return EventTypeRunFailed
	}
}
