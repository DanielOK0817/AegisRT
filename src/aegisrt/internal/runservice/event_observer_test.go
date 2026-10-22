package runservice

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/eventlog"
	"aegisrt/internal/executor"
)

func observerOutcome(failed bool) Outcome {
	outcome := Outcome{
		FinishedAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.FixedZone("test", 8*60*60)),
	}
	outcome.Result.Decision.Candidate.ReadyAt = outcome.FinishedAt.Add(-3 * time.Second)
	outcome.Result.Decision.Policy = "dag-drcfs"
	outcome.Result.Decision.Candidate.WorkflowID = "workflow-1"
	outcome.Result.Decision.Candidate.TaskID = "task-1"
	outcome.Result.Execution.Attempt.ID = "attempt-1"
	outcome.Result.Execution.Attempt.TaskID = "task-1"
	outcome.Result.Execution.Attempt.StartedAt = outcome.FinishedAt.Add(-2 * time.Second)
	outcome.Result.Execution.Attempt.FinishedAt = outcome.FinishedAt.Add(-time.Second)
	outcome.Result.Execution.Process.AttemptID = "attempt-1"
	if failed {
		outcome.Err = errors.New("raw-error-secret command --token=args-secret")
		outcome.Result.Decision.Reason = "unsafe-free-form-reason-secret"
		outcome.Result.Execution.Attempt.Status = domain.AttemptStatusFailed
		outcome.Result.Execution.Attempt.Failure = "attempt-failure-secret"
		outcome.Result.Execution.Process.ExitCode = 17
		outcome.Result.Execution.Process.TimedOut = false
		outcome.Result.Execution.Process.Stdout = []byte("stdout-secret")
		outcome.Result.Execution.Process.Stderr = []byte("stderr-secret")
	} else {
		outcome.Result.Execution.Attempt.Status = domain.AttemptStatusSucceeded
	}
	return outcome
}

