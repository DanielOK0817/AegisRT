package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/worker"
)

func TestOutputRecoveryWindowsNeverRerunCommittedAgent(t *testing.T) {
	for _, window := range []string{"publication", "completion-save", "completion-ack"} {
		t.Run(window, func(t *testing.T) {
			store := &completionWindowStore{fail: window}
			capacity := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 1024}
			r, _, err := runtimecore.NewPersistent(scheduler.FIFO{}, capacity, "window-test", store, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			publisher := &spyOutputPublisher{}
			if window == "publication" {
				publisher.resultErr = errors.New("publication unavailable")
			}
			c, err := NewWithOptions(r, worker.NewManager(executor.MinimalHostEnvironment(), nil), "worker-1", []domain.AgentSpec{processAgent("artifact")}, Options{OutputPublisher: publisher})
			if err != nil {
				t.Fatal(err)
			}
			task := coordinatorTask("recover-output", nil)
			if err := c.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
				t.Fatal(err)
			}
			result, err := c.Tick(context.Background(), time.Now())
			if err == nil || result.Execution.Attempt.Status == domain.AttemptStatusSucceeded {
				t.Fatalf("fault not surfaced: %+v %v", result, err)
			}
			if publisher.calls != 1 {
				t.Fatal("expected first publication attempt")
			}
			store.fail = ""
			// No cleanup hook: the prepared record proves that process wait and
			// cleanup finished before the durable completion decision.
			restored, report, err := runtimecore.NewPersistent(scheduler.FIFO{}, capacity, "window-test", store, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if len(report.RequeuedTaskIDs) != 0 {
				t.Fatal("committed Agent would run again")
			}
			publisher.resultErr = nil
			// A rerun would fail; recovery must use the saved output only.
			recovered, err := NewWithOptions(restored, worker.NewManager(executor.MinimalHostEnvironment(), nil), "worker-1", []domain.AgentSpec{processAgent("fail")}, Options{OutputPublisher: publisher})
			if err != nil {
				t.Fatal(err)
			}
			if err := recovered.RecoverOutputs(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertCoordinatorStatus(t, restored, task.ID, domain.TaskStatusSucceeded)
			wantCalls := 2
			if window == "completion-ack" {
				wantCalls = 1
			}
			if publisher.calls != wantCalls || len(publisher.artifacts) != 1 || publisher.artifacts[0].Content != "coordinator durable result" {
				t.Fatalf("wrong replay: %+v", publisher)
			}
			if restored.AttemptNumber(task.ID) != 1 || !restored.Stats().Allocated.IsZero() || len(restored.PendingCompletions()) != 0 {
				t.Fatal("recovery attempt/accounting mismatch")
			}
			if err := recovered.RecoverOutputs(context.Background()); err != nil || publisher.calls != wantCalls {
				t.Fatal("completed output replayed again")
			}
		})
	}
}

func TestPreparationCheckpointFailureNeverPublishes(t *testing.T) {
	store := &completionWindowStore{fail: "preparation"}
	r, _, err := runtimecore.NewPersistent(scheduler.FIFO{}, domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 1024}, "test", store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	publisher := &spyOutputPublisher{}
	c, err := NewWithOptions(r, worker.NewManager(executor.MinimalHostEnvironment(), nil), "worker-1", []domain.AgentSpec{processAgent("artifact")}, Options{OutputPublisher: publisher})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyPatch([]domain.TaskSpec{coordinatorTask("prepare-fails", nil)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tick(context.Background(), time.Now()); !errors.Is(err, runtimecore.ErrCheckpointUnavailable) {
		t.Fatalf("prepare failure=%v", err)
	}
	if publisher.calls != 0 || len(r.PendingCompletions()) != 0 {
		t.Fatal("output visible before durable preparation")
	}
}

func TestInvalidOutputFailsBeforeDurableCompletion(t *testing.T) {
	r := newCoordinatorRuntime(t)
	publisher := &invalidOutputPublisher{}
	c, err := NewWithOptions(r, worker.NewManager(executor.MinimalHostEnvironment(), nil), "worker-1", []domain.AgentSpec{processAgent("artifact")}, Options{OutputPublisher: publisher})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyPatch([]domain.TaskSpec{coordinatorTask("invalid", nil)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, tickErr := c.Tick(context.Background(), time.Now())
	if tickErr == nil {
		t.Fatal("accepted invalid output")
	}
	if result.Execution.Attempt.Status != domain.AttemptStatusFailed {
		t.Fatalf("invalid output reported %s", result.Execution.Attempt.Status)
	}
	assertCoordinatorStatus(t, r, "invalid", domain.TaskStatusFailed)
	if publisher.calls != 0 || len(r.PendingCompletions()) != 0 || !r.Stats().Allocated.IsZero() {
		t.Fatal("invalid output committed")
	}
}

type invalidOutputPublisher struct{ spyOutputPublisher }

func (*invalidOutputPublisher) ValidateAgentOutput(context.Context, string, int, []agentprotocol.Artifact) error {
	return errors.New("invalid output recipient")
}

type completionWindowStore struct {
	data []byte
	fail string
}

func (s *completionWindowStore) Load() ([]byte, error) {
	if s.data == nil {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), s.data...), nil
}
func (s *completionWindowStore) Save(data []byte) error {
	var checkpoint runtimecore.Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return err
	}
	if s.fail == "preparation" && len(checkpoint.PendingCompletions) != 0 {
		return errors.New("preparation write failed")
	}
	for _, task := range checkpoint.Graph.Tasks {
		if task.Status != domain.TaskStatusSucceeded {
			continue
		}
		if s.fail == "completion-save" {
			return errors.New("completion write failed")
		}
		if s.fail == "completion-ack" {
			s.data = append([]byte(nil), data...)
			return completionAckLost{}
		}
	}
	s.data = append([]byte(nil), data...)
	return nil
}

type completionAckLost struct{}

func (completionAckLost) Error() string         { return "completion saved but acknowledgement lost" }
func (completionAckLost) CommitUncertain() bool { return true }
