package runtimecore

import (
	"bytes"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/domain"
)

func TestPatchReplayPreservesCheckpointAfterRestart(t *testing.T) {
	store := &memoryCheckpointStore{}
	now := time.Unix(12_000, 0).UTC()
	runtime := openTestRuntime(t, store, testCapacity(), now)
	tasks := []domain.TaskSpec{task("root", "workflow", nil, 1, 100)}
	if err := runtime.ApplyPatch(tasks, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Complete("root", domain.ResourceVector{CPUMilli: 100}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runtime, _ = reopenTestRuntime(t, store, testCapacity(), now.Add(time.Minute))
	before := runtime.Stats()
	payload := append([]byte(nil), store.data...)
	store.saveErr = errors.New("replay must not write another checkpoint")
	if err := runtime.ApplyPatch(tasks, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("identical replay failed: %v", err)
	}
	if !reflect.DeepEqual(runtime.Stats(), before) || !bytes.Equal(store.data, payload) {
		t.Fatal("replay changed statistics or durable checkpoint")
	}
	assertTaskStatus(t, runtime, "root", domain.TaskStatusSucceeded)
	if runtime.AttemptNumber("root") != 1 || !runtime.Healthy() {
		t.Fatal("replay changed attempt count or checkpoint health")
	}
}

func TestConcurrentPatchReplayWritesOneCheckpoint(t *testing.T) {
	store := &recordingCheckpointStore{}
	now := time.Unix(13_000, 0).UTC()
	runtime := openTestRuntime(t, store, testCapacity(), now)
	tasks := []domain.TaskSpec{task("root", "workflow", nil, 1, 100)}
	var workers sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errs <- runtime.ApplyPatch(tasks, now)
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent replay: %v", err)
		}
	}
	if generations := store.Generations(); !reflect.DeepEqual(generations, []uint64{1, 2}) {
		t.Fatalf("checkpoint generations=%v, want initialization and one patch", generations)
	}
}
