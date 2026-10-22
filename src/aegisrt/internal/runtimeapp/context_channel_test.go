package runtimeapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

func TestContextChannelFansOutOneWorkflowPageToDirectDependants(t *testing.T) {
	control, repository, tasks := newChannelControl(t)
	artifact := agentprotocol.Artifact{
		Name: "analysis", Content: "one durable shared result", Visibility: "workflow",
	}
	if err := control.PublishAgentOutput(context.Background(), tasks[0].ID, 1, []agentprotocol.Artifact{artifact}); err != nil {
		t.Fatal(err)
	}

	assertChannelContext(t, repository, tasks[0], "one durable shared result")
	assertChannelContext(t, repository, tasks[1], "one durable shared result")
	assertChannelContext(t, repository, tasks[2], "one durable shared result")
	report, err := repository.Inspect(tasks[3].WorkflowID, tasks[3].AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.PageCount != 0 {
		t.Fatalf("non-direct dependant received output: %+v", report)
	}
	stats := repository.Stats().Store
	if stats.UniquePages != 1 || stats.UniqueBlobs != 1 || stats.ReusedPuts != 2 {
		t.Fatalf("content was not physically reused across three principals: %+v", stats)
	}

	before := channelSnapshots(t, repository, tasks[:3])
	if err := control.PublishAgentOutput(context.Background(), tasks[0].ID, 1, []agentprotocol.Artifact{artifact}); err != nil {
		t.Fatal(err)
	}
	after := channelSnapshots(t, repository, tasks[:3])
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("idempotent replay advanced target %d: before=%+v after=%+v", index, before[index], after[index])
		}
	}
}

func TestContextChannelCommitsWholeEnvelopeAtomically(t *testing.T) {
	control, repository, tasks := newChannelControl(t)
	before := channelSnapshots(t, repository, tasks)
	oversized := strings.TrimSpace(strings.Repeat("word ", 129))
	err := control.PublishAgentOutput(context.Background(), tasks[0].ID, 1, []agentprotocol.Artifact{
		{Name: "first", Content: "must roll back", Visibility: "workflow"},
		{Name: "second", Content: oversized, Visibility: "workflow"},
	})
	if !errors.Is(err, contextstate.ErrPageTokenLimit) {
		t.Fatalf("error = %v, want ErrPageTokenLimit", err)
	}
	after := channelSnapshots(t, repository, tasks)
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("failed envelope mutated target %d: before=%+v after=%+v", index, before[index], after[index])
		}
	}
	if _, err := repository.Output(tasks[0].ID); !errors.Is(err, contextstate.ErrOutputNotFound) {
		t.Fatalf("Output error = %v, want ErrOutputNotFound", err)
	}
}

func TestContextChannelHydratesTaskAddedAfterOutputPublication(t *testing.T) {
	control, repository, tasks := newChannelControl(t)
	artifact := agentprotocol.Artifact{Name: "analysis", Content: "historical output", Visibility: "workflow"}
	if err := control.PublishAgentOutput(context.Background(), tasks[0].ID, 1, []agentprotocol.Artifact{artifact}); err != nil {
		t.Fatal(err)
	}
	late := channelTask("late-report", "reporter", []string{"producer"})
	fake := control.control.(*fakeControl)
	fake.mu.Lock()
	fake.tasks = append(fake.tasks, taskgraph.TaskSnapshot{Spec: late, Status: domain.TaskStatusReady})
	fake.mu.Unlock()
	assertChannelContext(t, repository, late, artifact.Content)
}

func TestContextChannelBoundsDefaultFanoutByUniquePrincipal(t *testing.T) {
	control, repository, producer, recipients := largeChannelControl(t, agentprotocol.MaxRecipients+1)
	before := repository.Stats()
	err := control.PublishAgentOutput(context.Background(), producer.ID, 1, []agentprotocol.Artifact{{
		Name: "broadcast", Content: "bounded broadcast", Visibility: "workflow",
	}})
	if !errors.Is(err, ErrOutputFanoutExceeded) {
		t.Fatalf("error = %v, want ErrOutputFanoutExceeded", err)
	}
	after := repository.Stats()
	if after.Store.PutCalls != before.Store.PutCalls || after.OutputCount != before.OutputCount {
		t.Fatalf("rejected fanout mutated repository: before=%+v after=%+v", before, after)
	}

	subset := append([]string(nil), recipients[:agentprotocol.MaxRecipients]...)
	if err := control.PublishAgentOutput(context.Background(), producer.ID, 1, []agentprotocol.Artifact{{
		Name: "subset", Content: "bounded subset", Visibility: "workflow", Recipients: subset,
	}}); err != nil {
		t.Fatalf("explicit maximum subset failed: %v", err)
	}
}

