package api

import (
	"encoding/json"
	"testing"
	"time"

	"aegisrt/internal/eventlog"
)

func TestProjectEventAcceptsStrictDerivedTiming(t *testing.T) {
	readyAt := time.Date(2026, 7, 20, 10, 0, 0, 100_000_000, time.UTC)
	startedAt := readyAt.Add(1900 * time.Millisecond)
	finishedAt := startedAt.Add(3250 * time.Millisecond)
	event := timingProjectionEvent(timingPayload(
		readyAt, startedAt, finishedAt, 1900, 3250,
	))

	projected, err := projectEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Payload.Timing == nil ||
		!projected.Payload.Timing.ReadyAt.Equal(readyAt) ||
		!projected.Payload.Timing.StartedAt.Equal(startedAt) ||
		!projected.Payload.Timing.FinishedAt.Equal(finishedAt) ||
		projected.Payload.Timing.QueueMillis != 1900 ||
		projected.Payload.Timing.RunMillis != 3250 {
		t.Fatalf("timing = %+v", projected.Payload.Timing)
	}
}

func TestProjectEventRejectsMalformedOrInconsistentTiming(t *testing.T) {
	ready := "2026-07-20T10:00:00Z"
	started := "2026-07-20T10:00:02Z"
	finished := "2026-07-20T10:00:05Z"
	tests := []struct {
		name   string
		timing string
	}{
		{name: "unknown field", timing: `{"ready_at":"` + ready + `","started_at":"` + started + `","finished_at":"` + finished + `","queue_ms":2000,"run_ms":3000,"goal":"secret"}`},
		{name: "missing field", timing: `{"ready_at":"` + ready + `","started_at":"` + started + `","finished_at":"` + finished + `","queue_ms":2000}`},
		{name: "duplicate field", timing: `{"ready_at":"` + ready + `","ready_at":"` + ready + `","started_at":"` + started + `","finished_at":"` + finished + `","queue_ms":2000,"run_ms":3000}`},
		{name: "null timestamp", timing: `{"ready_at":null,"started_at":"` + started + `","finished_at":"` + finished + `","queue_ms":2000,"run_ms":3000}`},
		{name: "reversed queue", timing: `{"ready_at":"` + started + `","started_at":"` + ready + `","finished_at":"` + finished + `","queue_ms":-2000,"run_ms":5000}`},
		{name: "reversed run", timing: `{"ready_at":"` + ready + `","started_at":"` + finished + `","finished_at":"` + started + `","queue_ms":5000,"run_ms":-3000}`},
		{name: "wrong queue", timing: `{"ready_at":"` + ready + `","started_at":"` + started + `","finished_at":"` + finished + `","queue_ms":1999,"run_ms":3000}`},
		{name: "wrong run", timing: `{"ready_at":"` + ready + `","started_at":"` + started + `","finished_at":"` + finished + `","queue_ms":2000,"run_ms":2999}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := timingProjectionEvent(`{"policy":"fifo","exit_code":0,"timed_out":false,"canceled":false,"attempt_status":"SUCCEEDED","timing":` + test.timing + `}`)
			if _, err := projectEvent(event); err != errInvalidEventProjection {
				t.Fatalf("projectEvent() error = %v, want errInvalidEventProjection", err)
			}
		})
	}
}

func TestProjectEventAllowsOutcomeWithoutTiming(t *testing.T) {
	if _, err := projectEvent(timingProjectionEvent(safeEventPayload())); err != nil {
		t.Fatalf("projectEvent() rejected Context-before-Worker outcome: %v", err)
	}
}

func TestProjectEventRejectsObserverTimeBeforeTimingFinishedAt(t *testing.T) {
	readyAt := apiEventStreamStart.Add(-3 * time.Second)
	startedAt := apiEventStreamStart.Add(-2 * time.Second)
	finishedAt := apiEventStreamStart.Add(time.Second)
	event := timingProjectionEvent(timingPayload(
		readyAt, startedAt, finishedAt, 1000, 3000,
	))
	event.Time = apiEventStreamStart

	if _, err := projectEvent(event); err != errInvalidEventProjection {
		t.Fatalf("projectEvent() error = %v, want errInvalidEventProjection", err)
	}
}

func TestProjectEventRejectsTimingWithoutAttemptIdentity(t *testing.T) {
	readyAt := apiEventStreamStart.Add(-3 * time.Second)
	startedAt := apiEventStreamStart.Add(-2 * time.Second)
	finishedAt := apiEventStreamStart.Add(-time.Second)
	validPayload := timingPayload(readyAt, startedAt, finishedAt, 1000, 1000)

	t.Run("missing attempt id", func(t *testing.T) {
		event := timingProjectionEvent(validPayload)
		event.AttemptID = ""
		if _, err := projectEvent(event); err != errInvalidEventProjection {
			t.Fatalf("projectEvent() error = %v, want errInvalidEventProjection", err)
		}
	})

	t.Run("missing attempt status", func(t *testing.T) {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(validPayload), &payload); err != nil {
			t.Fatal(err)
		}
		delete(payload, "attempt_status")
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		event := timingProjectionEvent(string(encoded))
		if _, err := projectEvent(event); err != errInvalidEventProjection {
			t.Fatalf("projectEvent() error = %v, want errInvalidEventProjection", err)
		}
	})
}

func timingPayload(readyAt, startedAt, finishedAt time.Time, queueMillis, runMillis int64) string {
	payload, _ := json.Marshal(map[string]any{
		"policy": "fifo", "exit_code": 0, "timed_out": false,
		"canceled": false, "attempt_status": "SUCCEEDED",
		"timing": map[string]any{
			"ready_at": readyAt, "started_at": startedAt, "finished_at": finishedAt,
			"queue_ms": queueMillis, "run_ms": runMillis,
		},
	})
	return string(payload)
}

func timingProjectionEvent(payload string) eventlog.Event {
	return eventlog.Event{
		ID: 1, Type: "run.succeeded", Time: apiEventStreamStart.Add(time.Minute),
		WorkflowID: "workflow-1", TaskID: "task-1", AttemptID: "attempt-1",
		Payload: json.RawMessage(payload),
	}
}
