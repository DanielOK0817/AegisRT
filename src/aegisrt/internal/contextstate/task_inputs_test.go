package contextstate

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
)

func TestTaskInputsIsolateSameAgentAcrossRestartAndLedgerEviction(t *testing.T) {
	config := testConfig()
	config.Limits.MaxLedgerEntries = 1
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(config, store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first, second := testTask(), testTask()
	second.ID = "task-2"
	appendTaskInput(t, repository, first, "first task material", "first")
	appendTaskInput(t, repository, second, "second task material", "second")
	assertTaskInput(t, repository, first, "first task material")
	assertTaskInput(t, repository, second, "second task material")
	restored, err := NewPersistent(config, store, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertTaskInput(t, restored, first, "first task material")
	assertTaskInput(t, restored, second, "second task material")
	third := first
	third.ID = "task-3"
	assertTaskInput(t, restored, third, "")
}

func TestTaskInputsDoNotInheritSiblingDependencyOutputs(t *testing.T) {
	repository, producer, consumer := outputRepository(t)
	if _, err := repository.PublishOutput(context.Background(), testOutputCommand(producer, consumer), time.Now()); err != nil {
		t.Fatal(err)
	}
	task := dynamicOutputTask()
	assertTaskInput(t, repository, task, "shared analysis")
	task.ID = "unrelated-task"
	task.Dependencies = nil
	assertTaskInput(t, repository, task, "")
}

func TestTaskInputsReuseSharedContentOnlyAfterExplicitBinding(t *testing.T) {
	repository, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	first, second := testTask(), testTask()
	second.ID = "task-2"
	one := appendTaskInput(t, repository, first, "reusable content", "first")
	assertTaskInput(t, repository, second, "")
	two := appendTaskInput(t, repository, second, "reusable content", "second")
	if one.PageID != two.PageID || two.ManifestChanged || repository.Stats().Store.UniquePages != 1 {
		t.Fatal("task binding duplicated shared storage")
	}
	assertTaskInput(t, repository, first, "reusable content")
	assertTaskInput(t, repository, second, "reusable content")
	other := first
	other.WorkflowID = "other-workflow"
	appendTaskInput(t, repository, other, "other workflow content", "other")
	assertTaskInput(t, repository, other, "other workflow content")
	assertTaskInput(t, repository, first, "reusable content")
}

func TestTaskInputsRejectUnauthorizedDependencyDespiteAgentHistory(t *testing.T) {
	repository, producer, consumer := outputRepository(t)
	command := testOutputCommand(producer, consumer)
	command.Targets = command.Targets[:1]
	command.Artifacts[0].Targets = []string{"producer"}
	command.Artifacts[0].Recipients = []string{"producer"}
	if _, err := repository.PublishOutput(context.Background(), command, time.Now()); err != nil {
		t.Fatal(err)
	}
	task := dynamicOutputTask()
	assertTaskInput(t, repository, task, "")
	task.WorkflowID = "other-workflow"
	if _, _, err := repository.EnsureEmptyWithOwnership(task.WorkflowID, task.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Resolve(context.Background(), task, dynamicOutputAgent(), 64); !errors.Is(err, ErrInvalidTaskBinding) {
		t.Fatalf("cross-workflow dependency error = %v", err)
	}
}

func TestTaskInputsSameAgentDependencyRetainsPinnedOutput(t *testing.T) {
	repository, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	task := testTask()
	head, _, err := repository.EnsureEmptyWithOwnership(task.WorkflowID, task.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.PublishOutput(context.Background(), OutputCommand{
		TaskID: "producer-task", WorkflowID: task.WorkflowID, ProducerAgentID: task.AgentID,
		Targets: []OutputTarget{{AgentID: task.AgentID, ExpectedManifestID: head.ID, TaskTokenBudget: 64, PrincipalPinnedBudget: 64}},
		Artifacts: []ArtifactCommand{{Name: "result", Content: "pinned result", Visibility: VisibilityWorkflow,
			Recipients: []string{task.AgentID}, Targets: []string{task.AgentID}, Pin: true}},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	task.Dependencies = []string{"producer-task"}
	assertTaskInput(t, repository, task, "pinned result")
	other := task
	other.ID, other.Dependencies = "unrelated", nil
	assertTaskInput(t, repository, other, "")
}

func TestTaskInputsDeliveredOutputSurvivesLedgerEvictionAtLowerBudget(t *testing.T) {
	config := testConfig()
	config.Limits.MaxLedgerEntries = 1
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(config, store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	producer, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "producer")
	if err != nil {
		t.Fatal(err)
	}
	consumer, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "consumer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PublishOutput(context.Background(), testOutputCommand(producer.ID, consumer.ID), time.Now()); err != nil {
		t.Fatal(err)
	}
	appendTaskInput(t, repository, testTask(), "evict delivery ledger", "eviction")
	restored, err := NewPersistent(config, store, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	before := restored.Stats().Generation
	resolved, err := restored.Resolve(context.Background(), dynamicOutputTask(), dynamicOutputAgent(), 1)
	if err != nil || resolved.Text != "" || restored.Stats().Generation != before {
		t.Fatalf("delivered output was reappended: resolution=%+v error=%v generation=%d->%d", resolved, err, before, restored.Stats().Generation)
	}
}

func TestTaskInputsCheckpointRejectsLegacyAndInvalidBindings(t *testing.T) {
	config := testConfig()
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(config, store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	appendTaskInput(t, repository, testTask(), "owned content", "input")
	var original checkpoint
	if err := json.Unmarshal(store.payload, &original); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"legacy", "wrong-principal", "missing-page", "duplicate-binding", "wrong-flags"} {
		t.Run(scenario, func(t *testing.T) {
			payload, _ := json.Marshal(original)
			var stored checkpoint
			if err := json.Unmarshal(payload, &stored); err != nil {
				t.Fatal(err)
			}
			want := ErrInvalidCheckpoint
			switch scenario {
			case "legacy":
				stored.FormatVersion, stored.TaskInputs = 1, nil
				want = ErrMigrationRequired
			case "wrong-principal":
				stored.TaskInputs[0].WorkflowID = "other-workflow"
			case "missing-page":
				stored.TaskInputs[0].Refs[0].PageID = contextvm.PageID("missing")
			case "duplicate-binding":
				stored.TaskInputs = append(stored.TaskInputs, stored.TaskInputs[0])
			case "wrong-flags":
				stored.TaskInputs[0].Refs[0].Pin = true
			}
			store.payload, err = json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewPersistent(config, store, false, time.Now()); !errors.Is(err, want) {
				t.Fatalf("restore error = %v, want %v", err, want)
			}
		})
	}
}

func appendTaskInput(t *testing.T, repository *Repository, task domain.TaskSpec, content, key string) AppendResult {
	t.Helper()
	head, _, err := repository.EnsureEmptyWithOwnership(task.WorkflowID, task.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.Append(context.Background(), AppendCommand{
		TaskID: task.ID, WorkflowID: task.WorkflowID, AgentID: task.AgentID,
		ExpectedManifestID: head.ID, IdempotencyKey: key, Content: content,
		Visibility: VisibilityWorkflow, TaskTokenBudget: 64, PrincipalPinnedBudget: 64,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertTaskInput(t *testing.T, repository *Repository, task domain.TaskSpec, want string) {
	t.Helper()
	agent := testAgent()
	agent.ID = task.AgentID
	resolved, err := repository.Resolve(context.Background(), task, agent, 64)
	if err != nil || resolved.Text != want {
		t.Fatalf("task %s context = %q, error = %v; want %q", task.ID, resolved.Text, err, want)
	}
}
