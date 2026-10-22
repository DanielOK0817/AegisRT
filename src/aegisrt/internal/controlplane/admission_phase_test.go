package controlplane

import (
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/worker"
	"context"
	"errors"
	"testing"
	"time"
)

func TestPreparingContextRemainsAdmittedAndCanBeCanceled(t *testing.T) {
	r := newCoordinatorRuntime(t)
	resolver := &blockingResolver{started: make(chan struct{})}
	c, err := NewWithOptions(r, worker.NewManager(executor.MinimalHostEnvironment(), nil), "worker-1", []domain.AgentSpec{processAgent("success")}, Options{ContextResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("preparing", nil)
	if err := c.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan tickOutcome, 1)
	go func() { result, err := c.Tick(ctx, time.Now()); done <- tickOutcome{result: result, err: err} }()
	select {
	case <-resolver.started:
	case <-time.After(3 * time.Second):
		t.Fatal("resolver did not start")
	}
	assertCoordinatorStatus(t, r, task.ID, domain.TaskStatusAdmitted)
	if _, err := c.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	outcome := <-done
	if !errors.Is(outcome.err, context.Canceled) || outcome.result.Execution.Process.ProcessStarted {
		t.Fatalf("preparation cancel=%+v", outcome)
	}
	assertCoordinatorStatus(t, r, task.ID, domain.TaskStatusCanceled)
	assertCoordinatorIdle(t, c, r, task.ID)
}

func TestStartFailureDoesNotInventAttemptStartTime(t *testing.T) {
	agent := processAgent("success")
	agent.Entrypoint = "aegisrt-intentionally-missing-executable"
	c, r := newTestCoordinator(t, agent)
	task := coordinatorTask("cannot-start", nil)
	if err := c.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := c.Tick(context.Background(), time.Now())
	if !errors.Is(err, executor.ErrStart) {
		t.Fatalf("Tick error=%v", err)
	}
	if !result.Execution.Attempt.StartedAt.IsZero() || result.Execution.Attempt.Status != domain.AttemptStatusFailed {
		t.Fatalf("attempt=%+v", result.Execution.Attempt)
	}
	assertCoordinatorStatus(t, r, task.ID, domain.TaskStatusFailed)
	assertCoordinatorIdle(t, c, r, task.ID)
}
