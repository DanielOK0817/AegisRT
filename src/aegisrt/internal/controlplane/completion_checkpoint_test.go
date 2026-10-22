package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/worker"
)

func TestCompletionCheckpointFailureDoesNotReportSuccessfulAttempt(t *testing.T) {
	store := &rejectCompletionStore{}
	r, _, err := runtimecore.NewPersistent(scheduler.FIFO{}, domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 1024}, "test", store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(r, worker.NewManager(executor.MinimalHostEnvironment(), nil), "worker-1", []domain.AgentSpec{processAgent("success")})
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("completion", nil)
	if err := c.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := c.Tick(context.Background(), time.Now())
	if !errors.Is(err, runtimecore.ErrCheckpointUnavailable) {
		t.Fatalf("expected completion save failure: %v", err)
	}
	if result.Execution.Attempt.Status != domain.AttemptStatusLost || result.Execution.Attempt.Failure == "" {
		t.Fatalf("uncommitted success exposed: %+v", result.Execution.Attempt)
	}
	if len(result.NewlyReady) != 0 {
		t.Fatal("uncommitted dependencies exposed")
	}
	assertCoordinatorStatus(t, r, task.ID, domain.TaskStatusRunning)
	if r.Stats().Allocated.IsZero() {
		t.Fatal("lost recovery reservation")
	}
}

type rejectCompletionStore struct{ data []byte }

func (s *rejectCompletionStore) Load() ([]byte, error) {
	if s.data == nil {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), s.data...), nil
}

func (s *rejectCompletionStore) Save(data []byte) error {
	var checkpoint runtimecore.Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return err
	}
	for _, task := range checkpoint.Graph.Tasks {
		if task.Status == domain.TaskStatusSucceeded {
			return errors.New("injected completion checkpoint failure")
		}
	}
	s.data = append([]byte(nil), data...)
	return nil
}
