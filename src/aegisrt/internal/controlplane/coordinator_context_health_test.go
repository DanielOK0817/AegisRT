package controlplane

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"aegisrt/internal/contextdispatch"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/worker"
)

func TestCoordinatorDoesNotScheduleWhenContextRepositoryIsUnavailable(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	manager := worker.NewManager(executor.MinimalHostEnvironment(), nil)
	agent := processAgent("success")
	resolver := unavailableContextResolver{}
	coordinator, err := NewWithOptions(runtime, manager, "worker-1", []domain.AgentSpec{agent}, Options{ContextResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("context-unavailable", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Tick(context.Background(), time.Now()); !errors.Is(err, runtimecore.ErrRuntimeUnavailable) {
		t.Fatalf("Tick error = %v, want ErrRuntimeUnavailable", err)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusReady)
	if !runtime.Stats().Allocated.IsZero() {
		t.Fatalf("unhealthy Context resolver reserved resources: %+v", runtime.Stats().Allocated)
	}
}

func TestCoordinatorRequeuesAdmissionWhenContextFailsAfterHealthCheck(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	manager := worker.NewManager(executor.MinimalHostEnvironment(), nil)
	agent := processAgent("success")
	resolver := &poisoningContextResolver{}
	resolver.healthy.Store(true)
	coordinator, err := NewWithOptions(runtime, manager, "worker-1", []domain.AgentSpec{agent}, Options{ContextResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("context-poisoned", nil)
	task.MaxAttempts = 1
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Tick(context.Background(), time.Now()); !errors.Is(err, runtimecore.ErrRuntimeUnavailable) {
		t.Fatalf("Tick error = %v, want ErrRuntimeUnavailable", err)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusReady)
	if runtime.AttemptNumber(task.ID) != 0 || !runtime.Stats().Allocated.IsZero() {
		t.Fatalf("Context infrastructure failure consumed admission: attempt=%d stats=%+v",
			runtime.AttemptNumber(task.ID), runtime.Stats())
	}
}

type unavailableContextResolver struct{}

func (unavailableContextResolver) Healthy() bool { return false }

func (unavailableContextResolver) Resolve(
	context.Context, domain.TaskSpec, domain.AgentSpec, int,
) (contextdispatch.Resolution, error) {
	panic("Resolve must not run while Context is unavailable")
}

type poisoningContextResolver struct {
	healthy atomic.Bool
}

func (r *poisoningContextResolver) Healthy() bool { return r.healthy.Load() }

func (r *poisoningContextResolver) Resolve(
	context.Context, domain.TaskSpec, domain.AgentSpec, int,
) (contextdispatch.Resolution, error) {
	r.healthy.Store(false)
	return contextdispatch.Resolution{}, errors.New("Context checkpoint failed")
}
