package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/eventlog"
)

var errInvalidEventProjection = errors.New("invalid event projection")

type eventDTO struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Time       time.Time          `json:"time"`
	WorkflowID string             `json:"workflow_id,omitempty"`
	TaskID     string             `json:"task_id,omitempty"`
	AttemptID  string             `json:"attempt_id,omitempty"`
	Payload    runEventPayloadDTO `json:"payload"`
}

type runEventPayloadDTO struct {
	Policy        string                      `json:"policy,omitempty"`
	ExitCode      int                         `json:"exit_code"`
	TimedOut      bool                        `json:"timed_out"`
	Canceled      bool                        `json:"canceled"`
	AttemptStatus string                      `json:"attempt_status,omitempty"`
	Context       *eventContextDTO            `json:"context,omitempty"`
	Timing        *eventTimingDTO             `json:"timing,omitempty"`
	Scheduling    *eventlog.SchedulingPayload `json:"scheduling,omitempty"`
	Lifecycle     *eventlog.LifecyclePayload  `json:"lifecycle,omitempty"`
	Output        *eventlog.OutputPayload     `json:"output,omitempty"`
}

type eventContextDTO struct {
	ManifestID     string `json:"manifest_id"`
	RefCount       int    `json:"ref_count"`
	LogicalTokens  int    `json:"logical_tokens"`
	PhysicalTokens int    `json:"physical_tokens"`
	ReusedTokens   int    `json:"reused_tokens"`
	DroppedTokens  int    `json:"dropped_tokens"`
	SummaryPages   int    `json:"summary_pages"`
}

func projectEvent(event eventlog.Event) (eventDTO, error) {
	if event.ID == 0 || !validJSONTime(event.Time) ||
		!supportedRunEventType(event.Type) || event.Validate() != nil {
		return eventDTO{}, errInvalidEventProjection
	}
	payload, err := decodeRunEventPayload(event.Payload)
	if err != nil {
		return eventDTO{}, errInvalidEventProjection
	}
	if !validRunEventSemantics(event.Type, payload) ||
		payload.Lifecycle != nil && !payload.Lifecycle.Valid(event.Time) ||
		payload.Lifecycle != nil && event.Type == "run.succeeded" && payload.Lifecycle.Phase != "process_exited" ||
		payload.Lifecycle != nil && payload.Lifecycle.Phase == "before_start" && payload.Timing != nil ||
		payload.Output != nil && event.Type != "run.succeeded" ||
		payload.Timing != nil && (event.AttemptID == "" || payload.AttemptStatus == "" ||
			event.Time.Before(payload.Timing.FinishedAt)) {
		return eventDTO{}, errInvalidEventProjection
	}
	return eventDTO{
		ID: strconv.FormatUint(event.ID, 10), Type: event.Type, Time: event.Time.UTC(),
		WorkflowID: event.WorkflowID, TaskID: event.TaskID, AttemptID: event.AttemptID,
		Payload: payload,
	}, nil
}

func validRunEventSemantics(eventType string, payload runEventPayloadDTO) bool {
	if payload.TimedOut && payload.Canceled {
		return false
	}
	expectedStatus := domain.AttemptStatus("")
	switch eventType {
	case "run.succeeded":
		expectedStatus = domain.AttemptStatusSucceeded
		if payload.TimedOut || payload.Canceled {
			return false
		}
	case "run.failed":
		expectedStatus = domain.AttemptStatusFailed
		if payload.TimedOut || payload.Canceled {
			return false
		}
	case "run.canceled":
		expectedStatus = domain.AttemptStatusCanceled
		if payload.TimedOut {
			return false
		}
	case "run.timed_out":
		expectedStatus = domain.AttemptStatusFailed
		if payload.Canceled {
			return false
		}
	case "run.interrupted":
		expectedStatus = domain.AttemptStatusLost
		if payload.TimedOut {
			return false
		}
	default:
		return false
	}
	if payload.AttemptStatus == "" {
		return eventType != "run.succeeded"
	}
	return domain.AttemptStatus(payload.AttemptStatus) == expectedStatus
}

func supportedRunEventType(eventType string) bool {
	switch eventType {
	case "run.succeeded", "run.failed", "run.canceled", "run.timed_out", "run.interrupted":
		return true
	default:
		return false
	}
}

