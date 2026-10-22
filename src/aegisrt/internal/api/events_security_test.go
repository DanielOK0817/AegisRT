package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisrt/internal/eventlog"
)

func TestEventsProjectsOnlyKnownSafeOutcomeFields(t *testing.T) {
	log := newAPIEventLog(t, 2)
	payload := json.RawMessage(`{
        "policy":"priority",
        "exit_code":0,
        "timed_out":false,
        "canceled":false,
        "attempt_status":"SUCCEEDED",
        "context":{
          "manifest_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
          "ref_count":2,
          "logical_tokens":20,
          "physical_tokens":12,
          "reused_tokens":8,
          "dropped_tokens":3,
          "summary_pages":1
        }
      }`)
	_, err := log.Append(eventlog.Event{
		Type: "run.succeeded", Time: apiEventStreamStart,
		WorkflowID: "workflow-1", TaskID: "task-1", AttemptID: "attempt-1", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{Events: log, AuthToken: []byte(apiToken)})
	response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events", "", apiToken, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Events []struct {
			ID      string                     `json:"id"`
			Time    string                     `json:"time"`
			Payload map[string]json.RawMessage `json:"payload"`
		} `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 1 || body.Events[0].ID != "1" || !strings.HasSuffix(body.Events[0].Time, "Z") {
		t.Fatalf("events = %+v", body.Events)
	}
	assertExactJSONKeys(t, body.Events[0].Payload, []string{
		"policy", "exit_code", "timed_out", "canceled", "attempt_status", "context",
	})
	var contextPayload map[string]json.RawMessage
	if err := json.Unmarshal(body.Events[0].Payload["context"], &contextPayload); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, contextPayload, []string{
		"manifest_id", "ref_count", "logical_tokens", "physical_tokens",
		"reused_tokens", "dropped_tokens", "summary_pages",
	})
}

func TestEventsFailsClosedWithoutEchoingUnsafePayload(t *testing.T) {
	const secret = "artifact-secret-must-not-enter-event-api"
	tests := []struct {
		name, eventType, payload string
	}{
		{name: "unknown event", eventType: "custom.event", payload: safeEventPayload()},
		{name: "unknown field", eventType: "run.failed", payload: `{"policy":"dag-drcfs","exit_code":1,"timed_out":false,"canceled":false,"attempt_status":"FAILED","artifact":"` + secret + `"}`},
		{name: "duplicate field", eventType: "run.failed", payload: `{"policy":"fifo","policy":"priority","exit_code":1,"timed_out":false,"canceled":false,"attempt_status":"FAILED"}`},
		{name: "nested unknown field", eventType: "run.succeeded", payload: `{"policy":"fifo","exit_code":0,"timed_out":false,"canceled":false,"attempt_status":"SUCCEEDED","context":{"manifest_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ref_count":0,"logical_tokens":0,"physical_tokens":0,"reused_tokens":0,"dropped_tokens":0,"summary_pages":0,"content":"` + secret + `"}}`},
		{name: "scalar", eventType: "run.failed", payload: `true`},
		{name: "array", eventType: "run.failed", payload: `[]`},
		{name: "null", eventType: "run.failed", payload: `null`},
		{name: "missing required field", eventType: "run.failed", payload: `{"policy":"fifo","timed_out":false,"canceled":false,"attempt_status":"FAILED"}`},
		{name: "untrusted policy", eventType: "run.failed", payload: `{"policy":"` + secret + `","exit_code":1,"timed_out":false,"canceled":false,"attempt_status":"FAILED"}`},
		{name: "nonterminal attempt", eventType: "run.failed", payload: `{"policy":"fifo","exit_code":1,"timed_out":false,"canceled":false,"attempt_status":"RUNNING"}`},
		{name: "mismatched terminal", eventType: "run.succeeded", payload: `{"policy":"fifo","exit_code":0,"timed_out":false,"canceled":false,"attempt_status":"FAILED"}`},
		{name: "mismatched signal", eventType: "run.succeeded", payload: `{"policy":"fifo","exit_code":0,"timed_out":true,"canceled":false,"attempt_status":"SUCCEEDED"}`},
		{name: "invalid manifest", eventType: "run.succeeded", payload: `{"policy":"fifo","exit_code":0,"timed_out":false,"canceled":false,"attempt_status":"SUCCEEDED","context":{"manifest_id":"not-a-manifest","ref_count":0,"logical_tokens":0,"physical_tokens":0,"reused_tokens":0,"dropped_tokens":0,"summary_pages":0}}`},
		{name: "negative metric", eventType: "run.succeeded", payload: `{"policy":"fifo","exit_code":0,"timed_out":false,"canceled":false,"attempt_status":"SUCCEEDED","context":{"manifest_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ref_count":-1,"logical_tokens":0,"physical_tokens":0,"reused_tokens":0,"dropped_tokens":0,"summary_pages":0}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := newAPIEventLog(t, 1)
			_, err := log.Append(eventlog.Event{
				Type: test.eventType, Time: apiEventStreamStart,
				WorkflowID: "workflow-1", TaskID: "task-1", AttemptID: "attempt-1",
				Payload: json.RawMessage(test.payload),
			})
			if err != nil {
				t.Fatal(err)
			}
			server := NewServer(Config{Events: log, AuthToken: []byte(apiToken)})
			response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events", "", apiToken, "")
			if response.Code != http.StatusInternalServerError ||
				!strings.Contains(response.Body.String(), "event_projection_failed") ||
				strings.Contains(response.Body.String(), secret) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestEventsFailsClosedOnUnassignedEventID(t *testing.T) {
	source := &fakeEventSource{
		stream: eventlog.Stream{ID: "stream-test", StartedAt: apiEventStreamStart},
		replay: eventlog.Replay{
			Events: []eventlog.Event{{
				Type: "run.succeeded", Time: apiEventStreamStart,
				WorkflowID: "workflow-1", TaskID: "task-1", AttemptID: "attempt-1",
				Payload: json.RawMessage(safeEventPayload()),
			}},
		},
	}
	server := NewServer(Config{Events: source, AuthToken: []byte(apiToken)})
	response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events", "", apiToken, "")
	if response.Code != http.StatusInternalServerError ||
		!strings.Contains(response.Body.String(), "event_projection_failed") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRunEventSemanticsAcceptsRuntimeOutcomeVariants(t *testing.T) {
	tests := []struct {
		eventType string
		payload   runEventPayloadDTO
	}{
		{eventType: "run.succeeded", payload: runEventPayloadDTO{AttemptStatus: "SUCCEEDED"}},
		{eventType: "run.failed", payload: runEventPayloadDTO{AttemptStatus: "FAILED"}},
		{eventType: "run.failed", payload: runEventPayloadDTO{}},
		{eventType: "run.canceled", payload: runEventPayloadDTO{AttemptStatus: "CANCELED", Canceled: true}},
		{eventType: "run.canceled", payload: runEventPayloadDTO{}},
		{eventType: "run.timed_out", payload: runEventPayloadDTO{AttemptStatus: "FAILED", TimedOut: true}},
		{eventType: "run.timed_out", payload: runEventPayloadDTO{}},
		{eventType: "run.interrupted", payload: runEventPayloadDTO{AttemptStatus: "LOST", Canceled: true}},
		{eventType: "run.interrupted", payload: runEventPayloadDTO{}},
	}
	for _, test := range tests {
		if !validRunEventSemantics(test.eventType, test.payload) {
			t.Errorf("rejected %s payload %+v", test.eventType, test.payload)
		}
	}
}

func safeEventPayload() string {
	return `{"policy":"fifo","exit_code":0,"timed_out":false,"canceled":false,"attempt_status":"SUCCEEDED"}`
}

func assertExactJSONKeys(t *testing.T, got map[string]json.RawMessage, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("keys=%v, want %v", got, want)
	}
	for _, key := range want {
		if _, exists := got[key]; !exists {
			t.Fatalf("missing key %q in %v", key, got)
		}
	}
}
