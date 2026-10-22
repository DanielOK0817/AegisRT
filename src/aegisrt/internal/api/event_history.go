package api

import (
	"strconv"
	"time"

	"aegisrt/internal/eventlog"
)

type eventHistoryDTO struct {
	Retention string          `json:"retention"`
	Ordering  string          `json:"ordering"`
	StreamID  string          `json:"stream_id"`
	StartedAt time.Time       `json:"started_at"`
	Window    *eventWindowDTO `json:"window,omitempty"`
}

type eventWindowDTO struct {
	Capacity      int    `json:"capacity"`
	Retained      int    `json:"retained"`
	FirstID       string `json:"first_id"`
	LastID        string `json:"last_id"`
	TotalAppended string `json:"total_appended"`
	Evicted       string `json:"evicted"`
}

func (s *Server) eventHistory() *eventHistoryDTO {
	if s.events == nil || !validEventStream(s.eventStream) {
		return nil
	}
	history := &eventHistoryDTO{Retention: "process_memory", Ordering: "append_id", StreamID: s.eventStream.ID, StartedAt: s.eventStream.StartedAt}
	if source, ok := s.events.(interface{ Stats() eventlog.Stats }); ok {
		stats := source.Stats()
		history.Window = &eventWindowDTO{
			Capacity: stats.Capacity, Retained: stats.Retained,
			FirstID: strconv.FormatUint(stats.FirstID, 10), LastID: strconv.FormatUint(stats.LastID, 10),
			TotalAppended: strconv.FormatUint(stats.TotalAppended, 10), Evicted: strconv.FormatUint(stats.Evicted, 10),
		}
	}
	return history
}
