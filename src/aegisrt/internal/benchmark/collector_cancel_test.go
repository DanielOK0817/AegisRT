package benchmark

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestCollectSchedulerStopsSimulationWhenContextIsCanceled(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	clock := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	clockCalls := 0
	deps := testCollectorDeps("run-eeeeeeeeeeeeeeeeeeeeeeee")
	deps.now = func() time.Time {
		clockCalls++
		clock = clock.Add(time.Millisecond)
		if clockCalls == 2 {
			cancel()
		}
		return clock
	}
	_, err := collectScheduler(ctx, CollectOptions{
		OutputDir: parent, Plan: SchedulerPlan{Seeds: []uint32{1, 2}, Repetitions: 2},
	}, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collection error = %v, want context canceled", err)
	}
	if clockCalls > 3 {
		t.Fatalf("simulation ignored cancellation; clock calls = %d", clockCalls)
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled simulation created output: %#v", entries)
	}
}