func TestContextChannelHonorsExplicitDirectRecipientSubset(t *testing.T) {
	control, repository, tasks := newChannelControl(t)
	artifact := agentprotocol.Artifact{
		Name: "review-input", Content: "only analyzer should receive this",
		Visibility: "workflow", Recipients: []string{"analyzer"},
	}
	if err := control.PublishAgentOutput(context.Background(), tasks[0].ID, 1, []agentprotocol.Artifact{artifact}); err != nil {
		t.Fatal(err)
	}
	assertChannelContext(t, repository, tasks[0], artifact.Content)
	assertChannelContext(t, repository, tasks[1], artifact.Content)
	assertChannelContext(t, repository, tasks[2], "")
}

func TestContextChannelRejectsNonDirectRecipientWithoutMutation(t *testing.T) {
	control, repository, tasks := newChannelControl(t)
	before := channelSnapshots(t, repository, tasks)
	err := control.PublishAgentOutput(context.Background(), tasks[0].ID, 1, []agentprotocol.Artifact{{
		Name: "skip-edge", Content: "must not bypass the DAG", Visibility: "workflow",
		Recipients: []string{"reporter"},
	}})
	if !errors.Is(err, ErrOutputRecipientForbidden) {
		t.Fatalf("error = %v, want ErrOutputRecipientForbidden", err)
	}
	after := channelSnapshots(t, repository, tasks)
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("rejected output mutated target %d: before=%+v after=%+v", index, before[index], after[index])
		}
	}
}

func TestContextChannelKeepsAgentArtifactPrivate(t *testing.T) {
	control, repository, tasks := newChannelControl(t)
	artifact := agentprotocol.Artifact{
		Name: "private-note", Content: "producer private scratchpad", Visibility: "agent",
	}
	if err := control.PublishAgentOutput(context.Background(), tasks[0].ID, 1, []agentprotocol.Artifact{artifact}); err != nil {
		t.Fatal(err)
	}
	assertChannelContext(t, repository, tasks[0], artifact.Content)
	assertChannelContext(t, repository, tasks[1], "")
	assertChannelContext(t, repository, tasks[2], "")
}

