package runtimecore

import (
	"errors"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

func TestCancelQueuedTasks(t *testing.T) {
	runtime := newTestRuntime(t)
	if err := runtime.ApplyPatch([]domain.TaskSpec{
		task("root", "workflow", nil, 1, 100),
		task("waiting", "workflow", []string{"root"}, 1, 100),
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"root", "waiting"} {
		snapshot, err := runtime.Cancel(taskID)
		if err != nil {
			t.Fatalf("Cancel(%q) error = %v", taskID, err)
		}
		if snapshot.Status != domain.TaskStatusCanceled {
			t.Fatalf("Cancel(%q) status = %s, want CANCELED", taskID, snapshot.Status)
		}
	}
	if runtime.Stats().ReadyCount != 0 {
		t.Fatal("canceled ready task remained schedulable")
	}
}

func TestCancelActiveTaskReleasesReservationOnce(t *testing.T) {
	runtime := newTestRuntime(t)
	if err := runtime.ApplyPatch([]domain.TaskSpec{task("active", "workflow", nil, 1, 300)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(time.Now()); err != nil {
		t.Fatal(err)
	}

	const callers = 32
	var group sync.WaitGroup
	errorsCh := make(chan error, callers)
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := runtime.Cancel("active")
			errorsCh <- err
		}()
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Errorf("concurrent Cancel() error = %v", err)
		}
	}
	assertTaskStatus(t, runtime, "active", domain.TaskStatusCanceled)
	if !runtime.Stats().Allocated.IsZero() {
		t.Fatalf("cancellation leaked reservation: %+v", runtime.Stats().Allocated)
	}
}

func TestCancelTerminalTaskReturnsConflict(t *testing.T) {
	for _, terminal := range []domain.TaskStatus{domain.TaskStatusSucceeded, domain.TaskStatusFailed} {
		t.Run(string(terminal), func(t *testing.T) {
			runtime := newTestRuntime(t)
			if err := runtime.ApplyPatch([]domain.TaskSpec{task("done", "workflow", nil, 1, 100)}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.Schedule(time.Now()); err != nil {
				t.Fatal(err)
			}
			if terminal == domain.TaskStatusSucceeded {
				_, _ = runtime.Complete("done", domain.ResourceVector{}, time.Now())
			} else {
				_, _ = runtime.Fail("done", domain.ResourceVector{})
			}
			if _, err := runtime.Cancel("done"); !errors.Is(err, ErrTaskNotCancelable) {
				t.Fatalf("Cancel() error = %v, want ErrTaskNotCancelable", err)
			}
		})
	}
}

func TestCancelUnknownTaskPreservesTaskGraphError(t *testing.T) {
	runtime := newTestRuntime(t)
	if _, err := runtime.Cancel("missing"); !errors.Is(err, taskgraph.ErrTaskNotFound) {
		t.Fatalf("Cancel() error = %v, want taskgraph.ErrTaskNotFound", err)
	}
}
