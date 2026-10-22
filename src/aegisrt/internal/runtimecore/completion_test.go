package runtimecore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
	"aegisrt/internal/scheduler"
)

func completionFixture(t *testing.T) (*Runtime, *memoryCheckpointStore, []agentprotocol.Artifact) {
	t.Helper()
	store := &memoryCheckpointStore{}
	r := openTestRuntime(t, store, testCapacity(), time.Now())
	if err := r.ApplyPatch([]domain.TaskSpec{task("work", "workflow", nil, 0, 100)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Schedule(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkStarted("work", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	return r, store, []agentprotocol.Artifact{{Name: "result", Content: "saved original result", Visibility: "workflow"}}
}

func TestPendingCompletionSurvivesRestartWithoutRerunOrCleanup(t *testing.T) {
	r, store, artifacts := completionFixture(t)
	if err := r.PrepareCompletion("work", 1, artifacts, time.Now()); err != nil {
		t.Fatal(err)
	}
	policy, _ := scheduler.NewDAGDRCFS(testCapacity(), time.Second)
	restored, report, err := NewPersistent(policy, testCapacity(), "config-a", store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.RequeuedTaskIDs) != 0 || restored.AttemptNumber("work") != 1 {
		t.Fatalf("recovery retried committed work: %+v", report)
	}
	assertTaskStatus(t, restored, "work", domain.TaskStatusRunning)
	if len(restored.PendingCompletions()) != 1 || restored.Stats().Allocated.CPUMilli != 100 {
		t.Fatal("pending output or reservation lost")
	}
	if _, err := restored.Complete("work", domain.ResourceVector{CPUMilli: 100}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(restored.PendingCompletions()) != 0 || !restored.Stats().Allocated.IsZero() {
		t.Fatal("completion did not settle exactly once")
	}
}

func TestPendingCompletionRejectsConflictsAndCannotBeCanceledOrFailed(t *testing.T) {
	r, _, artifacts := completionFixture(t)
	if err := r.PrepareCompletion("work", 1, artifacts, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.PrepareCompletion("work", 1, artifacts, time.Now()); err != nil {
		t.Fatal(err)
	}
	artifacts[0].Content = "different retry output"
	if err := r.PrepareCompletion("work", 1, artifacts, time.Now()); !errors.Is(err, ErrCompletionPending) {
		t.Fatalf("conflict=%v", err)
	}
	for name, operation := range map[string]func() error{
		"cancel":    func() error { _, err := r.Cancel("work"); return err },
		"intent":    func() error { _, err := r.RequestCancel("work"); return err },
		"failure":   func() error { _, err := r.Fail("work", domain.ResourceVector{}); return err },
		"interrupt": func() error { _, err := r.Interrupt("work", time.Now()); return err },
	} {
		if err := operation(); !errors.Is(err, ErrCompletionPending) {
			t.Fatalf("%s lost completion: %v", name, err)
		}
	}
	copy := r.PendingCompletions()
	copy[0].Artifacts[0].Content = "mutated caller copy"
	if r.PendingCompletions()[0].Artifacts[0].Content != "saved original result" {
		t.Fatal("mutable pending output")
	}
}

func TestCancellationBeforePreparationWins(t *testing.T) {
	r, _, artifacts := completionFixture(t)
	if _, err := r.RequestCancel("work"); err != nil {
		t.Fatal(err)
	}
	if err := r.PrepareCompletion("work", 1, artifacts, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if len(r.PendingCompletions()) != 0 {
		t.Fatal("canceled output committed")
	}
}

func TestPreparationSaveFailureRecoveryUsesDurableDecision(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-written", true: "written-ack-lost"}[uncertain], func(t *testing.T) {
			r, store, artifacts := completionFixture(t)
			store.saveErr = errors.New("save failure")
			if uncertain {
				store.saveErr = uncertainSaveError{cause: store.saveErr}
				store.saveOnError = true
			}
			if err := r.PrepareCompletion("work", 1, artifacts, time.Now()); !errors.Is(err, ErrCheckpointUnavailable) {
				t.Fatalf("prepare=%v", err)
			}
			store.saveErr = nil
			restored, report := reopenTestRuntime(t, store, testCapacity(), time.Now())
			if uncertain {
				if len(restored.PendingCompletions()) != 1 || len(report.RequeuedTaskIDs) != 0 {
					t.Fatal("lost saved completion")
				}
			} else if len(restored.PendingCompletions()) != 0 || len(report.RequeuedTaskIDs) != 1 {
				t.Fatal("invented unsaved completion")
			}
		})
	}
}

func TestRestoreRejectsCorruptPendingAttempt(t *testing.T) {
	r, store, artifacts := completionFixture(t)
	if err := r.PrepareCompletion("work", 1, artifacts, time.Now()); err != nil {
		t.Fatal(err)
	}
	var saved Checkpoint
	if err := json.Unmarshal(store.data, &saved); err != nil {
		t.Fatal(err)
	}
	saved.PendingCompletions[0].AttemptNumber = 2
	store.data, _ = json.Marshal(saved)
	policy, _ := scheduler.NewDAGDRCFS(testCapacity(), time.Second)
	if _, _, err := NewPersistent(policy, testCapacity(), "config-a", store, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("corrupt pending accepted: %v", err)
	}
}
