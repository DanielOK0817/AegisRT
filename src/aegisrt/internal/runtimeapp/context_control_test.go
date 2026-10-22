package runtimeapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/contextdispatch"
	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/taskgraph"
)

const runtimeappTestInstanceID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestContextControlCreatesManifestBeforePatch(t *testing.T) {
	registry := contextdispatch.NewRegistry()
	underlying := &fakeControl{registry: registry}
	control, err := NewContextControl(underlying, registry, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	task := runtimeTask("task-1", "agent-1")
	if err := control.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !underlying.sawManifest {
		t.Fatal("underlying patch ran before context manifest existed")
	}
	manifest, err := registry.Get(task.WorkflowID, task.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.WorkflowID != task.WorkflowID || manifest.AgentID != task.AgentID || len(manifest.Refs()) != 0 {
		t.Fatalf("unexpected empty manifest: %#v", manifest)
	}
}

func TestContextControlPreservesExistingManifest(t *testing.T) {
	registry := contextdispatch.NewRegistry()
	manifest, err := contextvm.NewManifest("workflow-1", "agent-1",
		[]contextvm.ContextRef{{PageID: contextvm.PageID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Set(manifest); err != nil {
		t.Fatal(err)
	}
	control, err := NewContextControl(&fakeControl{}, registry, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	if err := control.ApplyPatch([]domain.TaskSpec{runtimeTask("task-1", "agent-1")}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Get("workflow-1", "agent-1")
	if err != nil || got.ID != manifest.ID {
		t.Fatalf("existing manifest was replaced: got=%#v error=%v", got, err)
	}
}

func TestContextControlRejectsUnknownAgentBeforeMutation(t *testing.T) {
	registry := contextdispatch.NewRegistry()
	underlying := &fakeControl{}
	control, err := NewContextControl(underlying, registry, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	task := runtimeTask("task-1", "agent-2")
	if err := control.ApplyPatch([]domain.TaskSpec{task}, time.Now()); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("error = %v, want ErrUnknownAgent", err)
	}
	if underlying.patchCalls != 0 {
		t.Fatal("unknown agent reached underlying control")
	}
	if _, err := registry.Get(task.WorkflowID, task.AgentID); !errors.Is(err, contextdispatch.ErrManifestNotFound) {
		t.Fatalf("unknown agent created manifest: %v", err)
	}
}

func TestContextControlRollsBackManifestsWhenPatchRejected(t *testing.T) {
	registry := contextdispatch.NewRegistry()
	underlying := &fakeControl{resultErr: errors.New("patch rejected")}
	control, err := NewContextControl(underlying, registry, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	task := runtimeTask("rejected", "agent-1")
	if err := control.ApplyPatch([]domain.TaskSpec{task}, time.Now()); !errors.Is(err, underlying.resultErr) {
		t.Fatalf("ApplyPatch error = %v, want patch rejection", err)
	}
	if _, err := registry.Get(task.WorkflowID, task.AgentID); !errors.Is(err, contextdispatch.ErrManifestNotFound) {
		t.Fatalf("rejected patch retained orphan manifest: %v", err)
	}
}

func TestContextControlRetainsManifestWhenRuntimeCommitIsUncertain(t *testing.T) {
	registry := contextdispatch.NewRegistry()
	underlying := &fakeControl{resultErr: runtimecore.ErrCheckpointUnavailable}
	control, err := NewContextControl(underlying, registry, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	task := runtimeTask("uncertain", "agent-1")
	if err := control.ApplyPatch([]domain.TaskSpec{task}, time.Now()); !errors.Is(err, runtimecore.ErrCheckpointUnavailable) {
		t.Fatalf("ApplyPatch error = %v", err)
	}
	if _, err := registry.Get(task.WorkflowID, task.AgentID); err != nil {
		t.Fatalf("uncertain Runtime commit lost its precommitted manifest: %v", err)
	}
}

func TestContextControlDerivesAppendOwnershipFromTask(t *testing.T) {
	repository, err := contextstate.New(contextstate.Config{
		Fingerprint: "runtimeapp-test-v1", RuntimeInstanceID: runtimeappTestInstanceID,
		Counter: contextvm.WhitespaceCounter{},
		Limits:  contextstate.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	task := runtimeTask("task-context", "agent-1")
	task.Resources.TokenBudget = 32
	underlying := &fakeControl{tasks: []taskgraph.TaskSnapshot{{Spec: task, Status: domain.TaskStatusReady}}}
	control, err := NewContextControl(underlying, repository, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := repository.EnsureEmptyWithOwnership(task.WorkflowID, task.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := control.AppendContext(context.Background(), AppendContextCommand{
		TaskID: task.ID, ExpectedManifestID: manifest.ID, IdempotencyKey: "append-1",
		Content: "owned by admitted task", Visibility: contextstate.VisibilityAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskID != task.ID || result.ManifestID == manifest.ID {
		t.Fatalf("unexpected append result: %#v", result)
	}
	snapshot, err := control.InspectContext(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TaskID != task.ID || snapshot.Manifest.ManifestID != result.ManifestID {
		t.Fatalf("unexpected context snapshot: %#v", snapshot)
	}
	if _, err := repository.Get("attacker-workflow", task.AgentID); !errors.Is(err, contextdispatch.ErrManifestNotFound) {
		t.Fatalf("client data escaped derived principal: %v", err)
	}
}

func TestContextControlRejectsTaskWhoseBudgetCannotFitPinnedContext(t *testing.T) {
	repository, err := contextstate.New(contextstate.Config{
		Fingerprint: "runtimeapp-test-v1", RuntimeInstanceID: runtimeappTestInstanceID,
		Counter: contextvm.WhitespaceCounter{},
		Limits:  contextstate.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(context.Background(), contextstate.AppendCommand{
		TaskID: "seed-task", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: manifest.ID, IdempotencyKey: "seed-pinned",
		Content: "one two three four", Visibility: contextstate.VisibilityWorkflow,
		Pin: true, TaskTokenBudget: 8, PrincipalPinnedBudget: 8,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	underlying := &fakeControl{}
	control, err := NewContextControl(underlying, repository, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	task := runtimeTask("too-small", "agent-1")
	task.Resources.TokenBudget = 3
	if err := control.ApplyPatch([]domain.TaskSpec{task}, time.Now()); !errors.Is(err, contextstate.ErrPinnedTokenLimit) {
		t.Fatalf("ApplyPatch error = %v, want ErrPinnedTokenLimit", err)
	}
	if underlying.patchCalls != 0 {
		t.Fatal("task with impossible pinned Context reached Runtime")
	}
}

func TestContextControlChecksEverySharedPrincipalTaskBudget(t *testing.T) {
	repository, err := contextstate.New(contextstate.Config{
		Fingerprint: "runtimeapp-test-v1", RuntimeInstanceID: runtimeappTestInstanceID,
		Counter: contextvm.WhitespaceCounter{},
		Limits:  contextstate.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(context.Background(), contextstate.AppendCommand{
		TaskID: "seed-task", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: manifest.ID, IdempotencyKey: "seed-batch-pinned",
		Content: "one two three four", Visibility: contextstate.VisibilityWorkflow,
		Pin: true, TaskTokenBudget: 8, PrincipalPinnedBudget: 8,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	underlying := &fakeControl{}
	control, err := NewContextControl(underlying, repository, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	large := runtimeTask("large-budget", "agent-1")
	large.Resources.TokenBudget = 8
	small := runtimeTask("small-budget", "agent-1")
	small.Resources.TokenBudget = 3
	if err := control.ApplyPatch([]domain.TaskSpec{large, small}, time.Now()); !errors.Is(err, contextstate.ErrPinnedTokenLimit) {
		t.Fatalf("ApplyPatch error = %v, want ErrPinnedTokenLimit", err)
	}
	if underlying.patchCalls != 0 {
		t.Fatal("batch with an impossible same-principal task reached Runtime")
	}
}

func TestContextControlUsesTerminalTaskOnlyAsPrincipalBinding(t *testing.T) {
	repository, err := contextstate.New(contextstate.Config{
		Fingerprint: "runtimeapp-test-v1", RuntimeInstanceID: runtimeappTestInstanceID,
		Counter: contextvm.WhitespaceCounter{},
		Limits:  contextstate.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	task := runtimeTask("completed-task", "agent-1")
	task.Resources.TokenBudget = 16
	underlying := &fakeControl{tasks: []taskgraph.TaskSnapshot{{Spec: task, Status: domain.TaskStatusSucceeded}}}
	control, err := NewContextControl(underlying, repository, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	result, err := control.AppendContext(context.Background(), AppendContextCommand{
		TaskID: task.ID, ExpectedManifestID: manifest.ID, IdempotencyKey: "post-task-context",
		Content: "principal knowledge", Visibility: contextstate.VisibilityWorkflow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ManifestChanged {
		t.Fatalf("terminal task did not bind principal Context update: %#v", result)
	}
}

func TestContextControlConcurrentManifestEnsure(t *testing.T) {
	registry := contextdispatch.NewRegistry()
	underlying := &fakeControl{registry: registry}
	control, err := NewContextControl(underlying, registry, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	const count = 32
	var wait sync.WaitGroup
	errs := make(chan error, count)
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			task := runtimeTask("task-"+time.Unix(int64(index), 0).Format("150405"), "agent-1")
			if err := control.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
				errs <- err
			}
		}(index)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

type fakeControl struct {
	mu          sync.Mutex
	registry    *contextdispatch.Registry
	patchCalls  int
	sawManifest bool
	resultErr   error
	tasks       []taskgraph.TaskSnapshot
}

func (f *fakeControl) ApplyPatch(tasks []domain.TaskSpec, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patchCalls++
	if f.resultErr != nil {
		return f.resultErr
	}
	if f.registry == nil {
		f.sawManifest = true
		return nil
	}
	_, err := f.registry.Get(tasks[0].WorkflowID, tasks[0].AgentID)
	f.sawManifest = err == nil
	return err
}

func (*fakeControl) Cancel(taskID string) (taskgraph.TaskSnapshot, error) {
	return taskgraph.TaskSnapshot{Spec: domain.TaskSpec{ID: taskID}, Status: domain.TaskStatusCanceled}, nil
}

func (f *fakeControl) Task(taskID string) (taskgraph.TaskSnapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.tasks {
		if item.Spec.ID == taskID {
			return item, true
		}
	}
	return taskgraph.TaskSnapshot{}, false
}

func (f *fakeControl) Tasks() []taskgraph.TaskSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]taskgraph.TaskSnapshot(nil), f.tasks...)
}

func (*fakeControl) Stats() runtimecore.Stats { return runtimecore.Stats{} }

func runtimeAgent(id string) domain.AgentSpec {
	return domain.AgentSpec{
		ID: id, Name: "Runtime Agent", Runtime: "process", Entrypoint: "agent-runner",
		MaxConcurrency: 1, ExecutionTimeout: time.Second,
	}
}

func runtimeTask(id, agentID string) domain.TaskSpec {
	return domain.TaskSpec{
		ID: id, WorkflowID: "workflow-1", AgentID: agentID, Goal: "test context control",
		MaxAttempts: 1, Timeout: time.Second, IdempotencyKey: "key-" + id,
	}
}
