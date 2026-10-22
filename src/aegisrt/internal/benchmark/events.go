package benchmark

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type Event struct {
	SchemaVersion  string         `json:"schema_version"`
	RunID          string         `json:"run_id"`
	Sequence       int            `json:"sequence"`
	EventType      string         `json:"event_type"`
	Status         string         `json:"status"`
	Phase          string         `json:"phase"`
	RecordedAt     time.Time      `json:"recorded_at"`
	MonotonicNS    int64          `json:"monotonic_ns"`
	ExperimentKind string         `json:"experiment_kind"`
	CaseID         *string        `json:"case_id"`
	PairID         *string        `json:"pair_id"`
	Seed           *uint32        `json:"seed"`
	Repetition     *int           `json:"repetition"`
	TrialOrder     *int           `json:"trial_order"`
	Treatment      *string        `json:"treatment"`
	Payload        map[string]any `json:"payload"`
}

func buildEvents(runID, experimentKind string, trials []trialObservation, started, finished time.Time) []Event {
	events := make([]Event, 0, len(trials)*2+3)
	appendEvent := func(event Event) {
		event.SchemaVersion = eventSchemaV1
		event.RunID = runID
		event.Sequence = len(events)
		event.ExperimentKind = experimentKind
		if event.Payload == nil {
			event.Payload = map[string]any{}
		}
		event.MonotonicNS = event.RecordedAt.Sub(started).Nanoseconds()
		if event.MonotonicNS < 0 {
			event.MonotonicNS = 0
		}
		event.RecordedAt = event.RecordedAt.UTC()
		events = append(events, event)
	}
	appendEvent(Event{EventType: "plan.declared", Status: "RUNNING", Phase: "planned", RecordedAt: started})
	appendEvent(Event{EventType: "run.started", Status: "RUNNING", Phase: "measurement", RecordedAt: started})
	for _, trial := range trials {
		caseID, pairID, seed := trial.CaseID, trial.PairID, trial.Seed
		repetition, order, treatment := trial.Repetition, trial.TrialOrder, trial.Treatment
		phase := trial.Phase
		if phase == "" {
			phase = "measurement"
		}
		base := Event{Phase: phase, CaseID: &caseID, PairID: &pairID, Seed: &seed, Repetition: &repetition, TrialOrder: &order, Treatment: &treatment}
		startedEvent := base
		startedEvent.EventType, startedEvent.Status, startedEvent.RecordedAt = "trial.started", "RUNNING", trial.StartedAt
		appendEvent(startedEvent)
		completedEvent := base
		status := trial.Status
		if status == "" {
			status = "SUCCEEDED"
		}
		completedEvent.EventType, completedEvent.Status, completedEvent.RecordedAt = "trial.completed", status, trial.FinishedAt
		appendEvent(completedEvent)
	}
	appendEvent(Event{EventType: "run.completed", Status: "SUCCEEDED", Phase: "measurement", RecordedAt: finished})
	return events
}

func writeEvents(writer io.Writer, events []Event) error {
	buffer := bufio.NewWriter(writer)
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(true)
	for index, event := range events {
		if event.Sequence != index {
			return fmt.Errorf("event sequence %d is not contiguous", event.Sequence)
		}
		if err := encoder.Encode(event); err != nil {
			return err
		}
	}
	return buffer.Flush()
}
