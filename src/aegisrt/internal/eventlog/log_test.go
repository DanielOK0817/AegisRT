package eventlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestStreamIdentityIsStablePerLogAndUniqueByDefault(t *testing.T) {
	startedAt := time.Date(2026, 7, 20, 10, 0, 0, 0, time.FixedZone("test", 8*60*60))
	log, err := NewWithOptions(Options{
		Capacity: 2, MaxPayloadBytes: DefaultMaxPayloadBytes,
		StartedAt: startedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := log.Stream(); got.ID == "" || !got.StartedAt.Equal(startedAt) || got.StartedAt.Location() != time.UTC {
		t.Fatalf("Stream() = %+v", got)
	}
	first, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stream().ID == "" || second.Stream().ID == "" || first.Stream().ID == second.Stream().ID {
		t.Fatalf("generated stream IDs = %q and %q", first.Stream().ID, second.Stream().ID)
	}
}

func appendEvents(t *testing.T, log *Log, count int) []Event {
	t.Helper()
	result := make([]Event, 0, count)
	for index := 1; index <= count; index++ {
		event := validEvent()
		event.Payload = json.RawMessage(fmt.Sprintf(`{"sequence":%d}`, index))
		appended, err := log.Append(event)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, appended)
	}
	return result
}

func eventIDs(events []Event) []uint64 {
	result := make([]uint64, len(events))
	for index, event := range events {
		result[index] = event.ID
	}
	return result
}

func TestCapacityEvictionAndGapReporting(t *testing.T) {
	log, err := New(3)
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, log, 5)
	if got, want := eventIDs(log.Snapshot()), []uint64{3, 4, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot IDs = %v, want %v", got, want)
	}
	wantStats := Stats{Capacity: 3, MaxPayloadBytes: DefaultMaxPayloadBytes, Retained: 3, TotalAppended: 5, Evicted: 2, FirstID: 3, LastID: 5}
	if got := log.Stats(); got != wantStats {
		t.Fatalf("Stats() = %#v, want %#v", got, wantStats)
	}

	tests := []struct {
		name       string
		after      uint64
		limit      int
		wantIDs    []uint64
		gap        bool
		gapFrom    uint64
		gapThrough uint64
		next       uint64
		hasMore    bool
	}{
		{name: "full lost prefix", after: 0, limit: 10, wantIDs: []uint64{3, 4, 5}, gap: true, gapFrom: 1, gapThrough: 2, next: 5},
		{name: "partial lost prefix", after: 1, limit: 10, wantIDs: []uint64{3, 4, 5}, gap: true, gapFrom: 2, gapThrough: 2, next: 5},
		{name: "cursor immediately before retained", after: 2, limit: 10, wantIDs: []uint64{3, 4, 5}, next: 5},
		{name: "retained cursor", after: 4, limit: 10, wantIDs: []uint64{5}, next: 5},
		{name: "at tail", after: 5, limit: 10, wantIDs: []uint64{}, next: 5},
		{name: "future cursor", after: 99, limit: 10, wantIDs: []uint64{}, next: 99},
		{name: "gap with page", after: 0, limit: 2, wantIDs: []uint64{3, 4}, gap: true, gapFrom: 1, gapThrough: 2, next: 4, hasMore: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			replay, err := log.Since(test.after, test.limit)
			if err != nil {
				t.Fatal(err)
			}
			if got := eventIDs(replay.Events); !reflect.DeepEqual(got, test.wantIDs) {
				t.Fatalf("event IDs = %v, want %v", got, test.wantIDs)
			}
			if replay.Gap != test.gap || replay.GapFromID != test.gapFrom || replay.GapThroughID != test.gapThrough ||
				replay.NextAfterID != test.next || replay.HasMore != test.hasMore ||
				replay.FirstAvailableID != 3 || replay.LastAvailableID != 5 {
				t.Fatalf("Replay metadata = %#v", replay)
			}
		})
	}
	if _, err := log.Since(0, 0); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("zero limit error = %v", err)
	}
}

