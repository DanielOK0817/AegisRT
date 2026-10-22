package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestEventsProjectsSchedulingLifecycleAndOutputDetails(t *testing.T) {
	extra := `,"scheduling":{"priority":2,"inherited_priority":4,"critical_path":3,"unlock_count":2,"workflow_vruntime":1.25,"workflow_reserved_charge":0.5,"dominant_share":0.25},"lifecycle":{"phase":"process_exited","preparation_started_at":"2026-07-20T09:59:57Z","process_started_at":"2026-07-20T09:59:58Z","wait_completed_at":"2026-07-20T09:59:59Z"},"output":{"artifact_count":1,"content_bytes":14}`
	event := timingProjectionEvent(strings.TrimSuffix(safeEventPayload(), "}") + extra + "}")
	projected, err := projectEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	p := projected.Payload
	if p.Scheduling == nil || p.Scheduling.WorkflowReservedCharge != 0.5 ||
		p.Lifecycle == nil || p.Lifecycle.Phase != "process_exited" ||
		p.Output == nil || p.Output.ContentBytes != 14 {
		t.Fatalf("payload = %+v", p)
	}
}

func TestEventsRejectsUnsafeOrInconsistentNewDetails(t *testing.T) {
	for _, extra := range []string{
		`"scheduling":{"priority":1,"reason":"secret"}`,
		`"scheduling":{"priority":1,"priority":2,"inherited_priority":1,"critical_path":1,"unlock_count":0,"workflow_vruntime":0,"workflow_reserved_charge":0,"dominant_share":0}`,
		`"scheduling":{"priority":1,"inherited_priority":1,"critical_path":1,"unlock_count":0,"workflow_vruntime":0,"workflow_reserved_charge":-1,"dominant_share":0}`,
		`"lifecycle":{"phase":"process_exited","process_started_at":"2026-07-20T09:59:58Z"}`,
		`"lifecycle":{"phase":"before_start","preparation_started_at":"2026-07-20T10:10:00Z"}`,
		`"lifecycle":{"phase":"before_start","preparation_started_at":null}`,
		`"output":{"artifact_count":1,"content_bytes":1,"content":"secret"}`,
		`"output":{"artifact_count":0,"content_bytes":1}`,
	} {
		event := timingProjectionEvent(strings.TrimSuffix(safeEventPayload(), "}") + "," + extra + "}")
		if _, err := projectEvent(event); err != errInvalidEventProjection {
			t.Fatalf("accepted %s: %v", extra, err)
		}
	}
	event := timingProjectionEvent(`{"policy":"fifo","exit_code":1,"timed_out":false,"canceled":false,"attempt_status":"FAILED","output":{"artifact_count":1,"content_bytes":1}}`)
	event.Type = "run.failed"
	if _, err := projectEvent(event); err != errInvalidEventProjection {
		t.Fatalf("failed output accepted: %v", err)
	}
}

func TestRuntimeReportsMemoryEventWindowAndRestartBoundary(t *testing.T) {
	first := newAPIEventLog(t, 2)
	for i := 1; i <= 3; i++ {
		appendAPIEvent(t, first, i)
	}
	server := NewServer(Config{Events: first, AuthToken: []byte(apiToken)})
	response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/runtime", "", apiToken, "")
	var payload struct {
		History eventHistoryDTO `json:"event_history"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	h := payload.History
	if h.Retention != "process_memory" || h.Ordering != "append_id" || h.Window == nil ||
		h.Window.Evicted != "1" || h.Window.FirstID != "2" || h.Window.LastID != "3" || h.Window.Retained != 2 {
		t.Fatalf("history = %+v", h)
	}
	second := newAPIEventLog(t, 2)
	restarted := NewServer(Config{Events: second, AuthToken: []byte(apiToken)})
	response = performAuthorizedRequest(restarted.Handler(), http.MethodGet, "/v1/events?after_id=3&stream_id="+first.Stream().ID, "", apiToken, "")
	if response.Code != http.StatusConflict {
		t.Fatalf("stale cursor accepted: %d", response.Code)
	}
	response = performAuthorizedRequest(restarted.Handler(), http.MethodGet, "/v1/events", "", apiToken, "")
	var replay eventReplayDTO
	if err := json.Unmarshal(response.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Retention != "process_memory" || replay.Ordering != "append_id" || len(replay.Events) != 0 || replay.StreamID == first.Stream().ID {
		t.Fatalf("restarted history = %+v", replay)
	}
}