func decodeRunEventPayload(raw json.RawMessage) (runEventPayloadDTO, error) {
	var payload runEventPayloadDTO
	handlers := map[string]func(json.RawMessage) error{
		"policy": func(value json.RawMessage) error {
			return decodeScalar(value, &payload.Policy)
		},
		"exit_code": func(value json.RawMessage) error {
			return decodeScalar(value, &payload.ExitCode)
		},
		"timed_out": func(value json.RawMessage) error {
			return decodeScalar(value, &payload.TimedOut)
		},
		"canceled": func(value json.RawMessage) error {
			return decodeScalar(value, &payload.Canceled)
		},
		"attempt_status": func(value json.RawMessage) error {
			return decodeScalar(value, &payload.AttemptStatus)
		},
		"context": func(value json.RawMessage) error {
			contextPayload, err := decodeEventContext(value)
			payload.Context = contextPayload
			return err
		},
		"timing": func(value json.RawMessage) error {
			timing, err := decodeEventTiming(value)
			payload.Timing = timing
			return err
		},
		"scheduling": func(value json.RawMessage) error {
			var err error
			payload.Scheduling, err = decodeScheduling(value)
			return err
		},
		"lifecycle": func(value json.RawMessage) error {
			var err error
			payload.Lifecycle, err = decodeLifecycle(value)
			return err
		},
		"output": func(value json.RawMessage) error {
			var err error
			payload.Output, err = decodeOutputMetrics(value)
			return err
		},
	}
	seen, err := decodeStrictObject(raw, handlers)
	if err != nil || !seen["exit_code"] || !seen["timed_out"] || !seen["canceled"] {
		return runEventPayloadDTO{}, errInvalidEventProjection
	}
	if payload.Policy != "" && payload.Policy != "fifo" &&
		payload.Policy != "priority" && payload.Policy != "dag-drcfs" {
		return runEventPayloadDTO{}, errInvalidEventProjection
	}
	if payload.AttemptStatus != "" {
		status := domain.AttemptStatus(payload.AttemptStatus)
		if !status.IsValid() || !status.IsTerminal() {
			return runEventPayloadDTO{}, errInvalidEventProjection
		}
	}
	return payload, nil
}

func decodeEventContext(raw json.RawMessage) (*eventContextDTO, error) {
	contextPayload := &eventContextDTO{}
	handlers := map[string]func(json.RawMessage) error{
		"manifest_id": func(value json.RawMessage) error {
			return decodeScalar(value, &contextPayload.ManifestID)
		},
		"ref_count": func(value json.RawMessage) error {
			return decodeScalar(value, &contextPayload.RefCount)
		},
		"logical_tokens": func(value json.RawMessage) error {
			return decodeScalar(value, &contextPayload.LogicalTokens)
		},
		"physical_tokens": func(value json.RawMessage) error {
			return decodeScalar(value, &contextPayload.PhysicalTokens)
		},
		"reused_tokens": func(value json.RawMessage) error {
			return decodeScalar(value, &contextPayload.ReusedTokens)
		},
		"dropped_tokens": func(value json.RawMessage) error {
			return decodeScalar(value, &contextPayload.DroppedTokens)
		},
		"summary_pages": func(value json.RawMessage) error {
			return decodeScalar(value, &contextPayload.SummaryPages)
		},
	}
	seen, err := decodeStrictObject(raw, handlers)
	if err != nil || len(seen) != len(handlers) || !validEventManifestID(contextPayload.ManifestID) ||
		contextPayload.RefCount < 0 || contextPayload.LogicalTokens < 0 ||
		contextPayload.PhysicalTokens < 0 || contextPayload.ReusedTokens < 0 ||
		contextPayload.DroppedTokens < 0 || contextPayload.SummaryPages < 0 {
		return nil, errInvalidEventProjection
	}
	return contextPayload, nil
}

func validEventManifestID(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func decodeStrictObject(
	raw json.RawMessage,
	handlers map[string]func(json.RawMessage) error,
) (map[string]bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	opening, ok := token.(json.Delim)
	if err != nil || !ok || opening != '{' {
		return nil, errInvalidEventProjection
	}
	seen := make(map[string]bool, len(handlers))
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		handler, known := handlers[key]
		if err != nil || !ok || !known || seen[key] {
			return nil, errInvalidEventProjection
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || handler(value) != nil {
			return nil, errInvalidEventProjection
		}
	}
	token, err = decoder.Token()
	closing, ok := token.(json.Delim)
	if err != nil || !ok || closing != '}' {
		return nil, errInvalidEventProjection
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errInvalidEventProjection
	}
	return seen, nil
}

func decodeScalar(raw json.RawMessage, target any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errInvalidEventProjection
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return errInvalidEventProjection
	}
	return nil
}