func TestEventObserverRecordsSuccessfulOutcome(t *testing.T) {
	log, err := eventlog.New(4)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewEventObserver(log)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.Observe(observerOutcome(false)); err != nil {
		t.Fatal(err)
	}
	events := log.Snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	event := events[0]
	if event.Type != EventTypeRunSucceeded || event.WorkflowID != "workflow-1" ||
		event.TaskID != "task-1" || event.AttemptID != "attempt-1" {
		t.Fatalf("success event = %#v", event)
	}
	if event.Time.Location() != time.UTC {
		t.Fatalf("event time location = %v, want UTC", event.Time.Location())
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["policy"] != "dag-drcfs" || payload["exit_code"] != float64(0) ||
		payload["timed_out"] != false || payload["canceled"] != false ||
		payload["attempt_status"] != string(domain.AttemptStatusSucceeded) {
		t.Fatalf("success payload = %#v", payload)
	}
}

func TestEventObserverRecordsCompleteTiming(t *testing.T) {
	log, _ := eventlog.New(1)
	observer, _ := NewEventObserver(log)
	outcome := observerOutcome(false)
	readyAt := time.Date(2026, 7, 19, 3, 59, 58, 100_000_000, time.UTC)
	startedAt := readyAt.Add(1900 * time.Millisecond)
	finishedAt := startedAt.Add(3250 * time.Millisecond)
	outcome.Result.Decision.Candidate.ReadyAt = readyAt
	outcome.Result.Execution.Attempt.StartedAt = startedAt
	outcome.Result.Execution.Attempt.FinishedAt = finishedAt
	outcome.FinishedAt = finishedAt.Add(time.Second)

	if err := observer.Observe(outcome); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Timing safeTimingPayload `json:"timing"`
	}
	if err := json.Unmarshal(log.Snapshot()[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Timing.ReadyAt.Equal(readyAt) ||
		!payload.Timing.StartedAt.Equal(startedAt) ||
		!payload.Timing.FinishedAt.Equal(finishedAt) ||
		payload.Timing.QueueMillis != 1900 || payload.Timing.RunMillis != 3250 {
		t.Fatalf("timing = %+v", payload.Timing)
	}
}

func TestEventObserverOmitsTimingWithoutAttempt(t *testing.T) {
	log, _ := eventlog.New(1)
	observer, _ := NewEventObserver(log)
	outcome := observerOutcome(true)
	outcome.Result.Execution.Attempt.ID = ""
	outcome.Result.Execution.Attempt.StartedAt = time.Time{}
	outcome.Result.Execution.Attempt.FinishedAt = time.Time{}
	outcome.Result.Execution.Process.AttemptID = ""

	if err := observer.Observe(outcome); err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(log.Snapshot()[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["timing"]; exists {
		t.Fatalf("incomplete timing was emitted: %s", log.Snapshot()[0].Payload)
	}
}

func TestEventObserverRejectsIncompleteAttemptTiming(t *testing.T) {
	log, _ := eventlog.New(1)
	observer, _ := NewEventObserver(log)
	outcome := observerOutcome(false)
	outcome.Result.Execution.Attempt.FinishedAt = time.Time{}

	if err := observer.Observe(outcome); !errors.Is(err, ErrInvalidOutcomeTiming) {
		t.Fatalf("Observe() error = %v, want ErrInvalidOutcomeTiming", err)
	}
	if log.Stats().TotalAppended != 0 {
		t.Fatalf("incomplete timing changed log stats: %#v", log.Stats())
	}
}

func TestEventObserverRejectsInvalidCompleteTiming(t *testing.T) {
	log, _ := eventlog.New(1)
	observer, _ := NewEventObserver(log)
	outcome := observerOutcome(false)
	outcome.Result.Decision.Candidate.ReadyAt = time.Now().UTC()
	outcome.Result.Execution.Attempt.StartedAt = outcome.Result.Decision.Candidate.ReadyAt.Add(-time.Millisecond)
	outcome.Result.Execution.Attempt.FinishedAt = outcome.Result.Decision.Candidate.ReadyAt.Add(time.Millisecond)

	if err := observer.Observe(outcome); !errors.Is(err, ErrInvalidOutcomeTiming) {
		t.Fatalf("Observe() error = %v, want ErrInvalidOutcomeTiming", err)
	}
	if log.Stats().TotalAppended != 0 {
		t.Fatalf("invalid timing changed log stats: %#v", log.Stats())
	}
}

func TestEventObserverRejectsInvalidObservationTime(t *testing.T) {
	tests := []struct {
		name       string
		finishedAt time.Time
	}{
		{name: "missing", finishedAt: time.Time{}},
		{name: "before attempt", finishedAt: observerOutcome(false).Result.Execution.Attempt.FinishedAt.Add(-time.Nanosecond)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log, _ := eventlog.New(1)
			observer, _ := NewEventObserver(log)
			outcome := observerOutcome(false)
			outcome.FinishedAt = test.finishedAt

			if err := observer.Observe(outcome); !errors.Is(err, ErrInvalidOutcomeTiming) {
				t.Fatalf("Observe() error = %v, want ErrInvalidOutcomeTiming", err)
			}
			if log.Stats().TotalAppended != 0 {
				t.Fatalf("invalid observation time changed log stats: %#v", log.Stats())
			}
		})
	}
}

func TestEventObserverDoesNotLeakAgentArtifacts(t *testing.T) {
	log, _ := eventlog.New(4)
	observer, _ := NewEventObserver(log)
	outcome := observerOutcome(false)
	const secret = "artifact-secret-must-not-enter-events"
	outcome.Result.Execution.Output = agentprotocol.Envelope{
		Version: agentprotocol.VersionV1,
		Artifacts: []agentprotocol.Artifact{{
			Name: "secret", Content: secret, Visibility: "agent",
		}},
	}
	if err := observer.Observe(outcome); err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(log.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), secret) {
		t.Fatalf("event log leaked Agent artifact: %s", serialized)
	}
}

func TestEventObserverFailureUsesSafeAllowlist(t *testing.T) {
	log, _ := eventlog.New(4)
	observer, _ := NewEventObserver(log)
	outcome := observerOutcome(true)
	if err := observer.Observe(outcome); err != nil {
		t.Fatal(err)
	}
	event := log.Snapshot()[0]
	if event.Type != EventTypeRunFailed {
		t.Fatalf("event type = %q, want %q", event.Type, EventTypeRunFailed)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	wantKeys := map[string]bool{
		"policy": true, "exit_code": true, "timed_out": true,
		"canceled": true, "attempt_status": true, "timing": true, "scheduling": true,
	}
	if len(payload) != len(wantKeys) {
		t.Fatalf("payload keys = %#v", payload)
	}
	for key := range payload {
		if !wantKeys[key] {
			t.Fatalf("unsafe payload key %q in %#v", key, payload)
		}
	}
	if payload["exit_code"] != float64(17) || payload["timed_out"] != false ||
		payload["attempt_status"] != string(domain.AttemptStatusFailed) {
		t.Fatalf("failure payload = %#v", payload)
	}
	serialized, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"stdout-secret", "stderr-secret", "raw-error-secret", "args-secret",
		"unsafe-free-form-reason-secret", "attempt-failure-secret", "stdout", "stderr",
	} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("serialized event leaked %q: %s", forbidden, serialized)
		}
	}
}

