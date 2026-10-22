package runtimeapp

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/taskgraph"
)

func TestContextPatchReplayUsesAdmittedTaskAfterContextGrowth(t *testing.T) {
	repository, err := contextstate.New(contextstate.Config{
		Fingerprint: "patch-replay-v1", RuntimeInstanceID: runtimeappTestInstanceID,
		Counter: contextvm.WhitespaceCounter{}, Limits: contextstate.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	capacity := domain.ResourceVector{TokenBudget: 32}
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := runtimecore.New(policy, capacity)
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewContextControl(runtime, repository, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	task := runtimeTask("accepted", "agent-1")
	task.Resources.TokenBudget = 3
	if err := control.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	manifest, err := repository.Get(task.WorkflowID, task.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(context.Background(), contextstate.AppendCommand{
		TaskID: "later-task", WorkflowID: task.WorkflowID, AgentID: task.AgentID,
		ExpectedManifestID: manifest.ID, IdempotencyKey: "later-pinned",
		Content: "one two three four", Visibility: contextstate.VisibilityWorkflow,
		Pin: true, TaskTokenBudget: 8, PrincipalPinnedBudget: 8,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	contextBefore, runtimeBefore := repository.Stats(), runtime.Stats()
	if err := control.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatalf("replay revalidated changed context: %v", err)
	}
	if !reflect.DeepEqual(repository.Stats(), contextBefore) || !reflect.DeepEqual(runtime.Stats(), runtimeBefore) {
		t.Fatal("replay changed Context or Runtime state")
	}
	changed := task.Clone()
	changed.Goal = "conflicting goal"
	if err := control.ApplyPatch([]domain.TaskSpec{changed}, time.Now()); !errors.Is(err, taskgraph.ErrDuplicateTask) {
		t.Fatalf("conflicting existing task bypassed graph validation: %v", err)
	}
	if err := control.ApplyPatch([]domain.TaskSpec{task, task}, time.Now()); !errors.Is(err, taskgraph.ErrDuplicateTask) {
		t.Fatalf("same-request duplicate bypassed graph validation: %v", err)
	}
	newTask := task.Clone()
	newTask.ID, newTask.IdempotencyKey = "new-task", "new-task-key"
	if err := control.ApplyPatch([]domain.TaskSpec{newTask}, time.Now()); !errors.Is(err, contextstate.ErrPinnedTokenLimit) {
		t.Fatalf("new task bypassed current context budget: %v", err)
	}
}
