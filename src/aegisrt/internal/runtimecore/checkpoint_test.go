package runtimecore

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/scheduler"
)

func TestPersistentRuntimeRoundTripPreservesFairnessAndAttempts(t *testing.T) {
	store := &memoryCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(1_000, 0).UTC()
	runtime := openTestRuntime(t, store, capacity, now)
	if err := runtime.ApplyPatch([]domain.TaskSpec{
		task("a1", "a", nil, 0, 500),
		task("a2", "a", nil, 0, 500),
		task("b1", "b", nil, 0, 500),
	}, now); err != nil {
		t.Fatal(err)
	}
	first, err := runtime.Schedule(now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Candidate.TaskID != "a1" {
		t.Fatalf("first task = %q, want a1", first.Candidate.TaskID)
	}
	if _, err = runtime.Complete("a1", domain.ResourceVector{CPUMilli: 500}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	restored, report := reopenTestRuntime(t, store, capacity, now.Add(2*time.Second))
	if report.Recovered || len(report.RequeuedTaskIDs) != 0 || len(report.FailedTaskIDs) != 0 {
		t.Fatalf("unexpected recovery report: %+v", report)
	}
	assertTaskStatus(t, restored, "a1", domain.TaskStatusSucceeded)
	if got := restored.AttemptNumber("a1"); got != 1 {
		t.Fatalf("attempt number = %d, want 1", got)
	}
	second, err := restored.Schedule(now.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if second.Candidate.TaskID != "b1" {
		t.Fatalf("task after restart = %q, want b1 from preserved vruntime", second.Candidate.TaskID)
	}
}

func TestPersistentRuntimeRestoresInstanceID(t *testing.T) {
	store := &memoryCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(1_500, 0).UTC()
	first := openTestRuntime(t, store, capacity, now)
	firstID := first.InstanceID()
	if first.LegacyContextMigrationPending() {
		t.Fatal("new v2 Runtime unexpectedly has legacy Context migration pending")
	}

	restored, report := reopenTestRuntime(t, store, capacity, now.Add(time.Second))
	if !report.Loaded {
		t.Fatalf("recovery report = %+v, want loaded checkpoint", report)
	}
	if got := restored.InstanceID(); got != firstID {
		t.Fatalf("restored instance ID = %q, want %q", got, firstID)
	}
}

func TestPersistentRuntimeRejectsInvalidInstanceIDAndLegacyFormat(t *testing.T) {
	capacity := testCapacity()
	now := time.Unix(1_600, 0).UTC()
	seed := seededCheckpoint(t, capacity, "config-a", now)
	var valid Checkpoint
	if err := json.Unmarshal(seed.data, &valid); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Checkpoint)
	}{
		{name: "missing", mutate: func(value *Checkpoint) { value.InstanceID = "" }},
		{name: "wrong length", mutate: func(value *Checkpoint) { value.InstanceID = strings.Repeat("a", 62) }},
		{name: "non hex", mutate: func(value *Checkpoint) { value.InstanceID = strings.Repeat("z", 64) }},
		{name: "non canonical uppercase", mutate: func(value *Checkpoint) { value.InstanceID = strings.Repeat("A", 64) }},
		{name: "legacy format", mutate: func(value *Checkpoint) { value.FormatVersion = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			payload, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			store := &memoryCheckpointStore{data: payload}
			policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = NewPersistent(policy, capacity, "config-a", store, now); !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("NewPersistent error = %v, want ErrInvalidCheckpoint", err)
			}
		})
	}
}