func TestContextChannelUsesProducerPageBudgetForLowBudgetConsumer(t *testing.T) {
	control, repository, tasks := newChannelControl(t)
	fake := control.control.(*fakeControl)
	fake.mu.Lock()
	fake.tasks[1].Spec.Resources.TokenBudget = 1
	fake.mu.Unlock()
	artifact := agentprotocol.Artifact{
		Name: "analysis", Content: "producer budget owns this multi token result", Visibility: "workflow",
	}
	if err := control.PublishAgentOutput(context.Background(), tasks[0].ID, 1, []agentprotocol.Artifact{artifact}); err != nil {
		t.Fatalf("low-budget consumer rejected producer output: %v", err)
	}
	snapshot, err := repository.Inspect(tasks[0].WorkflowID, tasks[1].AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PageCount != 1 {
		t.Fatalf("consumer Context page count = %d, want 1", snapshot.PageCount)
	}
}

func TestContextChannelPersistsEmptyCompletionMarker(t *testing.T) {
	control, repository, tasks := newChannelControl(t)
	if err := control.PublishAgentOutput(context.Background(), tasks[0].ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	record, err := repository.Output(tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Artifacts) != 0 {
		t.Fatalf("empty completion marker has artifacts: %+v", record)
	}
}

func TestContextChannelRejectsUnknownTaskAndCanceledContext(t *testing.T) {
	control, _, tasks := newChannelControl(t)
	artifact := agentprotocol.Artifact{Name: "result", Content: "value", Visibility: "workflow"}
	if err := control.PublishAgentOutput(context.Background(), "missing", 1, []agentprotocol.Artifact{artifact}); !errors.Is(err, taskgraph.ErrTaskNotFound) {
		t.Fatalf("missing task error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := control.PublishAgentOutput(canceled, tasks[0].ID, 1, []agentprotocol.Artifact{artifact}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func newChannelControl(t *testing.T) (*ContextControl, *contextstate.Repository, []domain.TaskSpec) {
	t.Helper()
	repository, err := contextstate.New(contextstate.Config{
		Fingerprint: "runtimeapp-channel-v1", RuntimeInstanceID: runtimeappTestInstanceID,
		Counter: contextvm.WhitespaceCounter{}, Limits: contextstate.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	tasks := []domain.TaskSpec{
		channelTask("producer", "producer", nil),
		channelTask("analyze", "analyzer", []string{"producer"}),
		channelTask("review", "reviewer", []string{"producer"}),
		channelTask("report", "reporter", []string{"analyze", "review"}),
	}
	snapshots := make([]taskgraph.TaskSnapshot, 0, len(tasks))
	agents := make([]domain.AgentSpec, 0, len(tasks))
	for _, task := range tasks {
		snapshots = append(snapshots, taskgraph.TaskSnapshot{Spec: task, Status: domain.TaskStatusReady})
		agents = append(agents, runtimeAgent(task.AgentID))
		if _, _, err := repository.EnsureEmptyWithOwnership(task.WorkflowID, task.AgentID); err != nil {
			t.Fatal(err)
		}
	}
	control, err := NewContextControl(&fakeControl{tasks: snapshots}, repository, agents)
	if err != nil {
		t.Fatal(err)
	}
	return control, repository, tasks
}

func largeChannelControl(
	t *testing.T,
	dependantCount int,
) (*ContextControl, *contextstate.Repository, domain.TaskSpec, []string) {
	t.Helper()
	repository, err := contextstate.New(contextstate.Config{
		Fingerprint: "runtimeapp-large-channel-v1", RuntimeInstanceID: runtimeappTestInstanceID,
		Counter: contextvm.WhitespaceCounter{}, Limits: contextstate.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	producer := channelTask("producer", "producer", nil)
	tasks := []taskgraph.TaskSnapshot{{Spec: producer, Status: domain.TaskStatusReady}}
	agents := []domain.AgentSpec{runtimeAgent("producer")}
	recipients := make([]string, 0, dependantCount)
	if _, _, err := repository.EnsureEmptyWithOwnership(producer.WorkflowID, producer.AgentID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < dependantCount; index++ {
		agentID := fmt.Sprintf("consumer-%d", index)
		task := channelTask(fmt.Sprintf("consume-%d", index), agentID, []string{producer.ID})
		tasks = append(tasks, taskgraph.TaskSnapshot{Spec: task, Status: domain.TaskStatusWaitingDependency})
		agents = append(agents, runtimeAgent(agentID))
		recipients = append(recipients, agentID)
		if _, _, err := repository.EnsureEmptyWithOwnership(task.WorkflowID, task.AgentID); err != nil {
			t.Fatal(err)
		}
	}
	control, err := NewContextControl(&fakeControl{tasks: tasks}, repository, agents)
	if err != nil {
		t.Fatal(err)
	}
	return control, repository, producer, recipients
}

func channelTask(id, agentID string, dependencies []string) domain.TaskSpec {
	task := runtimeTask(id, agentID)
	task.Dependencies = append([]string(nil), dependencies...)
	task.Resources.TokenBudget = 128
	return task
}

func assertChannelContext(t *testing.T, repository *contextstate.Repository, task domain.TaskSpec, want string) {
	t.Helper()
	resolved, err := repository.Resolve(context.Background(), task, runtimeAgent(task.AgentID), 128)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resolved.Text) != want {
		t.Fatalf("context for %s/%s = %q, want %q", task.ID, task.AgentID, resolved.Text, want)
	}
}

func channelSnapshots(t *testing.T, repository *contextstate.Repository, tasks []domain.TaskSpec) []contextstate.Snapshot {
	t.Helper()
	result := make([]contextstate.Snapshot, 0, len(tasks))
	for _, task := range tasks {
		snapshot, err := repository.Inspect(task.WorkflowID, task.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, snapshot)
	}
	return result
}
