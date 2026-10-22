package controlplane

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/worker"
)

func TestCoordinatorPreservesDurableRunningStateWhenIsolationCleanupFails(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	cleanupErr := errors.New("injected cleanup failure")
	manager := worker.NewManager(executor.MinimalHostEnvironment(), cleanupFailureIsolation{err: cleanupErr})
	agent := processAgent("success")
	coordinator, err := New(runtime, manager, "worker-1", []domain.AgentSpec{agent})
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("cleanup-uncertain", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}

	result, err := coordinator.Tick(context.Background(), time.Now())
	if !errors.Is(err, executor.ErrCleanup) || !errors.Is(err, cleanupErr) ||
		!errors.Is(err, runtimecore.ErrRuntimeUnavailable) {
		t.Fatalf("Tick error = %v, want cleanup and Runtime unavailable errors", err)
	}
	if result.Execution.Attempt.Status != domain.AttemptStatusLost {
		t.Fatalf("attempt status = %s, want LOST", result.Execution.Attempt.Status)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusRunning)
	if runtime.AttemptNumber(task.ID) != 1 || runtime.Stats().Allocated.IsZero() {
		t.Fatalf("cleanup uncertainty lost durable recovery state: attempt=%d stats=%+v",
			runtime.AttemptNumber(task.ID), runtime.Stats())
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.active) != 0 || len(coordinator.running) != 0 {
		t.Fatalf("process-local execution bookkeeping was retained: active=%d running=%d",
			len(coordinator.active), len(coordinator.running))
	}
}

type cleanupFailureIsolation struct{ err error }

func (isolation cleanupFailureIsolation) PrepareHook(domain.ResourceVector) executor.PrepareHook {
	return func(context.Context, executor.Request, *exec.Cmd) (executor.Cleanup, error) {
		return func() error { return isolation.err }, nil
	}
}
