package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/eventlog"
)

func TestEventsReturnsBoundedGapAwareReplayWithStreamBoundCursor(t *testing.T) {
	log := newAPIEventLog(t, 3)
	for sequence := 1; sequence <= 5; sequence++ {
		appendAPIEvent(t, log, sequence)
	}
	server := NewServer(Config{Events: log, AuthToken: []byte(apiToken)})
	streamID := log.Stream().ID

	response := performAuthorizedRequest(
		server.Handler(), http.MethodGet, "/v1/events?limit=2", "", apiToken, "",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %v", response.Header())
	}
	body := decodeEventResponse(t, response)
	if body.StreamID != streamID || !body.StreamStartedAt.Equal(apiEventStreamStart) ||
		len(body.Events) != 2 || body.Events[0].ID != "3" || body.Events[1].ID != "4" ||
		!body.Gap || body.GapFromID != "1" || body.GapThroughID != "2" ||
		body.FirstAvailableID != "3" || body.LastAvailableID != "5" ||
		body.NextAfterID != "4" || !body.HasMore {
		t.Fatalf("replay = %+v", body)
	}
	if body.Events[0].Payload.Policy != "dag-drcfs" || body.Events[0].Payload.ExitCode != 3 {
		t.Fatalf("event payload = %+v", body.Events[0].Payload)
	}

	response = performAuthorizedRequest(
		server.Handler(), http.MethodGet,
		"/v1/events?after_id=4&limit=2&stream_id="+streamID, "", apiToken, "",
	)
	body = decodeEventResponse(t, response)
	if response.Code != http.StatusOK || len(body.Events) != 1 || body.Events[0].ID != "5" ||
		body.NextAfterID != "5" || body.HasMore || body.Gap {
		t.Fatalf("second replay status=%d body=%+v", response.Code, body)
	}
}

func TestEventsRejectsStaleMissingAndFutureStreamCursors(t *testing.T) {
	log := newAPIEventLog(t, 4)
	appendAPIEvent(t, log, 1)
	server := NewServer(Config{Events: log, AuthToken: []byte(apiToken)})
	streamID := log.Stream().ID
	tests := []struct {
		name, target, code string
		status             int
	}{
		{name: "missing stream", target: "/v1/events?after_id=1", status: http.StatusBadRequest, code: "invalid_event_cursor"},
		{name: "stale stream", target: "/v1/events?after_id=1&stream_id=old-stream", status: http.StatusConflict, code: "stale_event_cursor"},
		{name: "stale initial stream", target: "/v1/events?stream_id=old-stream", status: http.StatusConflict, code: "stale_event_cursor"},
		{name: "future cursor", target: "/v1/events?after_id=2&stream_id=" + streamID, status: http.StatusConflict, code: "invalid_event_cursor"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performAuthorizedRequest(server.Handler(), http.MethodGet, test.target, "", apiToken, "")
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.status == http.StatusConflict && !strings.Contains(response.Body.String(), `"stream_id":"`+streamID+`"`) {
				t.Fatalf("cursor conflict omitted current stream: %s", response.Body.String())
			}
		})
	}
}

func TestEventsRequiresAuthorizationBeforeReadingSource(t *testing.T) {
	source := &fakeEventSource{stream: eventlog.Stream{ID: "stream-test", StartedAt: apiEventStreamStart}}
	server := NewServer(Config{Events: source, AuthToken: []byte(apiToken)})
	for _, token := range []string{"", "wrong-token"} {
		response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events", "", token, "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("token %q status=%d", token, response.Code)
		}
	}
	if source.calls != 0 {
		t.Fatalf("unauthorized requests read source %d times", source.calls)
	}
}

