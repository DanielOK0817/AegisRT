package api

import (
	"encoding/json"
	"time"

	"aegisrt/internal/eventlog"
)

func decodeScheduling(raw json.RawMessage) (*eventlog.SchedulingPayload, error) {
	p := &eventlog.SchedulingPayload{}
	handlers := scalarHandlers(map[string]any{
		"priority": &p.Priority, "inherited_priority": &p.InheritedPriority,
		"critical_path": &p.CriticalPath, "unlock_count": &p.UnlockCount,
		"workflow_vruntime": &p.WorkflowVRuntime, "workflow_reserved_charge": &p.WorkflowReservedCharge,
		"dominant_share": &p.DominantShare,
	})
	seen, err := decodeStrictObject(raw, handlers)
	if err != nil || len(seen) != len(handlers) || !p.Valid() {
		return nil, errInvalidEventProjection
	}
	return p, nil
}

func decodeLifecycle(raw json.RawMessage) (*eventlog.LifecyclePayload, error) {
	p := &eventlog.LifecyclePayload{}
	handlers := scalarHandlers(map[string]any{"phase": &p.Phase})
	for key, target := range map[string]**time.Time{
		"preparation_started_at": &p.PreparationStartedAt,
		"process_started_at":     &p.ProcessStartedAt, "wait_completed_at": &p.WaitCompletedAt,
	} {
		handlers[key] = func(value json.RawMessage) error {
			var timestamp time.Time
			if err := decodeScalar(value, &timestamp); err != nil || !validJSONTime(timestamp) {
				return errInvalidEventProjection
			}
			timestamp = timestamp.UTC()
			*target = &timestamp
			return nil
		}
	}
	seen, err := decodeStrictObject(raw, handlers)
	if err != nil || !seen["phase"] {
		return nil, errInvalidEventProjection
	}
	return p, nil
}

func decodeOutputMetrics(raw json.RawMessage) (*eventlog.OutputPayload, error) {
	p := &eventlog.OutputPayload{}
	handlers := scalarHandlers(map[string]any{"artifact_count": &p.ArtifactCount, "content_bytes": &p.ContentBytes})
	seen, err := decodeStrictObject(raw, handlers)
	if err != nil || len(seen) != len(handlers) || !p.Valid() {
		return nil, errInvalidEventProjection
	}
	return p, nil
}

func scalarHandlers(fields map[string]any) map[string]func(json.RawMessage) error {
	handlers := make(map[string]func(json.RawMessage) error, len(fields))
	for name, target := range fields {
		handlers[name] = func(raw json.RawMessage) error { return decodeScalar(raw, target) }
	}
	return handlers
}
