package runtimecore

import (
	"math"
	"testing"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/scheduler"
)

func TestDurationCompletionRecoveryExcludesPublicationDowntime(t *testing.T) {
	start := time.Unix(100, 0)
	store := &memoryCheckpointStore{}
	policy, _ := scheduler.NewDurationDAGDRCFS(testCapacity(), time.Second)
	r, _, err := NewPersistent(policy, testCapacity(), "duration-test", store, start)
	if err != nil {
		t.Fatal(err)
	}
	spec := task("work", "flow", nil, 0, 100)
	if err := r.ApplyPatch([]domain.TaskSpec{spec}, start); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Schedule(start); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkStarted("work", 1, start); err != nil {
		t.Fatal(err)
	}
	if err := r.PrepareCompletion("work", 1, nil, start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	restoredPolicy, _ := scheduler.NewDurationDAGDRCFS(testCapacity(), time.Second)
	restored, _, err := NewPersistent(restoredPolicy, testCapacity(), "duration-test", store, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Complete("work", spec.Resources, start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	want := 2 * spec.Resources.DominantShare(testCapacity())
	if math.Abs(restoredPolicy.VRuntime("flow")-want) > 1e-12 {
		t.Fatalf("charge=%v want=%v", restoredPolicy.VRuntime("flow"), want)
	}
}

func TestDurationCrashRecoveryOnlyChargesDurablyObservedInterval(t *testing.T) {
	start := time.Unix(100, 0)
	store := &memoryCheckpointStore{}
	policy, _ := scheduler.NewDurationDAGDRCFS(testCapacity(), time.Second)
	r, _, err := NewPersistent(policy, testCapacity(), "duration-test", store, start)
	if err != nil {
		t.Fatal(err)
	}
	spec := task("work", "flow", nil, 0, 100)
	if err := r.ApplyPatch([]domain.TaskSpec{spec}, start); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Schedule(start); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkStarted("work", 1, start); err != nil {
		t.Fatal(err)
	}
	// A later, unrelated mutation is a durable observation of ongoing service.
	if err := r.ApplyPatch([]domain.TaskSpec{task("later", "other", nil, 0, 100)}, start.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	restoredPolicy, _ := scheduler.NewDurationDAGDRCFS(testCapacity(), time.Second)
	restored, _, err := NewPersistent(restoredPolicy, testCapacity(), "duration-test", store, start.Add(time.Hour), func(string, int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := 3 * spec.Resources.DominantShare(testCapacity())
	if math.Abs(restoredPolicy.VRuntime("flow")-want) > 1e-12 || !restored.Stats().Allocated.IsZero() {
		t.Fatalf("charge=%v want=%v", restoredPolicy.VRuntime("flow"), want)
	}
}

func TestDurationQueuedMutationCannotMoveCheckpointBeforeObservedStart(t *testing.T) {
	start := time.Unix(100, 0)
	store := &memoryCheckpointStore{}
	policy, _ := scheduler.NewDurationDAGDRCFS(testCapacity(), time.Second)
	r, _, err := NewPersistent(policy, testCapacity(), "duration-test", store, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPatch([]domain.TaskSpec{task("work", "flow", nil, 0, 100)}, start); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Schedule(start); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkStarted("work", 1, start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPatch([]domain.TaskSpec{task("later", "other", nil, 0, 100)}, start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	restoredPolicy, _ := scheduler.NewDurationDAGDRCFS(testCapacity(), time.Second)
	if _, _, err := NewPersistent(restoredPolicy, testCapacity(), "duration-test", store, start.Add(time.Hour), func(string, int) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