func TestEventObserverSeparatesCancellationAndTimeout(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Outcome)
		wantType  string
	}{
		{
			name: "canceled",
			configure: func(outcome *Outcome) {
				outcome.Err = executor.ErrCanceled
				outcome.Result.Execution.Attempt.Status = domain.AttemptStatusCanceled
				outcome.Result.Execution.Process.Canceled = true
			},
			wantType: EventTypeRunCanceled,
		},
		{
			name: "interrupted",
			configure: func(outcome *Outcome) {
				outcome.Err = errors.Join(executor.ErrCanceled, controlplane.ErrAttemptInterrupted)
				outcome.Result.Execution.Attempt.Status = domain.AttemptStatusLost
				outcome.Result.Execution.Process.Canceled = true
			},
			wantType: EventTypeRunInterrupted,
		},
		{
			name: "timed out",
			configure: func(outcome *Outcome) {
				outcome.Err = executor.ErrTimeout
				outcome.Result.Execution.Attempt.Status = domain.AttemptStatusFailed
				outcome.Result.Execution.Process.TimedOut = true
			},
			wantType: EventTypeRunTimedOut,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log, _ := eventlog.New(1)
			observer, _ := NewEventObserver(log)
			outcome := observerOutcome(false)
			test.configure(&outcome)
			if err := observer.Observe(outcome); err != nil {
				t.Fatal(err)
			}
			if got := log.Snapshot()[0].Type; got != test.wantType {
				t.Fatalf("event type = %q, want %q", got, test.wantType)
			}
		})
	}
}

func TestEventObserverRecordsOnlySafeContextMetrics(t *testing.T) {
	log, _ := eventlog.New(4)
	observer, _ := NewEventObserver(log)
	outcome := observerOutcome(false)
	outcome.Result.Context = &controlplane.ContextResult{
		ManifestID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RefCount:   2,
		Metrics: contextvm.AssemblyMetrics{
			LogicalTokens: 20, PhysicalTokens: 12, ReusedTokens: 8,
			DroppedTokens: 3, SummaryPages: 1,
		},
	}
	if err := observer.Observe(outcome); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Context safeContextPayload `json:"context"`
	}
	if err := json.Unmarshal(log.Snapshot()[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Context.ManifestID != outcome.Result.Context.ManifestID ||
		payload.Context.RefCount != 2 || payload.Context.ReusedTokens != 8 ||
		payload.Context.PhysicalTokens != 12 {
		t.Fatalf("safe context payload = %#v", payload.Context)
	}
}

func TestEventObserverReturnsAppendErrors(t *testing.T) {
	if _, err := NewEventObserver(nil); !errors.Is(err, ErrEventLogRequired) {
		t.Fatalf("nil log error = %v", err)
	}
	log, _ := eventlog.New(1)
	observer, _ := NewEventObserver(log)
	outcome := observerOutcome(false)
	outcome.FinishedAt = time.Time{}
	outcome.Result.Execution.Attempt.ID = ""
	outcome.Result.Execution.Process.AttemptID = ""
	if err := observer.Observe(outcome); !errors.Is(err, eventlog.ErrInvalidEvent) {
		t.Fatalf("Observe() error = %v, want eventlog.ErrInvalidEvent", err)
	}
	if log.Stats().TotalAppended != 0 {
		t.Fatalf("failed Observe changed log stats: %#v", log.Stats())
	}
}

func TestEventObserverConcurrentOutcomes(t *testing.T) {
	const count = 64
	log, _ := eventlog.New(count)
	observer, _ := NewEventObserver(log)
	errs := make(chan error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			outcome := observerOutcome(index%2 == 1)
			outcome.Result.Decision.Candidate.TaskID = fmt.Sprintf("task-%d", index)
			outcome.Result.Execution.Attempt.ID = fmt.Sprintf("attempt-%d", index)
			if err := observer.Observe(outcome); err != nil {
				errs <- err
			}
		}(index)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	events := log.Snapshot()
	if len(events) != count {
		t.Fatalf("event count = %d, want %d", len(events), count)
	}
	for index, event := range events {
		if event.ID != uint64(index+1) {
			t.Fatalf("event ID[%d] = %d", index, event.ID)
		}
	}
}
