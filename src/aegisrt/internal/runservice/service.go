package runservice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
)

var ErrAlreadyRunning = errors.New("service is already running")

type Coordinator interface {
	Tick(context.Context, time.Time) (controlplane.TickResult, error)
}

type Observer interface {
	Observe(Outcome) error
}

type Outcome struct {
	FinishedAt time.Time
	Result     controlplane.TickResult
	Err        error
}

type Config struct {
	PollInterval  time.Duration
	MaxConcurrent int
}

type Stats struct {
	TicksStarted     uint64    `json:"ticks_started"`
	TicksSucceeded   uint64    `json:"ticks_succeeded"`
	TicksFailed      uint64    `json:"ticks_failed"`
	TicksCanceled    uint64    `json:"ticks_canceled"`
	TicksTimedOut    uint64    `json:"ticks_timed_out"`
	TicksInterrupted uint64    `json:"ticks_interrupted"`
	IdleTicks        uint64    `json:"idle_ticks"`
	ObserverErrors   uint64    `json:"observer_errors"`
	Active           int       `json:"active"`
	PeakActive       int       `json:"peak_active"`
	LastFinishedAt   time.Time `json:"last_finished_at,omitempty"`
}

// Service owns repeated Coordinator ticks. It provides bounded concurrency,
// wakeups after graph changes, and graceful cancellation without exposing a
// remote command-execution API.
type Service struct {
	coordinator Coordinator
	observer    Observer
	config      Config
	wake        chan struct{}
	slots       chan struct{}
	workers     sync.WaitGroup
	runMu       sync.Mutex
	running     bool
	mu          sync.Mutex
	stats       Stats
}

func New(coordinator Coordinator, observer Observer, config Config) (*Service, error) {
	if coordinator == nil {
		return nil, errors.New("coordinator is required")
	}
	if config.PollInterval <= 0 {
		return nil, errors.New("poll interval must be positive")
	}
	if config.MaxConcurrent <= 0 {
		return nil, errors.New("max concurrent must be positive")
	}
	return &Service{
		coordinator: coordinator,
		observer:    observer,
		config:      config,
		wake:        make(chan struct{}, 1),
		slots:       make(chan struct{}, config.MaxConcurrent),
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	s.runMu.Lock()
	if s.running {
		s.runMu.Unlock()
		return ErrAlreadyRunning
	}
	s.running = true
	s.runMu.Unlock()
	defer func() {
		s.runMu.Lock()
		s.running = false
		s.runMu.Unlock()
	}()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	fatalErrors := make(chan error, 1)
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	if runCtx.Err() == nil {
		s.dispatch(runCtx, fatalErrors)
	}
	for {
		select {
		case <-runCtx.Done():
			s.workers.Wait()
			select {
			case err := <-fatalErrors:
				return err
			default:
				return nil
			}
		case <-ticker.C:
			s.dispatch(runCtx, fatalErrors)
		case <-s.wake:
			s.dispatch(runCtx, fatalErrors)
		case err := <-fatalErrors:
			cancel()
			s.workers.Wait()
			return err
		}
	}
}

// Notify asks the service to poll immediately. Repeated notifications are
// coalesced so callers never block.
func (s *Service) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

func (s *Service) dispatch(ctx context.Context, fatalErrors chan<- error) {
	for ctx.Err() == nil {
		select {
		case s.slots <- struct{}{}:
			s.markStarted()
			s.workers.Add(1)
			go s.runOne(ctx, fatalErrors)
		default:
			return
		}
	}
}

func (s *Service) runOne(ctx context.Context, fatalErrors chan<- error) {
	defer s.workers.Done()
	result, err := s.coordinator.Tick(ctx, time.Now().UTC())
	finishedAt := time.Now().UTC()
	s.markFinished(result, err, finishedAt)
	<-s.slots
	if errors.Is(err, scheduler.ErrNoCandidate) {
		return
	}
	isFatal := errors.Is(err, runtimecore.ErrRuntimeUnavailable) ||
		errors.Is(err, runtimecore.ErrCheckpointUnavailable)
	if !isFatal {
		s.Notify()
	}
	if s.observer != nil {
		if observeErr := s.observer.Observe(Outcome{FinishedAt: finishedAt, Result: result, Err: err}); observeErr != nil {
			s.mu.Lock()
			s.stats.ObserverErrors++
			s.mu.Unlock()
		}
	}
	if isFatal {
		select {
		case fatalErrors <- err:
		default:
		}
	}
}

func (s *Service) markStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.TicksStarted++
	s.stats.Active++
	if s.stats.Active > s.stats.PeakActive {
		s.stats.PeakActive = s.stats.Active
	}
}

func (s *Service) markFinished(result controlplane.TickResult, err error, finishedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.Active--
	s.stats.LastFinishedAt = finishedAt
	switch {
	case err == nil:
		s.stats.TicksSucceeded++
	case errors.Is(err, scheduler.ErrNoCandidate):
		s.stats.IdleTicks++
	case tickInterrupted(result, err):
		s.stats.TicksInterrupted++
	case tickTimedOut(result, err):
		s.stats.TicksTimedOut++
	case tickCanceled(result, err):
		s.stats.TicksCanceled++
	default:
		s.stats.TicksFailed++
	}
}

func tickInterrupted(result controlplane.TickResult, err error) bool {
	return result.Execution.Attempt.Status == domain.AttemptStatusLost ||
		errors.Is(err, controlplane.ErrAttemptInterrupted)
}

func tickTimedOut(result controlplane.TickResult, err error) bool {
	return result.Execution.Process.TimedOut ||
		errors.Is(err, executor.ErrTimeout) || errors.Is(err, context.DeadlineExceeded)
}

func tickCanceled(result controlplane.TickResult, err error) bool {
	return result.Execution.Process.Canceled ||
		result.Execution.Attempt.Status == domain.AttemptStatusCanceled ||
		errors.Is(err, executor.ErrCanceled) || errors.Is(err, context.Canceled)
}

func (s Stats) Validate() error {
	if s.Active < 0 || s.PeakActive < s.Active {
		return fmt.Errorf("invalid active tick counts")
	}
	finished := s.TicksSucceeded + s.TicksFailed + s.TicksCanceled + s.TicksTimedOut +
		s.TicksInterrupted + s.IdleTicks
	if finished+uint64(s.Active) != s.TicksStarted {
		return fmt.Errorf("tick accounting mismatch")
	}
	return nil
}
