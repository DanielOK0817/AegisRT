package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aegisrt/internal/eventlog"
)

const (
	defaultEventLimit = 100
	maxEventLimit     = 256
	maxEventRawQuery  = 512
)

type eventQuery struct {
	afterID     uint64
	limit       int
	streamID    string
	hasStreamID bool
}

type eventReplayDTO struct {
	StreamID        string     `json:"stream_id"`
	StreamStartedAt time.Time  `json:"stream_started_at"`
	Retention       string     `json:"retention"`
	Ordering        string     `json:"ordering"`
	Events          []eventDTO `json:"events"`
	Gap             bool       `json:"gap"`
	GapFromID       string     `json:"gap_from_id,omitempty"`
	GapThroughID    string     `json:"gap_through_id,omitempty"`
	FirstAvailable  string     `json:"first_available_id,omitempty"`
	LastAvailable   string     `json:"last_available_id,omitempty"`
	NextAfterID     string     `json:"next_after_id"`
	HasMore         bool       `json:"has_more"`
}

func (s *Server) listEvents(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	if !s.requireAuthorization(writer, request) {
		return
	}
	query, err := parseEventQuery(request.URL.RawQuery)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_query", "event query parameters are invalid")
		return
	}
	if query.afterID > 0 && !query.hasStreamID {
		writeAPIError(writer, http.StatusBadRequest, "invalid_event_cursor", "stream_id is required with a non-zero after_id")
		return
	}
	if s.events == nil || !validEventStream(s.eventStream) {
		writeAPIError(writer, http.StatusServiceUnavailable, "events_unavailable", "runtime events are unavailable")
		return
	}
	if query.hasStreamID && query.streamID != s.eventStream.ID {
		s.writeEventCursorError(writer, "stale_event_cursor", "event cursor belongs to another runtime stream")
		return
	}

	replay, err := s.events.Since(query.afterID, query.limit)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "event_read_failed", "runtime events could not be read")
		return
	}
	if query.afterID > replay.LastAvailableID {
		s.writeEventCursorError(writer, "invalid_event_cursor", "event cursor is ahead of the current runtime stream")
		return
	}
	if validateEventReplay(query.afterID, query.limit, replay) != nil {
		writeAPIError(writer, http.StatusInternalServerError, "event_projection_failed", "runtime events could not be projected safely")
		return
	}
	response, err := projectReplay(s.eventStream, replay)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "event_projection_failed", "runtime events could not be projected safely")
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func parseEventQuery(rawQuery string) (eventQuery, error) {
	result := eventQuery{limit: defaultEventLimit}
	if len(rawQuery) > maxEventRawQuery {
		return eventQuery{}, eventlog.ErrInvalidLimit
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return eventQuery{}, err
	}
	for key, items := range values {
		if key != "after_id" && key != "limit" && key != "stream_id" || len(items) != 1 {
			return eventQuery{}, eventlog.ErrInvalidLimit
		}
	}
	if items, exists := values["after_id"]; exists {
		result.afterID, err = parseUintDecimal(items[0], 64)
		if err != nil {
			return eventQuery{}, err
		}
	}
	if items, exists := values["limit"]; exists {
		value, parseErr := parseUintDecimal(items[0], 16)
		if parseErr != nil || value == 0 || value > maxEventLimit {
			return eventQuery{}, eventlog.ErrInvalidLimit
		}
		result.limit = int(value)
	}
	if items, exists := values["stream_id"]; exists {
		if !validEventStreamID(items[0]) {
			return eventQuery{}, eventlog.ErrInvalidStream
		}
		result.streamID = items[0]
		result.hasStreamID = true
	}
	return result, nil
}

func parseUintDecimal(value string, bitSize int) (uint64, error) {
	if value == "" {
		return 0, eventlog.ErrInvalidLimit
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return 0, eventlog.ErrInvalidLimit
		}
	}
	return strconv.ParseUint(value, 10, bitSize)
}

func validEventStream(stream eventlog.Stream) bool {
	return validEventStreamID(stream.ID) && validJSONTime(stream.StartedAt)
}

func validJSONTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, err := value.UTC().MarshalJSON()
	return err == nil
}

func validEventStreamID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for index, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || index > 0 && (char == '-' || char == '_' || char == '.' || char == ':')
		if !valid {
			return false
		}
	}
	return true
}

func projectReplay(stream eventlog.Stream, replay eventlog.Replay) (eventReplayDTO, error) {
	result := eventReplayDTO{
		StreamID: stream.ID, StreamStartedAt: stream.StartedAt.UTC(),
		Retention: "process_memory", Ordering: "append_id",
		Events: make([]eventDTO, 0, len(replay.Events)), Gap: replay.Gap,
		FirstAvailable: formatEventID(replay.FirstAvailableID),
		LastAvailable:  formatEventID(replay.LastAvailableID),
		NextAfterID:    strconv.FormatUint(replay.NextAfterID, 10),
		HasMore:        replay.HasMore,
	}
	if replay.Gap {
		result.GapFromID = formatEventID(replay.GapFromID)
		result.GapThroughID = formatEventID(replay.GapThroughID)
	}
	for _, event := range replay.Events {
		projected, err := projectEvent(event)
		if err != nil {
			return eventReplayDTO{}, err
		}
		result.Events = append(result.Events, projected)
	}
	return result, nil
}

func formatEventID(id uint64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatUint(id, 10)
}

func (s *Server) writeEventCursorError(writer http.ResponseWriter, code, message string) {
	writeJSON(writer, http.StatusConflict, map[string]any{
		"error":             map[string]string{"code": code, "message": message},
		"stream_id":         s.eventStream.ID,
		"stream_started_at": s.eventStream.StartedAt.UTC(),
	})
}
