package controlplane

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/contextdispatch"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/worker"
)

func TestCoordinatorCancelsQueuedTask(t *testing.T) {
	coordinator, runtime := newTestCoordinator(t, processAgent("success"))
	task := coordinatorTask("queued", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := coordinator.Cancel(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != domain.TaskStatusCanceled {
		t.Fatalf("status = %s, want CANCELED", snapshot.Status)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusCanceled)
}

func TestCoordinatorCancelsActiveTaskIdempotently(t *testing.T) {
	coordinator, runtime := newTestCoordinator(t, processAgent("sleep"))
	task := coordinatorTask("active", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	done := make(chan tickOutcome, 1)
	go func() {
		result, err := coordinator.Tick(context.Background(), time.Now())
		done <- tickOutcome{result: result, err: err}
	}()
	waitForTaskActive(t, coordinator, task.ID)
	for range 2 {
		snapshot, err := coordinator.Cancel(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Status != domain.TaskStatusRunning {
			t.Fatalf("in-flight cancellation status = %s, want RUNNING", snapshot.Status)
		}
	}
	outcome := <-done
	if !errors.Is(outcome.err, executor.ErrCanceled) {
		t.Fatalf("Tick() error = %v, want executor.ErrCanceled", outcome.err)
	}
	if outcome.result.Execution.Attempt.Status != domain.AttemptStatusCanceled {
		t.Fatalf("attempt status = %s, want CANCELED", outcome.result.Execution.Attempt.Status)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusCanceled)
	assertCoordinatorIdle(t, coordinator, runtime, task.ID)

	snapshot, err := coordinator.Cancel(task.ID)
	if err != nil || snapshot.Status != domain.TaskStatusCanceled {
		t.Fatalf("idempotent terminal Cancel() = (%+v, %v)", snapshot, err)
	}
}

func TestCoordinatorRootContextCancellationRequeuesTask(t *testing.T) {
	coordinator, runtime := newTestCoordinator(t, processAgent("sleep"))
	task := coordinatorTask("root-canceled", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan tickOutcome, 1)
	go func() {
		result, err := coordinator.Tick(ctx, time.Now())
		done <- tickOutcome{result: result, err: err}
	}()
	waitForTaskActive(t, coordinator, task.ID)
	cancel()
	outcome := <-done
	if !errors.Is(outcome.err, executor.ErrCanceled) {
		t.Fatalf("Tick() error = %v, want executor.ErrCanceled", outcome.err)
	}
	if !errors.Is(outcome.err, ErrAttemptInterrupted) {
		t.Fatalf("Tick() error = %v, want ErrAttemptInterrupted", outcome.err)
	}
	if outcome.result.Execution.Attempt.Status != domain.AttemptStatusLost {
		t.Fatalf("attempt status = %s, want LOST", outcome.result.Execution.Attempt.Status)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusReady)
	assertCoordinatorIdle(t, coordinator, runtime, task.ID)
}

func TestCoordinatorResolverShutdownRequeuesTask(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	agent := processAgent("success")
	resolver := &blockingResolver{started: make(chan struct{})}
	manager := worker.NewManager(executor.MinimalHostEnvironment(), nil)
	coordinator, err := NewWithOptions(runtime, manager, "worker-1", []domain.AgentSpec{agent}, Options{ContextResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("resolve-canceled", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, tickErr := coordinator.Tick(ctx, time.Now())
		done <- tickErr
	}()
	<-resolver.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Tick() error = %v, want context.Canceled", err)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusReady)
	assertCoordinatorIdle(t, coordinator, runtime, task.ID)
}

func TestCoordinatorResolverDeadlineFailsTask(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	agent := processAgent("success")
	agent.ExecutionTimeout = 30 * time.Millisecond
	resolver := &blockingResolver{started: make(chan struct{})}
	manager := worker.NewManager(executor.MinimalHostEnvironment(), nil)
	coordinator, err := NewWithOptions(runtime, manager, "worker-1", []domain.AgentSpec{agent}, Options{ContextResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("resolve-timeout", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Tick(context.Background(), time.Now())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Tick() error = %v, want context.DeadlineExceeded", err)
	}
	if result.Execution.Attempt.ID != "" {
		t.Fatalf("worker launched after resolver timeout: %+v", result.Execution)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusFailed)
	assertCoordinatorIdle(t, coordinator, runtime, task.ID)
}

func TestCoordinatorWorkerDeadlineFailsAttemptAndTask(t *testing.T) {
	agent := processAgent("sleep")
	agent.ExecutionTimeout = 30 * time.Millisecond
	coordinator, runtime := newTestCoordinator(t, agent)
	task := coordinatorTask("worker-timeout", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Tick(context.Background(), time.Now())
	if !errors.Is(err, executor.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Tick() error = %v, want timeout", err)
	}
	if result.Execution.Attempt.Status != domain.AttemptStatusFailed || !result.Execution.Process.TimedOut {
		t.Fatalf("execution = %+v, want timed-out failed attempt", result.Execution)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusFailed)
	assertCoordinatorIdle(t, coordinator, runtime, task.ID)
}

func TestCoordinatorCancelRacesAreSafe(t *testing.T) {
	coordinator, runtime := newTestCoordinator(t, processAgent("sleep"))
	task := coordinatorTask("raced", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := coordinator.Tick(context.Background(), time.Now())
		done <- err
	}()
	waitForTaskActive(t, coordinator, task.ID)

	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = coordinator.Cancel(task.ID)
		}()
	}
	group.Wait()
	if err := <-done; !errors.Is(err, executor.ErrCanceled) {
		t.Fatalf("Tick() error = %v, want executor.ErrCanceled", err)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusCanceled)
	assertCoordinatorIdle(t, coordinator, runtime, task.ID)
}

func TestCoordinatorDurableCancelIntentWinsFinalizationRace(t *testing.T) {
	coordinator, runtime := newTestCoordinator(t, processAgent("success"))
	task := coordinatorTask("success-race", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RequestCancel(task.ID); err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	coordinator.running[task.AgentID] = 1
	coordinator.active[task.ID] = func() {}
	coordinator.mu.Unlock()
	if _, canceled, err := coordinator.finalizeOutput(context.Background(), task, 1, nil); err != nil || !canceled {
		t.Fatal(err)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusCanceled)
	assertCoordinatorIdle(t, coordinator, runtime, task.ID)
}

func TestExecutionSuccessIgnoresLaterShutdownSignal(t *testing.T) {
	timedOut, canceled := executionFailureSignals(nil, context.Canceled)
	if timedOut || canceled {
		t.Fatalf("successful execution classified as failure: timeout=%v canceled=%v", timedOut, canceled)
	}
}

func TestCoordinatorDurableCancelIntentWinsDeadlineRace(t *testing.T) {
	coordinator, runtime := newTestCoordinator(t, processAgent("sleep"))
	task := coordinatorTask("deadline-race", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RequestCancel(task.ID); err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	coordinator.running[task.AgentID] = 1
	coordinator.active[task.ID] = func() {}
	coordinator.mu.Unlock()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	canceled, timedOut, _, err := coordinator.finalizeFailure(
		ctx, task.ID, task.AgentID, task.Resources, false, true,
	)
	if err != nil || !canceled || timedOut {
		t.Fatalf("finalizeFailure = canceled:%v timedOut:%v err:%v", canceled, timedOut, err)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusCanceled)
}

func TestCoordinatorCancelCompletedTaskReturnsConflict(t *testing.T) {
	coordinator, _ := newTestCoordinator(t, processAgent("success"))
	task := coordinatorTask("done", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Cancel(task.ID); !errors.Is(err, runtimecore.ErrTaskNotCancelable) {
		t.Fatalf("Cancel() error = %v, want ErrTaskNotCancelable", err)
	}
}

func TestCoordinatorExposesRuntimeSnapshots(t *testing.T) {
	coordinator, _ := newTestCoordinator(t, processAgent("success"))
	if err := coordinator.ApplyPatch([]domain.TaskSpec{coordinatorTask("visible", nil)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if tasks := coordinator.Tasks(); len(tasks) != 1 || tasks[0].Spec.ID != "visible" {
		t.Fatalf("Tasks() = %+v", tasks)
	}
	if stats := coordinator.Stats(); stats.TaskCount != 1 || stats.ReadyCount != 1 {
		t.Fatalf("Stats() = %+v", stats)
	}
}

type tickOutcome struct {
	result TickResult
	err    error
}

type blockingResolver struct {
	once    sync.Once
	started chan struct{}
}

func (r *blockingResolver) Resolve(ctx context.Context, _ domain.TaskSpec, _ domain.AgentSpec, _ int) (contextdispatch.Resolution, error) {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return contextdispatch.Resolution{}, ctx.Err()
}

func waitForTaskActive(t *testing.T, coordinator *Coordinator, taskID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		coordinator.mu.Lock()
		_, active := coordinator.active[taskID]
		coordinator.mu.Unlock()
		snapshot, exists := coordinator.Task(taskID)
		if active && exists && snapshot.Status == domain.TaskStatusRunning {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("task did not become active")
}

func assertCoordinatorIdle(t *testing.T, coordinator *Coordinator, runtime *runtimecore.Runtime, taskID string) {
	t.Helper()
	coordinator.mu.Lock()
	_, active := coordinator.active[taskID]
	running := coordinator.running["agent-1"]
	coordinator.mu.Unlock()
	if active || running != 0 || !runtime.Stats().Allocated.IsZero() {
		t.Fatalf("coordinator not idle: active=%v running=%d allocated=%+v", active, running, runtime.Stats().Allocated)
	}
}
