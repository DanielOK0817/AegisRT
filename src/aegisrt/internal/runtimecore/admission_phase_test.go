package runtimecore

import (
	"aegisrt/internal/domain"
	"context"
	"errors"
	"testing"
	"time"
)

func TestSchedulingDoesNotClaimProcessStarted(t *testing.T) {
	r := newTestRuntime(t)
	if err := r.ApplyPatch([]domain.TaskSpec{task("preparing", "workflow", nil, 0, 100)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Schedule(time.Now()); err != nil {
		t.Fatal(err)
	}
	assertTaskStatus(t, r, "preparing", domain.TaskStatusAdmitted)
}

func TestMarkStartedRejectsStaleAttemptAndCancelIntent(t *testing.T) {
	r := newTestRuntime(t)
	if err := r.ApplyPatch([]domain.TaskSpec{task("work", "workflow", nil, 0, 100)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Schedule(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkStarted("work", 2, time.Now()); err == nil {
		t.Fatal("accepted stale start")
	}
	assertTaskStatus(t, r, "work", domain.TaskStatusAdmitted)
	if _, err := r.RequestCancel("work"); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkStarted("work", 1, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel intent lost: %v", err)
	}
	assertTaskStatus(t, r, "work", domain.TaskStatusAdmitted)
}

func TestStartedAttemptCannotBeRequeuedAsUnstarted(t *testing.T) {
	r := newTestRuntime(t)
	if err := r.ApplyPatch([]domain.TaskSpec{task("work", "workflow", nil, 0, 100)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Schedule(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkStarted("work", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RequeueUnstarted("work", time.Now()); err == nil {
		t.Fatal("returned retry budget after execution started")
	}
	assertTaskStatus(t, r, "work", domain.TaskStatusRunning)
	if r.AttemptNumber("work") != 1 || r.Stats().Allocated.CPUMilli != 100 {
		t.Fatal("failed requeue changed accounting")
	}
}

func TestMarkStartedCheckpointFailurePreservesAdmission(t *testing.T) {
	store := &memoryCheckpointStore{}
	r := openTestRuntime(t, store, testCapacity(), time.Now())
	if err := r.ApplyPatch([]domain.TaskSpec{task("work", "workflow", nil, 0, 100)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Schedule(time.Now()); err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("injected start checkpoint failure")
	if err := r.MarkStarted("work", 1, time.Now()); !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("start error=%v", err)
	}
	assertTaskStatus(t, r, "work", domain.TaskStatusAdmitted)
	if r.Healthy() || r.Stats().Allocated.CPUMilli != 100 {
		t.Fatal("uncertain start lost recovery reservation")
	}
}
