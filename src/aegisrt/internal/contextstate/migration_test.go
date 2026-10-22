package contextstate

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"aegisrt/internal/domain"
)

func legacyInputFixture(t *testing.T, evicted bool) (Config, []byte, []domain.TaskSpec) {
	t.Helper()
	config := testConfig()
	if evicted {
		config.Limits.MaxLedgerEntries = 1
	}
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	r, err := NewPersistent(config, store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	task := testTask()
	other := task
	other.ID = "other-task"
	if _, _, err := r.EnsureEmptyWithOwnership(task.WorkflowID, task.AgentID); err != nil {
		t.Fatal(err)
	}
	appendTaskInput(t, r, task, "first task material", "first")
	appendTaskInput(t, r, other, "second task material", "second")
	var checkpoint checkpoint
	if err := json.Unmarshal(store.payload, &checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint.FormatVersion = 1
	checkpoint.TaskInputs = nil
	checkpoint.CounterIdentity = ""
	checkpoint.SummarizerIdentity = ""
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return config, payload, []domain.TaskSpec{task, other}
}

func TestLegacyMigrationReconstructsTaskIsolationFromCompleteHistory(t *testing.T) {
	config, payload, tasks := legacyInputFixture(t, false)
	before := string(payload)
	converted, err := MigrateLegacyCheckpoint(config, payload, tasks, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != before {
		t.Fatal("source mutated")
	}
	r, err := NewPersistent(config, &scriptedStore{payload: converted}, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i, task := range tasks {
		result, err := r.Resolve(context.Background(), task, testAgent(), 64)
		want := []string{"first task material", "second task material"}[i]
		if err != nil || result.Text != want {
			t.Fatalf("task=%s result=%+v err=%v", task.ID, result, err)
		}
	}
}

func TestLegacyMigrationRejectsEvictedOwnershipAndWrongTask(t *testing.T) {
	config, payload, tasks := legacyInputFixture(t, true)
	if _, err := MigrateLegacyCheckpoint(config, payload, tasks, nil, time.Now()); !errors.Is(err, ErrMigrationOwnershipRequired) {
		t.Fatalf("evicted=%v", err)
	}
	config, payload, tasks = legacyInputFixture(t, false)
	tasks[0].AgentID = "another-agent"
	if _, err := MigrateLegacyCheckpoint(config, payload, tasks, nil, time.Now()); !errors.Is(err, ErrInvalidTaskBinding) {
		t.Fatalf("mismatched task=%v", err)
	}
}

func TestLegacyMigrationExplicitOwnershipIsValidated(t *testing.T) {
	config, payload, tasks := legacyInputFixture(t, true)
	var saved checkpoint
	if err := json.Unmarshal(payload, &saved); err != nil {
		t.Fatal(err)
	}
	bindings := []MigrationBinding{}
	for _, m := range saved.Manifests {
		if m.Revision == 3 {
			refs := m.Refs()
			for i, task := range tasks {
				bindings = append(bindings, MigrationBinding{task.ID, task.WorkflowID, task.AgentID, refs[i : i+1]})
			}
		}
	}
	converted, err := MigrateLegacyCheckpoint(config, payload, tasks, bindings, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewPersistent(config, &scriptedStore{payload: converted}, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Resolve(context.Background(), tasks[0], testAgent(), 64)
	if err != nil || result.Text != "first task material" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	bindings[0].AgentID = "wrong-agent"
	if _, err := MigrateLegacyCheckpoint(config, payload, tasks, bindings, time.Now()); err == nil {
		t.Fatal("invalid explicit owner accepted")
	}
}