func TestPersistentRuntimeRequeuesInterruptedAttempt(t *testing.T) {
	store := &memoryCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(2_000, 0).UTC()
	runtime := openTestRuntime(t, store, capacity, now)
	if err := runtime.ApplyPatch([]domain.TaskSpec{task("work", "workflow", nil, 1, 300)}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(now); err != nil {
		t.Fatal(err)
	}

	restored, report := reopenTestRuntime(t, store, capacity, now.Add(time.Minute))
	if !report.Recovered || len(report.RequeuedTaskIDs) != 1 || report.RequeuedTaskIDs[0] != "work" {
		t.Fatalf("recovery report = %+v, want work requeued", report)
	}
	assertTaskStatus(t, restored, "work", domain.TaskStatusReady)
	if !restored.Stats().Allocated.IsZero() {
		t.Fatalf("restored allocation = %+v, want zero", restored.Stats().Allocated)
	}
	if got := restored.AttemptNumber("work"); got != 1 {
		t.Fatalf("attempt number after recovery = %d, want 1", got)
	}
	if _, err := restored.Schedule(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := restored.AttemptNumber("work"); got != 2 {
		t.Fatalf("attempt number after retry = %d, want 2", got)
	}
}

func TestPersistentRuntimeFailsInterruptedTaskAtAttemptLimit(t *testing.T) {
	store := &memoryCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(3_000, 0).UTC()
	runtime := openTestRuntime(t, store, capacity, now)
	spec := task("one-shot", "workflow", nil, 1, 300)
	spec.MaxAttempts = 1
	if err := runtime.ApplyPatch([]domain.TaskSpec{spec}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(now); err != nil {
		t.Fatal(err)
	}

	restored, report := reopenTestRuntime(t, store, capacity, now.Add(time.Minute))
	if !report.Recovered || len(report.FailedTaskIDs) != 1 || report.FailedTaskIDs[0] != spec.ID {
		t.Fatalf("recovery report = %+v, want exhausted task failed", report)
	}
	assertTaskStatus(t, restored, spec.ID, domain.TaskStatusFailed)
	if !restored.Stats().Allocated.IsZero() {
		t.Fatalf("restored allocation = %+v, want zero", restored.Stats().Allocated)
	}
}

func TestPersistentRuntimeHonorsDurableCancelIntent(t *testing.T) {
	store := &memoryCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(4_000, 0).UTC()
	runtime := openTestRuntime(t, store, capacity, now)
	if err := runtime.ApplyPatch([]domain.TaskSpec{task("canceling", "workflow", nil, 1, 300)}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RequestCancel("canceling"); err != nil {
		t.Fatal(err)
	}

	restored, report := reopenTestRuntime(t, store, capacity, now.Add(time.Minute))
	if !report.Recovered || len(report.CanceledTaskIDs) != 1 || report.CanceledTaskIDs[0] != "canceling" {
		t.Fatalf("recovery report = %+v, want canceling canceled", report)
	}
	assertTaskStatus(t, restored, "canceling", domain.TaskStatusCanceled)
}

func TestPersistentRuntimeRecoveryHookFailureLeavesCheckpointUntouched(t *testing.T) {
	store := &memoryCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(4_500, 0).UTC()
	runtime := openTestRuntime(t, store, capacity, now)
	if err := runtime.ApplyPatch([]domain.TaskSpec{task("orphan", "workflow", nil, 1, 300)}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(now); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), store.data...)
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	hookErr := errors.New("orphan cleanup failed")
	_, _, err = NewPersistent(policy, capacity, "config-a", store, now.Add(time.Minute),
		func(taskID string, attemptNumber int) error {
			if taskID != "orphan" || attemptNumber != 1 {
				t.Fatalf("cleanup target = (%q, %d)", taskID, attemptNumber)
			}
			return hookErr
		})
	if !errors.Is(err, ErrRecoveryBlocked) {
		t.Fatalf("NewPersistent error = %v, want ErrRecoveryBlocked", err)
	}
	if !bytes.Equal(store.data, before) {
		t.Fatal("failed cleanup changed durable checkpoint")
	}
}

func TestPersistentRuntimeRejectsActiveRecoveryWithoutCleanupHook(t *testing.T) {
	store := &memoryCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(4_600, 0).UTC()
	runtime := openTestRuntime(t, store, capacity, now)
	if err := runtime.ApplyPatch([]domain.TaskSpec{task("unsafe", "workflow", nil, 1, 300)}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(now); err != nil {
		t.Fatal(err)
	}
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = NewPersistent(policy, capacity, "config-a", store, now.Add(time.Minute)); !errors.Is(err, ErrRecoveryBlocked) {
		t.Fatalf("NewPersistent error = %v, want ErrRecoveryBlocked", err)
	}
}

func TestCheckpointFailureRollsBackAndPoisonsRuntime(t *testing.T) {
	store := &memoryCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(5_000, 0).UTC()
	runtime := openTestRuntime(t, store, capacity, now)
	store.saveErr = errors.New("disk full")
	err := runtime.ApplyPatch([]domain.TaskSpec{task("never-visible", "workflow", nil, 1, 100)}, now)
	if !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("ApplyPatch error = %v, want ErrCheckpointUnavailable", err)
	}
	if runtime.Stats().TaskCount != 0 {
		t.Fatal("failed checkpoint left graph mutation visible")
	}
	if runtime.Healthy() {
		t.Fatal("runtime remained healthy after durable state failure")
	}
	if _, err = runtime.Schedule(now); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("Schedule error = %v, want ErrRuntimeUnavailable", err)
	}
}

func TestUncertainCheckpointCommitKeepsVisibleMutationAndPoisonsRuntime(t *testing.T) {
	store := &memoryCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(5_500, 0).UTC()
	runtime := openTestRuntime(t, store, capacity, now)
	store.saveErr = uncertainSaveError{cause: errors.New("directory sync failed")}
	store.saveOnError = true

	err := runtime.ApplyPatch([]domain.TaskSpec{task("possibly-durable", "workflow", nil, 1, 100)}, now)
	if !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("ApplyPatch error = %v, want ErrCheckpointUnavailable", err)
	}
	stats := runtime.Stats()
	if stats.TaskCount != 1 || stats.Generation != 2 {
		t.Fatalf("runtime stats = %+v, want visible task at generation 2", stats)
	}
	if runtime.Healthy() {
		t.Fatal("runtime remained healthy after uncertain durable commit")
	}

	reopened, report := reopenTestRuntime(t, store, capacity, now.Add(time.Second))
	if report.Recovered || reopened.Stats().TaskCount != 1 {
		t.Fatalf("reopened state = (%+v, %+v), want one durable task", reopened.Stats(), report)
	}
}

func TestPersistentRuntimeRejectsCorruptAndIncompatibleState(t *testing.T) {
	capacity := testCapacity()
	now := time.Unix(6_000, 0).UTC()
	for _, test := range []struct {
		name        string
		store       *memoryCheckpointStore
		capacity    domain.ResourceVector
		fingerprint string
	}{
		{name: "invalid json", store: &memoryCheckpointStore{data: []byte("{")}, capacity: capacity, fingerprint: "config-a"},
		{name: "capacity mismatch", store: seededCheckpoint(t, capacity, "config-a", now), capacity: domain.ResourceVector{CPUMilli: 999}, fingerprint: "config-a"},
		{name: "config mismatch", store: seededCheckpoint(t, capacity, "config-a", now), capacity: capacity, fingerprint: "config-b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := scheduler.NewDAGDRCFS(test.capacity, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = NewPersistent(policy, test.capacity, test.fingerprint, test.store, now); err == nil {
				t.Fatal("NewPersistent accepted incompatible state")
			}
		})
	}
}

