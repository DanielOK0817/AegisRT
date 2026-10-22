package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/eventlog"
)

func TestEventsRejectsInconsistentReplayMetadata(t *testing.T) {
	tests := []struct {
		name   string
		replay eventlog.Replay
	}{
		{
			name: "missing first id",
			replay: eventlog.Replay{
				Events: []eventlog.Event{safeReplayEvent(1)}, LastAvailableID: 1, NextAfterID: 1,
			},
		},
		{
			name: "non-contiguous ids",
			replay: eventlog.Replay{
				Events:           []eventlog.Event{safeReplayEvent(1), safeReplayEvent(3)},
				FirstAvailableID: 1, LastAvailableID: 3, NextAfterID: 3, HasMore: true,
			},
		},
		{
			name: "wrong next cursor",
			replay: eventlog.Replay{
				Events:           []eventlog.Event{safeReplayEvent(1)},
				FirstAvailableID: 1, LastAvailableID: 1,
			},
		},
		{
			name: "missing eviction gap",
			replay: eventlog.Replay{
				Events:           []eventlog.Event{safeReplayEvent(3)},
				FirstAvailableID: 3, LastAvailableID: 3, NextAfterID: 3,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &fakeEventSource{
				stream: eventlog.Stream{ID: "stream-test", StartedAt: apiEventStreamStart}, replay: test.replay,
			}
			server := NewServer(Config{Events: source, AuthToken: []byte(apiToken)})
			response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events?limit=2", "", apiToken, "")
			if response.Code != http.StatusInternalServerError ||
				!strings.Contains(response.Body.String(), "event_projection_failed") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestEventsRejectsTimesThatCannotBeEncodedAsJSON(t *testing.T) {
	invalidTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Run("stream", func(t *testing.T) {
		source := &fakeEventSource{stream: eventlog.Stream{ID: "stream-test", StartedAt: invalidTime}}
		server := NewServer(Config{Events: source, AuthToken: []byte(apiToken)})
		response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events", "", apiToken, "")
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("event", func(t *testing.T) {
		event := safeReplayEvent(1)
		event.Time = invalidTime
		source := &fakeEventSource{
			stream: eventlog.Stream{ID: "stream-test", StartedAt: apiEventStreamStart},
			replay: eventlog.Replay{
				Events: []eventlog.Event{event}, FirstAvailableID: 1, LastAvailableID: 1, NextAfterID: 1,
			},
		}
		server := NewServer(Config{Events: source, AuthToken: []byte(apiToken)})
		response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/events", "", apiToken, "")
		if response.Code != http.StatusInternalServerError ||
			!strings.Contains(response.Body.String(), "event_projection_failed") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func safeReplayEvent(id uint64) eventlog.Event {
	return eventlog.Event{
		ID: id, Type: "run.succeeded", Time: apiEventStreamStart,
		Payload: json.RawMessage(safeEventPayload()),
	}
}
