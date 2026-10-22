package runservice

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
	"aegisrt/internal/eventlog"
)

func TestEventObserverRetainsFailureBeforeProcessStart(t *testing.T) {
	for _, status := range []domain.AttemptStatus{domain.AttemptStatusFailed, domain.AttemptStatusLost, domain.AttemptStatusCanceled} {
		t.Run(string(status), func(t *testing.T) {
			log, _ := eventlog.New(2)
			observer, _ := NewEventObserver(log)
			outcome := observerOutcome(true)
			outcome.Result.Execution.Attempt.Status = status
			outcome.Result.Execution.Attempt.StartedAt = time.Time{}
			if err := observer.Observe(outcome); err != nil {
				t.Fatalf("legitimate pre-start failure was dropped: %v", err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(log.Snapshot()[0].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if _, exists := payload["timing"]; exists {
				t.Fatal("a process that never started must not have run duration")
			}
		})
	}
}

func TestEventObserverRecordsSchedulingProcessAndCommittedOutput(t *testing.T) {
	log, _ := eventlog.New(2)
	observer, _ := NewEventObserver(log)
	outcome := observerOutcome(false)
	d := &outcome.Result.Decision
	d.Candidate.Priority, d.Candidate.InheritedPriority = 2, 4
	d.Candidate.CriticalPath, d.Candidate.UnlockCount = 3, 2
	d.WorkflowVRuntime, d.WorkflowReservedCharge, d.DominantShare = 1.25, 0.5, 0.25
	p := &outcome.Result.Execution.Process
	p.PreparationStartedAt = d.Candidate.ReadyAt.Add(time.Millisecond)
	p.ProcessStarted, p.WaitCompleted = true, true
	p.ProcessStartedAt = outcome.Result.Execution.Attempt.StartedAt
	p.WaitCompletedAt = outcome.Result.Execution.Attempt.FinishedAt.Add(-time.Millisecond)
	outcome.Result.Execution.Output = agentprotocol.Envelope{Version: agentprotocol.VersionV1, Artifacts: []agentprotocol.Artifact{
		{Name: "secret-name", Content: "secret-content", Recipients: []string{"secret-recipient"}},
	}}
	if err := observer.Observe(outcome); err != nil {
		t.Fatal(err)
	}
	var payload safeOutcomePayload
	if err := json.Unmarshal(log.Snapshot()[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Scheduling == nil || payload.Scheduling.WorkflowReservedCharge != 0.5 ||
		payload.Lifecycle == nil || payload.Lifecycle.Phase != "process_exited" ||
		payload.Output == nil || payload.Output.ArtifactCount != 1 || payload.Output.ContentBytes != 14 {
		t.Fatalf("details = %+v", payload)
	}
	if strings.Contains(string(log.Snapshot()[0].Payload), "secret") {
		t.Fatal("output detail leaked")
	}
	outcome.Err = errors.New("checkpoint failed")
	outcome.Result.Execution.Attempt.Status = domain.AttemptStatusLost
	if err := observer.Observe(outcome); err != nil {
		t.Fatal(err)
	}
	var failed map[string]json.RawMessage
	if err := json.Unmarshal(log.Snapshot()[1].Payload, &failed); err != nil {
		t.Fatal(err)
	}
	if _, exists := failed["output"]; exists {
		t.Fatal("unconfirmed output was reported as published")
	}
}

func TestEventObserverRejectsInvalidDetailsWithoutPoisoningReplay(t *testing.T) {
	for _, mutate := range []func(*Outcome){
		func(o *Outcome) { o.Result.Decision.WorkflowReservedCharge = math.NaN() },
		func(o *Outcome) { o.Result.Decision.Policy = "secret-policy" },
		func(o *Outcome) { o.Result.Execution.Attempt.Status = "secret-status" },
		func(o *Outcome) { o.Result.Execution.Process.WaitCompletedAt = o.FinishedAt },
		func(o *Outcome) {
			o.Result.Execution.Process.PreparationStartedAt = o.FinishedAt.Add(time.Second)
		},
	} {
		log, _ := eventlog.New(2)
		observer, _ := NewEventObserver(log)
		outcome := observerOutcome(false)
		mutate(&outcome)
		if err := observer.Observe(outcome); !errors.Is(err, ErrInvalidOutcomeDetails) {
			t.Fatalf("error = %v", err)
		}
		if log.Stats().TotalAppended != 0 {
			t.Fatal("invalid event entered replay")
		}
	}
}
