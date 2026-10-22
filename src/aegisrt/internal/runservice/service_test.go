package runservice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/worker"
)

func TestServiceDrainsWorkWithBoundedConcurrency(t *testing.T) {
	coordinator := &fakeCoordinator{remaining: 7, delay: 25 * time.Millisecond}
	service, err := New(coordinator, nil, Config{PollInterval: time.Second, MaxConcurrent: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	waitFor(t, func() bool { return service.Stats().TicksSucceeded == 7 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stats := service.Stats()
	if err := stats.Validate(); err != nil {
		t.Fatal(err)
	}
	if stats.Active != 0 || stats.PeakActive < 2 || stats.PeakActive > 3 {
		t.Fatalf("unexpected service concurrency: %+v", stats)
	}
	if peak := coordinator.Peak(); peak < 2 || peak > 3 {
		t.Fatalf("coordinator peak concurrency = %d", peak)
	}
}

func TestServiceObservesTaskErrors(t *testing.T) {
	wantErr := errors.New("worker failed")
	coordinator := &fakeCoordinator{remaining: 1, resultErr: wantErr}
	observer := &recordingObserver{outcomes: make(chan Outcome, 1)}
	service, err := New(coordinator, observer, Config{PollInterval: time.Second, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case outcome := <-observer.outcomes:
		if !errors.Is(outcome.Err, wantErr) {
			t.Fatalf("observed error = %v, want %v", outcome.Err, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer did not receive failed outcome")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if service.Stats().TicksFailed != 1 {
		t.Fatalf("stats = %+v", service.Stats())
	}
}

func TestServiceStopsOnRuntimeDurabilityFailure(t *testing.T) {
	coordinator := &fakeCoordinator{remaining: 100, resultErr: runtimecore.ErrRuntimeUnavailable}
	service, err := New(coordinator, nil, Config{PollInterval: time.Hour, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Run(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, runtimecore.ErrRuntimeUnavailable) {
			t.Fatalf("Run error = %v, want ErrRuntimeUnavailable", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service did not fail stop after durability failure")
	}
	if got := service.Stats().TicksStarted; got != 1 {
		t.Fatalf("ticks started = %d, want one without a hot retry loop", got)
	}
}

func TestServiceCountsObserverErrors(t *testing.T) {
	coordinator := &fakeCoordinator{remaining: 1}
	observer := errorObserver{err: errors.New("event log unavailable")}
	service, err := New(coordinator, observer, Config{PollInterval: time.Second, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	waitFor(t, func() bool { return service.Stats().ObserverErrors == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := service.Stats().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceSeparatesCancellationAndTimeoutStats(t *testing.T) {
	tests := []struct {
		name   string
		result controlplane.TickResult
		err    error
		check  func(Stats) bool
	}{
		{
			name: "canceled",
			result: controlplane.TickResult{Execution: workerResult(
				domain.AttemptStatusCanceled, true, false,
			)},
			err:   executor.ErrCanceled,
			check: func(stats Stats) bool { return stats.TicksCanceled == 1 && stats.TicksFailed == 0 },
		},
		{
			name: "interrupted",
			result: controlplane.TickResult{Execution: workerResult(
				domain.AttemptStatusLost, true, false,
			)},
			err: errors.Join(executor.ErrCanceled, controlplane.ErrAttemptInterrupted),
			check: func(stats Stats) bool {
				return stats.TicksInterrupted == 1 && stats.TicksCanceled == 0 && stats.TicksFailed == 0
			},
		},
		{
			name: "timed out",
			result: controlplane.TickResult{Execution: workerResult(
				domain.AttemptStatusFailed, false, true,
			)},
			err:   executor.ErrTimeout,
			check: func(stats Stats) bool { return stats.TicksTimedOut == 1 && stats.TicksFailed == 0 },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &fakeCoordinator{remaining: 1, result: test.result, resultErr: test.err}
			service, err := New(coordinator, nil, Config{PollInterval: time.Second, MaxConcurrent: 1})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- service.Run(ctx) }()
			waitFor(t, func() bool { return service.Stats().Active == 0 && service.Stats().TicksStarted > 0 })
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			stats := service.Stats()
			if !test.check(stats) || stats.Validate() != nil {
				t.Fatalf("stats = %+v", stats)
			}
		})
	}
}

func TestServiceRejectsConcurrentRun(t *testing.T) {
	service, err := New(&fakeCoordinator{}, nil, Config{PollInterval: time.Second, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	waitFor(t, func() bool {
		service.runMu.Lock()
		defer service.runMu.Unlock()
		return service.running
	})
	if err := service.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("error = %v, want ErrAlreadyRunning", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceValidation(t *testing.T) {
	valid := Config{PollInterval: time.Millisecond, MaxConcurrent: 1}
	if _, err := New(nil, nil, valid); err == nil {
		t.Fatal("New accepted a nil coordinator")
	}
	if _, err := New(&fakeCoordinator{}, nil, Config{MaxConcurrent: 1}); err == nil {
		t.Fatal("New accepted a zero poll interval")
	}
	if _, err := New(&fakeCoordinator{}, nil, Config{PollInterval: time.Millisecond}); err == nil {
		t.Fatal("New accepted zero concurrency")
	}
	service, err := New(&fakeCoordinator{}, nil, valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(nil); err == nil {
		t.Fatal("Run accepted nil context")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if service.Stats().TicksStarted != 0 {
		t.Fatalf("canceled service started ticks: %+v", service.Stats())
	}
}

func TestStatsValidateRejectsBrokenAccounting(t *testing.T) {
	if err := (Stats{TicksStarted: 1}).Validate(); err == nil {
		t.Fatal("Validate accepted missing completion")
	}
	if err := (Stats{Active: -1}).Validate(); err == nil {
		t.Fatal("Validate accepted negative active count")
	}
}

type fakeCoordinator struct {
	mu        sync.Mutex
	remaining int
	active    int
	peak      int
	delay     time.Duration
	resultErr error
	result    controlplane.TickResult
}

func (f *fakeCoordinator) Tick(ctx context.Context, _ time.Time) (controlplane.TickResult, error) {
	f.mu.Lock()
	if f.remaining == 0 {
		f.mu.Unlock()
		return controlplane.TickResult{}, scheduler.ErrNoCandidate
	}
	f.remaining--
	f.active++
	if f.active > f.peak {
		f.peak = f.active
	}
	delay := f.delay
	resultErr := f.resultErr
	f.mu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			resultErr = ctx.Err()
		case <-timer.C:
		}
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return f.result, resultErr
}

func (f *fakeCoordinator) Peak() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

type recordingObserver struct {
	outcomes chan Outcome
}

func (r *recordingObserver) Observe(outcome Outcome) error {
	r.outcomes <- outcome
	return nil
}

type errorObserver struct {
	err error
}

func (o errorObserver) Observe(Outcome) error {
	return o.err
}

func workerResult(status domain.AttemptStatus, canceled, timedOut bool) worker.Result {
	return worker.Result{
		Attempt: domain.AgentAttempt{Status: status},
		Process: executor.Result{Canceled: canceled, TimedOut: timedOut},
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