func TestSincePagination(t *testing.T) {
	log, _ := New(10)
	appendEvents(t, log, 7)
	cursor := uint64(0)
	wantPages := [][]uint64{{1, 2, 3}, {4, 5, 6}, {7}}
	for index, want := range wantPages {
		replay, err := log.Since(cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		if got := eventIDs(replay.Events); !reflect.DeepEqual(got, want) {
			t.Fatalf("page %d IDs = %v, want %v", index, got, want)
		}
		if replay.Gap || replay.HasMore != (index < len(wantPages)-1) {
			t.Fatalf("page %d metadata = %#v", index, replay)
		}
		cursor = replay.NextAfterID
	}
	if cursor != 7 {
		t.Fatalf("final cursor = %d, want 7", cursor)
	}
}

func TestEmptyLogReplayAndStats(t *testing.T) {
	log, _ := New(2)
	replay, err := log.Since(8, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Events) != 0 || replay.Gap || replay.NextAfterID != 8 || replay.FirstAvailableID != 0 || replay.LastAvailableID != 0 {
		t.Fatalf("empty Replay = %#v", replay)
	}
	if got := log.Stats(); got.Retained != 0 || got.FirstID != 0 || got.LastID != 0 {
		t.Fatalf("empty Stats = %#v", got)
	}
}

func TestConcurrentAppendHasUniqueMonotonicOrder(t *testing.T) {
	const (
		writers   = 40
		perWriter = 25
		total     = writers * perWriter
	)
	log, _ := New(total)
	ids := make(chan uint64, total)
	errs := make(chan error, total+8)
	start := make(chan struct{})
	var writeWait sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		writeWait.Add(1)
		go func(writer int) {
			defer writeWait.Done()
			<-start
			for sequence := 0; sequence < perWriter; sequence++ {
				event := validEvent()
				event.Payload = json.RawMessage(fmt.Sprintf(`{"writer":%d,"sequence":%d}`, writer, sequence))
				appended, err := log.Append(event)
				if err != nil {
					errs <- err
					return
				}
				ids <- appended.ID
			}
		}(writer)
	}

	stopReaders := make(chan struct{})
	var readWait sync.WaitGroup
	for reader := 0; reader < 8; reader++ {
		readWait.Add(1)
		go func() {
			defer readWait.Done()
			<-start
			for {
				select {
				case <-stopReaders:
					return
				default:
					snapshot := log.Snapshot()
					for index := 1; index < len(snapshot); index++ {
						if snapshot[index-1].ID >= snapshot[index].ID {
							errs <- fmt.Errorf("snapshot order %d then %d", snapshot[index-1].ID, snapshot[index].ID)
							return
						}
					}
					_, _ = log.Since(0, total)
					runtime.Gosched()
				}
			}
		}()
	}
	close(start)
	writeWait.Wait()
	close(stopReaders)
	readWait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	allIDs := make([]uint64, 0, total)
	for id := range ids {
		allIDs = append(allIDs, id)
	}
	sort.Slice(allIDs, func(i, j int) bool { return allIDs[i] < allIDs[j] })
	if len(allIDs) != total {
		t.Fatalf("received %d IDs, want %d", len(allIDs), total)
	}
	for index, id := range allIDs {
		if id != uint64(index+1) {
			t.Fatalf("sorted ID[%d] = %d, want %d", index, id, index+1)
		}
	}
	snapshot := log.Snapshot()
	for index, event := range snapshot {
		if event.ID != uint64(index+1) {
			t.Fatalf("snapshot ID[%d] = %d", index, event.ID)
		}
	}
	stats := log.Stats()
	if stats.TotalAppended != total || stats.Retained != total || stats.Evicted != 0 || stats.FirstID != 1 || stats.LastID != total {
		t.Fatalf("concurrent Stats = %#v", stats)
	}
}
