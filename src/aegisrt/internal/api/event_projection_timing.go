package api

import (
	"encoding/json"
	"time"
)

type eventTimingDTO struct {
	ReadyAt     time.Time `json:"ready_at"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	QueueMillis int64     `json:"queue_ms"`
	RunMillis   int64     `json:"run_ms"`
}

func decodeEventTiming(raw json.RawMessage) (*eventTimingDTO, error) {
	timing := &eventTimingDTO{}
	handlers := map[string]func(json.RawMessage) error{
		"ready_at": func(value json.RawMessage) error {
			return decodeScalar(value, &timing.ReadyAt)
		},
		"started_at": func(value json.RawMessage) error {
			return decodeScalar(value, &timing.StartedAt)
		},
		"finished_at": func(value json.RawMessage) error {
			return decodeScalar(value, &timing.FinishedAt)
		},
		"queue_ms": func(value json.RawMessage) error {
			return decodeScalar(value, &timing.QueueMillis)
		},
		"run_ms": func(value json.RawMessage) error {
			return decodeScalar(value, &timing.RunMillis)
		},
	}
	seen, err := decodeStrictObject(raw, handlers)
	if err != nil || len(seen) != len(handlers) ||
		!validJSONTime(timing.ReadyAt) || !validJSONTime(timing.StartedAt) ||
		!validJSONTime(timing.FinishedAt) || timing.StartedAt.Before(timing.ReadyAt) ||
		timing.FinishedAt.Before(timing.StartedAt) ||
		timing.QueueMillis != timing.StartedAt.Sub(timing.ReadyAt).Milliseconds() ||
		timing.RunMillis != timing.FinishedAt.Sub(timing.StartedAt).Milliseconds() {
		return nil, errInvalidEventProjection
	}
	timing.ReadyAt = timing.ReadyAt.UTC()
	timing.StartedAt = timing.StartedAt.UTC()
	timing.FinishedAt = timing.FinishedAt.UTC()
	return timing, nil
}