type memoryCheckpointStore struct {
	data        []byte
	saveErr     error
	saveOnError bool
}

func (s *memoryCheckpointStore) Load() ([]byte, error) {
	if s.data == nil {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), s.data...), nil
}

func (s *memoryCheckpointStore) Save(data []byte) error {
	if s.saveErr != nil {
		if s.saveOnError {
			s.data = append([]byte(nil), data...)
		}
		return s.saveErr
	}
	s.data = append([]byte(nil), data...)
	return nil
}

type uncertainSaveError struct {
	cause error
}

func (e uncertainSaveError) Error() string       { return e.cause.Error() }
func (e uncertainSaveError) Unwrap() error       { return e.cause }
func (uncertainSaveError) CommitUncertain() bool { return true }

func seededCheckpoint(t *testing.T, capacity domain.ResourceVector, fingerprint string, now time.Time) *memoryCheckpointStore {
	t.Helper()
	store := &memoryCheckpointStore{}
	_ = openTestRuntimeWithFingerprint(t, store, capacity, fingerprint, now)
	return store
}

func openTestRuntime(t *testing.T, store CheckpointStore, capacity domain.ResourceVector, now time.Time) *Runtime {
	t.Helper()
	return openTestRuntimeWithFingerprint(t, store, capacity, "config-a", now)
}

func openTestRuntimeWithFingerprint(t *testing.T, store CheckpointStore, capacity domain.ResourceVector, fingerprint string, now time.Time) *Runtime {
	t.Helper()
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runtime, report, err := NewPersistent(policy, capacity, fingerprint, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Recovered {
		t.Fatalf("new runtime reported recovery: %+v", report)
	}
	return runtime
}

func reopenTestRuntime(t *testing.T, store CheckpointStore, capacity domain.ResourceVector, now time.Time) (*Runtime, RecoveryReport) {
	t.Helper()
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runtime, report, err := NewPersistent(policy, capacity, "config-a", store, now,
		func(string, int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return runtime, report
}

func testCapacity() domain.ResourceVector {
	return domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 1024, LLMSlots: 2, TokenBudget: 10000}
}
