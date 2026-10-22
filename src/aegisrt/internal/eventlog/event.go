package eventlog

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const maxIdentifierBytes = 128

// Event is one immutable runtime fact. ID is assigned by Log.Append.
type Event struct {
	ID         uint64          `json:"id"`
	Type       string          `json:"type"`
	Time       time.Time       `json:"time"`
	WorkflowID string          `json:"workflow_id,omitempty"`
	TaskID     string          `json:"task_id,omitempty"`
	AttemptID  string          `json:"attempt_id,omitempty"`
	Payload    json.RawMessage `json:"payload"`
}

func (e Event) Validate() error {
	if err := validateIdentifier("type", e.Type, false); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	if e.Time.IsZero() {
		return fmt.Errorf("%w: time is required", ErrInvalidEvent)
	}
	for _, item := range []struct {
		name  string
		value string
	}{
		{"workflow_id", e.WorkflowID},
		{"task_id", e.TaskID},
		{"attempt_id", e.AttemptID},
	} {
		if err := validateIdentifier(item.name, item.value, true); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidEvent, err)
		}
	}
	if e.TaskID != "" && e.WorkflowID == "" {
		return fmt.Errorf("%w: task_id requires workflow_id", ErrInvalidEvent)
	}
	if e.AttemptID != "" && e.TaskID == "" {
		return fmt.Errorf("%w: attempt_id requires task_id", ErrInvalidEvent)
	}
	if len(e.Payload) == 0 || !json.Valid(e.Payload) {
		return fmt.Errorf("%w: %w: payload must contain one valid JSON value", ErrInvalidEvent, ErrInvalidPayload)
	}
	return nil
}

func validateIdentifier(field, value string, optional bool) error {
	if value == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%w: %s is required", ErrInvalidIdentifier, field)
	}
	if strings.TrimSpace(value) != value || len(value) > maxIdentifierBytes {
		return fmt.Errorf("%w: %s has invalid length or whitespace", ErrInvalidIdentifier, field)
	}
	for index, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || index > 0 && (char == '-' || char == '_' || char == '.' || char == ':')
		if !valid {
			return fmt.Errorf("%w: %s contains invalid character %q", ErrInvalidIdentifier, field, char)
		}
	}
	return nil
}

func cloneEvent(event Event) Event {
	result := event
	result.Payload = append(json.RawMessage(nil), event.Payload...)
	return result
}
