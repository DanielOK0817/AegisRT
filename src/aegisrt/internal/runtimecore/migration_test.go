package runtimecore

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/scheduler"
)

func TestPersistentRuntimeRejectsV1MigrationByDefault(t *testing.T) {
	capacity := testCapacity()
	now := time.Unix(8_900, 0).UTC()
	store := seededCheckpoint(t, capacity, "config-a", now)
	downgradeStoreToV1(t, store)
	before := append([]byte(nil), store.data...)
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = NewPersistent(policy, capacity, "config-a", store, now.Add(time.Second)); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("NewPersistent error = %v, want ErrInvalidCheckpoint", err)
	}
	if !bytes.Equal(store.data, before) {
		t.Fatal("default v1 rejection rewrote checkpoint")
	}
}

func TestPersistentRuntimeExplicitlyMigratesV1AndKeepsInstanceID(t *testing.T) {
	capacity := testCapacity()
	now := time.Unix(9_000, 0).UTC()
	store := seededCheckpoint(t, capacity, "config-a", now)
	downgradeStoreToV1(t, store)

	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	migrated, report, err := NewPersistentWithOptions(
		policy, capacity, "config-a", store, now.Add(time.Second),
		PersistentOptions{AllowLegacyV1Migration: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Loaded || !report.Migrated {
		t.Fatalf("migration report = %+v, want loaded migration", report)
	}
	if !migrated.LegacyContextMigrationPending() {
		t.Fatal("v1 migration did not mark legacy Context migration pending")
	}
	if err = validateRuntimeInstanceID(migrated.InstanceID()); err != nil {
		t.Fatalf("migrated instance ID is invalid: %v", err)
	}
	var persisted Checkpoint
	if err = json.Unmarshal(store.data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.FormatVersion != checkpointFormatVersion || persisted.InstanceID != migrated.InstanceID() {
		t.Fatalf("persisted migration = %+v, want current format and runtime identity", persisted)
	}

	reopened, secondReport := reopenTestRuntime(t, store, capacity, now.Add(2*time.Second))
	if secondReport.Migrated || reopened.InstanceID() != migrated.InstanceID() ||
		!reopened.LegacyContextMigrationPending() {
		t.Fatalf("reopened ID/report = (%q, %+v), want stable non-migration", reopened.InstanceID(), secondReport)
	}
	if err = reopened.CompleteLegacyContextMigration(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if reopened.LegacyContextMigrationPending() {
		t.Fatal("completed legacy Context migration remains pending")
	}
	completed, completedReport := reopenTestRuntime(t, store, capacity, now.Add(4*time.Second))
	if completedReport.Migrated || completed.LegacyContextMigrationPending() {
		t.Fatalf("completed migration reopened as pending: %+v", completedReport)
	}
}

func TestPersistentRuntimeMigrationValidationFailureDoesNotRewriteV1(t *testing.T) {
	capacity := testCapacity()
	now := time.Unix(9_100, 0).UTC()
	for _, test := range []struct {
		name        string
		fingerprint string
		mutate      func(map[string]any, []byte) []byte
	}{
		{name: "configuration mismatch", fingerprint: "config-b"},
		{name: "unknown field", fingerprint: "config-a", mutate: func(document map[string]any, _ []byte) []byte {
			document["unexpected"] = true
			return marshalDocument(t, document)
		}},
		{name: "trailing value", fingerprint: "config-a", mutate: func(_ map[string]any, payload []byte) []byte {
			return append(payload, []byte(" {}")...)
		}},
		{name: "missing generation", fingerprint: "config-a", mutate: func(document map[string]any, _ []byte) []byte {
			document["generation"] = float64(0)
			return marshalDocument(t, document)
		}},
		{name: "missing saved at", fingerprint: "config-a", mutate: func(document map[string]any, _ []byte) []byte {
			document["saved_at"] = "0001-01-01T00:00:00Z"
			return marshalDocument(t, document)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := seededCheckpoint(t, capacity, "config-a", now)
			document := downgradeStoreToV1(t, store)
			if test.mutate != nil {
				store.data = test.mutate(document, store.data)
			}
			before := append([]byte(nil), store.data...)
			policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = NewPersistentWithOptions(
				policy, capacity, test.fingerprint, store, now.Add(time.Second),
				PersistentOptions{AllowLegacyV1Migration: true},
			)
			if !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("migration error = %v, want ErrInvalidCheckpoint", err)
			}
			if !bytes.Equal(store.data, before) {
				t.Fatal("failed migration rewrote v1 checkpoint")
			}
		})
	}
}

func TestPersistentRuntimeMigrationHookFailureDoesNotRewriteV1(t *testing.T) {
	capacity := testCapacity()
	now := time.Unix(9_200, 0).UTC()
	store := &memoryCheckpointStore{}
	runtime := openTestRuntime(t, store, capacity, now)
	if err := runtime.ApplyPatch([]domain.TaskSpec{task("active", "workflow", nil, 1, 100)}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(now); err != nil {
		t.Fatal(err)
	}
	downgradeStoreToV1(t, store)
	before := append([]byte(nil), store.data...)
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	hookErr := errors.New("cleanup failed")
	_, _, err = NewPersistentWithOptions(
		policy, capacity, "config-a", store, now.Add(time.Second),
		PersistentOptions{
			AllowLegacyV1Migration: true,
			RecoveryHook:           func(string, int) error { return hookErr },
		},
	)
	if !errors.Is(err, ErrRecoveryBlocked) {
		t.Fatalf("migration error = %v, want ErrRecoveryBlocked", err)
	}
	if !bytes.Equal(store.data, before) {
		t.Fatal("failed recovery hook rewrote v1 checkpoint")
	}
}

func TestPersistentRuntimeMigrationSaveFailureKeepsV1(t *testing.T) {
	capacity := testCapacity()
	now := time.Unix(9_300, 0).UTC()
	store := seededCheckpoint(t, capacity, "config-a", now)
	downgradeStoreToV1(t, store)
	before := append([]byte(nil), store.data...)
	store.saveErr = errors.New("disk full")
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = NewPersistentWithOptions(
		policy, capacity, "config-a", store, now.Add(time.Second),
		PersistentOptions{AllowLegacyV1Migration: true},
	)
	if !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("migration error = %v, want ErrCheckpointUnavailable", err)
	}
	if !bytes.Equal(store.data, before) {
		t.Fatal("definite migration save failure rewrote v1 checkpoint")
	}
}

func TestCompleteLegacyContextMigrationSaveFailureRollsBack(t *testing.T) {
	capacity := testCapacity()
	now := time.Unix(9_400, 0).UTC()
	store := seededCheckpoint(t, capacity, "config-a", now)
	downgradeStoreToV1(t, store)
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runtime, _, err := NewPersistentWithOptions(
		policy, capacity, "config-a", store, now.Add(time.Second),
		PersistentOptions{AllowLegacyV1Migration: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), store.data...)
	store.saveErr = errors.New("disk full")
	if err = runtime.CompleteLegacyContextMigration(now.Add(2 * time.Second)); !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("complete migration error = %v, want ErrCheckpointUnavailable", err)
	}
	if !runtime.LegacyContextMigrationPending() {
		t.Fatal("failed checkpoint incorrectly published completed migration")
	}
	if !bytes.Equal(store.data, before) {
		t.Fatal("failed completion changed durable checkpoint")
	}
	store.saveErr = nil
	reopened, _ := reopenTestRuntime(t, store, capacity, now.Add(3*time.Second))
	if !reopened.LegacyContextMigrationPending() {
		t.Fatal("failed completion became durable")
	}
}

func downgradeStoreToV1(t *testing.T, store *memoryCheckpointStore) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(store.data, &document); err != nil {
		t.Fatal(err)
	}
	document["format_version"] = float64(1)
	delete(document, "instance_id")
	delete(document, "legacy_context_migration_pending")
	store.data = marshalDocument(t, document)
	return document
}

func marshalDocument(t *testing.T, document map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
