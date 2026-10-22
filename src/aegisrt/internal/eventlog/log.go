package eventlog

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sync"
	"time"
)

const DefaultMaxPayloadBytes = 1 << 20

type Options struct {
	Capacity        int
	MaxPayloadBytes int
	StartedAt       time.Time
}

type Stream struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
}

type Stats struct {
	Capacity        int    `json:"capacity"`
	MaxPayloadBytes int    `json:"max_payload_bytes"`
	Retained        int    `json:"retained"`
	TotalAppended   uint64 `json:"total_appended"`
	Evicted         uint64 `json:"evicted"`
	FirstID         uint64 `json:"first_id"`
	LastID          uint64 `json:"last_id"`
}

type Replay struct {
	Events           []Event `json:"events"`
	Gap              bool    `json:"gap"`
	GapFromID        uint64  `json:"gap_from_id,omitempty"`
	GapThroughID     uint64  `json:"gap_through_id,omitempty"`
	FirstAvailableID uint64  `json:"first_available_id,omitempty"`
	LastAvailableID  uint64  `json:"last_available_id,omitempty"`
	NextAfterID      uint64  `json:"next_after_id"`
	HasMore          bool    `json:"has_more"`
}

// Log is a bounded, process-local, append-only runtime event log.
type Log struct {
	mu              sync.RWMutex
	buffer          []Event
	start           int
	size            int
	lastID          uint64
	totalAppended   uint64
	evicted         uint64
	maxPayloadBytes int
	stream          Stream
}

func New(capacity int) (*Log, error) {
	return NewWithOptions(Options{Capacity: capacity, MaxPayloadBytes: DefaultMaxPayloadBytes})
}

func NewWithOptions(options Options) (*Log, error) {
	if options.Capacity <= 0 {
		return nil, fmt.Errorf("%w: capacity must be positive", ErrInvalidCapacity)
	}
	if options.MaxPayloadBytes <= 0 {
		return nil, fmt.Errorf("%w: max_payload_bytes must be positive", ErrInvalidCapacity)
	}
	stream, err := newStream(options.StartedAt)
	if err != nil {
		return nil, err
	}
	return &Log{
		buffer:          make([]Event, options.Capacity),
		maxPayloadBytes: options.MaxPayloadBytes,
		stream:          stream,
	}, nil
}

func newStream(startedAt time.Time) (Stream, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return Stream{}, fmt.Errorf("%w: generate id", ErrInvalidStream)
	}
	id := hex.EncodeToString(value)
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	} else {
		startedAt = startedAt.UTC()
	}
	return Stream{ID: id, StartedAt: startedAt}, nil
}

func (l *Log) Stream() Stream {
	if l == nil {
		return Stream{}
	}
	return l.stream
}

func (l *Log) Append(event Event) (Event, error) {
	if l == nil {
		return Event{}, fmt.Errorf("%w: log is nil", ErrInvalidCapacity)
	}
	event = cloneEvent(event)
	if event.ID != 0 {
		return Event{}, fmt.Errorf("%w: got %d", ErrEventIDAssigned, event.ID)
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	if len(event.Payload) > l.maxPayloadBytes {
		return Event{}, fmt.Errorf("%w: got %d bytes, maximum %d", ErrPayloadTooLarge, len(event.Payload), l.maxPayloadBytes)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastID == math.MaxUint64 {
		return Event{}, ErrIDExhausted
	}
	event.ID = l.lastID + 1
	if l.size < len(l.buffer) {
		index := (l.start + l.size) % len(l.buffer)
		l.buffer[index] = event
		l.size++
	} else {
		l.buffer[l.start] = event
		l.start = (l.start + 1) % len(l.buffer)
		l.evicted++
	}
	l.lastID = event.ID
	l.totalAppended++
	return cloneEvent(event), nil
}

// Since returns events with ID greater than afterID, in ascending order. Gap
// identifies the exact missing range when requested events were evicted.
func (l *Log) Since(afterID uint64, limit int) (Replay, error) {
	if l == nil {
		return Replay{}, fmt.Errorf("%w: log is nil", ErrInvalidCapacity)
	}
	if limit <= 0 {
		return Replay{}, fmt.Errorf("%w: limit must be positive", ErrInvalidLimit)
	}
	l.mu.RLock()
	defer l.mu.RUnlock()

	replay := Replay{NextAfterID: afterID, LastAvailableID: l.lastID}
	if l.size == 0 {
		return replay, nil
	}
	firstID := l.buffer[l.start].ID
	replay.FirstAvailableID = firstID
	if afterID < firstID-1 {
		replay.Gap = true
		replay.GapFromID = afterID + 1
		replay.GapThroughID = firstID - 1
	}

	offset := 0
	if afterID >= l.lastID {
		offset = l.size
	} else if afterID >= firstID {
		offset = int(afterID - firstID + 1)
	}
	remaining := l.size - offset
	count := remaining
	if count > limit {
		count = limit
	}
	replay.Events = make([]Event, 0, count)
	for index := 0; index < count; index++ {
		position := (l.start + offset + index) % len(l.buffer)
		replay.Events = append(replay.Events, cloneEvent(l.buffer[position]))
	}
	if count > 0 {
		replay.NextAfterID = replay.Events[count-1].ID
	}
	replay.HasMore = remaining > count
	return replay, nil
}

func (l *Log) Snapshot() []Event {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]Event, 0, l.size)
	for index := 0; index < l.size; index++ {
		position := (l.start + index) % len(l.buffer)
		result = append(result, cloneEvent(l.buffer[position]))
	}
	return result
}

func (l *Log) Stats() Stats {
	if l == nil {
		return Stats{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	stats := Stats{
		Capacity:        len(l.buffer),
		MaxPayloadBytes: l.maxPayloadBytes,
		Retained:        l.size,
		TotalAppended:   l.totalAppended,
		Evicted:         l.evicted,
		LastID:          l.lastID,
	}
	if l.size > 0 {
		stats.FirstID = l.buffer[l.start].ID
	}
	return stats
}