func TestEventsStrictQueryValidation(t *testing.T) {
	source := &fakeEventSource{stream: eventlog.Stream{ID: "stream-test", StartedAt: apiEventStreamStart}}
	server := NewServer(Config{Events: source, AuthToken: []byte(apiToken)})
	targets := []string{
		"/v1/events?unknown=1", "/v1/events?limit=", "/v1/events?limit=0",
		"/v1/events?limit=257", "/v1/events?limit=1&limit=2", "/v1/events?after_id=",
		"/v1/events?after_id=-1", "/v1/events?after_id=%2B1", "/v1/events?after_id=1.0",
		"/v1/events?after_id=0x1", "/v1/events?after_id=18446744073709551616",
		"/v1/events?after_id=1;limit=2", "/v1/events?stream_id=",
		"/v1/events?stream_id=a&stream_id=b",
	}
	for _, target := range targets {
		response := performAuthorizedRequest(server.Handler(), http.MethodGet, target, "", apiToken, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	request.URL.RawQuery = "%zz"
	request.Header.Set("Authorization", "Bearer "+apiToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed query status=%d body=%s", response.Code, response.Body.String())
	}
	if source.calls != 0 {
		t.Fatalf("invalid queries read source %d times", source.calls)
	}
}

func TestEventsMapsUnavailableAndReadFailureWithoutLeaking(t *testing.T) {
	server := NewServer(Config{AuthToken: []byte(apiToken)})
	response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events", "", apiToken, "")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "events_unavailable") {
		t.Fatalf("unavailable status=%d body=%s", response.Code, response.Body.String())
	}

	const secret = "event-reader-secret"
	source := &fakeEventSource{
		stream: eventlog.Stream{ID: "stream-test", StartedAt: apiEventStreamStart},
		err:    errors.New(secret),
	}
	server = NewServer(Config{Events: source, AuthToken: []byte(apiToken)})
	response = performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events", "", apiToken, "")
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "event_read_failed") ||
		strings.Contains(response.Body.String(), secret) {
		t.Fatalf("read failure status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestEventsUsesDefaultBoundAndGETOnly(t *testing.T) {
	source := &fakeEventSource{stream: eventlog.Stream{ID: "stream-test", StartedAt: apiEventStreamStart}}
	server := NewServer(Config{Events: source, AuthToken: []byte(apiToken)})
	response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events", "", apiToken, "")
	if response.Code != http.StatusOK || source.afterID != 0 || source.limit != 100 {
		t.Fatalf("default replay status=%d after=%d limit=%d body=%s", response.Code, source.afterID, source.limit, response.Body.String())
	}
	var empty struct {
		Events      []json.RawMessage `json:"events"`
		NextAfterID string            `json:"next_after_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&empty); err != nil {
		t.Fatal(err)
	}
	if empty.Events == nil || len(empty.Events) != 0 || empty.NextAfterID != "0" {
		t.Fatalf("empty replay = %+v", empty)
	}
	response = performAuthorizedRequest(server.Handler(), http.MethodPost, "/v1/events", "", apiToken, "")
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST status=%d Allow=%q", response.Code, response.Header().Get("Allow"))
	}
}

func TestEventsAcceptsMaximumCursorAndPageBound(t *testing.T) {
	source := &fakeEventSource{
		stream: eventlog.Stream{ID: "stream-test", StartedAt: apiEventStreamStart},
		replay: eventlog.Replay{
			FirstAvailableID: math.MaxUint64, LastAvailableID: math.MaxUint64,
			NextAfterID: math.MaxUint64,
		},
	}
	server := NewServer(Config{Events: source, AuthToken: []byte(apiToken)})
	target := "/v1/events?after_id=18446744073709551615&limit=256&stream_id=stream-test"
	response := performAuthorizedRequest(server.Handler(), http.MethodGet, target, "", apiToken, "")
	if response.Code != http.StatusOK || source.afterID != math.MaxUint64 || source.limit != maxEventLimit {
		t.Fatalf("status=%d after=%d limit=%d body=%s", response.Code, source.afterID, source.limit, response.Body.String())
	}
}

func TestRuntimeStartedAtUsesEventStreamEpoch(t *testing.T) {
	log := newAPIEventLog(t, 1)
	server := NewServer(Config{
		Events: log, StartedAt: apiEventStreamStart.Add(-time.Hour), AuthToken: []byte(apiToken),
	})
	response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/runtime", "", "", "")
	var body struct {
		StartedAt time.Time `json:"started_at"`
	}
	if response.Code != http.StatusOK || json.NewDecoder(response.Body).Decode(&body) != nil ||
		!body.StartedAt.Equal(log.Stream().StartedAt) {
		t.Fatalf("runtime status=%d started_at=%s stream=%+v", response.Code, body.StartedAt, log.Stream())
	}
}

func TestEventsRejectsSourceThatExceedsRequestedPageBound(t *testing.T) {
	events := []eventlog.Event{
		{ID: 1, Type: "run.succeeded", Time: apiEventStreamStart, Payload: json.RawMessage(safeEventPayload())},
		{ID: 2, Type: "run.succeeded", Time: apiEventStreamStart, Payload: json.RawMessage(safeEventPayload())},
	}
	source := &fakeEventSource{
		stream: eventlog.Stream{ID: "stream-test", StartedAt: apiEventStreamStart},
		replay: eventlog.Replay{Events: events, FirstAvailableID: 1, LastAvailableID: 2, NextAfterID: 2},
	}
	server := NewServer(Config{Events: source, AuthToken: []byte(apiToken)})
	response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events?limit=1", "", apiToken, "")
	if response.Code != http.StatusInternalServerError ||
		!strings.Contains(response.Body.String(), "event_projection_failed") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

var apiEventStreamStart = time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)

type fakeEventSource struct {
	stream  eventlog.Stream
	replay  eventlog.Replay
	err     error
	calls   int
	afterID uint64
	limit   int
}

func (f *fakeEventSource) Stream() eventlog.Stream {
	if f == nil {
		return eventlog.Stream{}
	}
	return f.stream
}

func (f *fakeEventSource) Since(afterID uint64, limit int) (eventlog.Replay, error) {
	f.calls++
	f.afterID, f.limit = afterID, limit
	return f.replay, f.err
}

type eventResponse struct {
	StreamID        string    `json:"stream_id"`
	StreamStartedAt time.Time `json:"stream_started_at"`
	Events          []struct {
		ID      string `json:"id"`
		Payload struct {
			Policy   string `json:"policy"`
			ExitCode int    `json:"exit_code"`
		} `json:"payload"`
	} `json:"events"`
	Gap              bool   `json:"gap"`
	GapFromID        string `json:"gap_from_id"`
	GapThroughID     string `json:"gap_through_id"`
	FirstAvailableID string `json:"first_available_id"`
	LastAvailableID  string `json:"last_available_id"`
	NextAfterID      string `json:"next_after_id"`
	HasMore          bool   `json:"has_more"`
}

func decodeEventResponse(t *testing.T, response *httptest.ResponseRecorder) eventResponse {
	t.Helper()
	var body eventResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func newAPIEventLog(t *testing.T, capacity int) *eventlog.Log {
	t.Helper()
	log, err := eventlog.NewWithOptions(eventlog.Options{
		Capacity: capacity, MaxPayloadBytes: eventlog.DefaultMaxPayloadBytes,
		StartedAt: apiEventStreamStart,
	})
	if err != nil {
		t.Fatal(err)
	}
	return log
}

func appendAPIEvent(t *testing.T, log *eventlog.Log, sequence int) {
	t.Helper()
	payload := json.RawMessage(fmt.Sprintf(
		`{"policy":"dag-drcfs","exit_code":%d,"timed_out":false,"canceled":false,"attempt_status":"SUCCEEDED"}`,
		sequence,
	))
	_, err := log.Append(eventlog.Event{
		Type: "run.succeeded", Time: apiEventStreamStart.Add(time.Duration(sequence) * time.Second),
		WorkflowID: "workflow-1", TaskID: fmt.Sprintf("task-%d", sequence),
		AttemptID: fmt.Sprintf("attempt-%d", sequence), Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
}
