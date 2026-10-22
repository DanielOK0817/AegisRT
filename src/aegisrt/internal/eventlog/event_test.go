package eventlog

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func validEvent() Event {
	return Event{
		Type:       "task.started",
		Time:       time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
		WorkflowID: "workflow-1",
		TaskID:     "task-1",
		AttemptID:  "attempt-1",
		Payload:    json.RawMessage(`{"worker":"worker-1"}`),
	}
}

func TestEventValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Event)
		want   error
	}{
		{name: "valid task attempt", mutate: func(*Event) {}},
		{name: "valid global event", mutate: func(e *Event) { e.WorkflowID, e.TaskID, e.AttemptID = "", "", "" }},
		{name: "valid JSON scalar", mutate: func(e *Event) { e.Payload = json.RawMessage(`true`) }},
		{name: "missing type", mutate: func(e *Event) { e.Type = "" }, want: ErrInvalidIdentifier},
		{name: "invalid type", mutate: func(e *Event) { e.Type = "task/started" }, want: ErrInvalidIdentifier},
		{name: "zero time", mutate: func(e *Event) { e.Time = time.Time{} }, want: ErrInvalidEvent},
		{name: "invalid workflow", mutate: func(e *Event) { e.WorkflowID = " workflow" }, want: ErrInvalidIdentifier},
		{name: "task without workflow", mutate: func(e *Event) { e.WorkflowID = "" }, want: ErrInvalidEvent},
		{name: "attempt without task", mutate: func(e *Event) { e.TaskID = "" }, want: ErrInvalidEvent},
		{name: "nil payload", mutate: func(e *Event) { e.Payload = nil }, want: ErrInvalidPayload},
		{name: "truncated JSON", mutate: func(e *Event) { e.Payload = json.RawMessage(`{"x":`) }, want: ErrInvalidPayload},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := validEvent()
			test.mutate(&event)
			err := event.Validate()
			if test.want == nil && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}

func TestAppendRejectsInvalidMetadataAndBounds(t *testing.T) {
	if _, err := New(0); !errors.Is(err, ErrInvalidCapacity) {
		t.Fatalf("New(0) error = %v", err)
	}
	if _, err := NewWithOptions(Options{Capacity: 1}); !errors.Is(err, ErrInvalidCapacity) {
		t.Fatalf("zero payload bound error = %v", err)
	}
	log, err := NewWithOptions(Options{Capacity: 2, MaxPayloadBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	assigned := validEvent()
	assigned.ID = 7
	if _, err := log.Append(assigned); !errors.Is(err, ErrEventIDAssigned) {
		t.Fatalf("assigned ID error = %v", err)
	}
	oversized := validEvent()
	oversized.Payload = json.RawMessage(`{"long":true}`)
	if _, err := log.Append(oversized); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized payload error = %v", err)
	}
	if stats := log.Stats(); stats.TotalAppended != 0 || stats.Retained != 0 {
		t.Fatalf("failed appends changed stats: %#v", stats)
	}
}

func TestAppendAndReadsDeepCopyPayload(t *testing.T) {
	log, err := New(4)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"value":"original"}`)
	event := validEvent()
	event.Payload = payload
	appended, err := log.Append(event)
	if err != nil {
		t.Fatal(err)
	}
	payload[10] = 'X'
	appended.Payload[10] = 'Y'

	snapshot := log.Snapshot()
	if got := string(snapshot[0].Payload); got != `{"value":"original"}` {
		t.Fatalf("stored payload = %s", got)
	}
	snapshot[0].Payload[10] = 'Z'
	replay, err := log.Since(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(replay.Events[0].Payload); got != `{"value":"original"}` {
		t.Fatalf("replayed payload = %s", got)
	}
	replay.Events[0].Payload[10] = 'Q'
	if got := string(log.Snapshot()[0].Payload); got != `{"value":"original"}` {
		t.Fatalf("replay exposed internal payload = %s", got)
	}
}

func TestAppendIDExhaustion(t *testing.T) {
	log, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	log.lastID = math.MaxUint64
	if _, err := log.Append(validEvent()); !errors.Is(err, ErrIDExhausted) {
		t.Fatalf("Append() error = %v, want ErrIDExhausted", err)
	}
}

func TestIdentifierLengthBound(t *testing.T) {
	event := validEvent()
	event.Type = strings.Repeat("x", maxIdentifierBytes+1)
	if err := event.Validate(); !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("Validate() error = %v, want ErrInvalidIdentifier", err)
	}
}
