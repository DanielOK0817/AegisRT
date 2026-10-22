package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/worker"
)

func TestCoordinatorPublishesArtifactsBeforeCompletingTask(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	agent := processAgent("artifact")
	publisher := &spyOutputPublisher{inspect: func(taskID string) domain.TaskStatus {
		snapshot, _ := runtime.Task(taskID)
		return snapshot.Status
	}}
	coordinator, err := NewWithOptions(
		runtime, worker.NewManager(executor.MinimalHostEnvironment(), nil), "worker-1",
		[]domain.AgentSpec{agent}, Options{OutputPublisher: publisher},
	)
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("publish-output", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 1 || publisher.taskID != task.ID || len(publisher.artifacts) != 1 ||
		publisher.artifacts[0].Content != "coordinator durable result" {
		t.Fatalf("unexpected publication: %+v", publisher)
	}
	if publisher.statusAtPublish != domain.TaskStatusRunning {
		t.Fatalf("task status during publication = %s, want RUNNING", publisher.statusAtPublish)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusSucceeded)
}

func TestCoordinatorRetainsCompletionWhenArtifactPublicationFails(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	agent := processAgent("artifact")
	publishErr := errors.New("ContextChannel unavailable")
	publisher := &spyOutputPublisher{resultErr: publishErr}
	coordinator, err := NewWithOptions(
		runtime, worker.NewManager(executor.MinimalHostEnvironment(), nil), "worker-1",
		[]domain.AgentSpec{agent}, Options{OutputPublisher: publisher},
	)
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("publish-failure", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Tick(context.Background(), time.Now()); !errors.Is(err, publishErr) {
		t.Fatalf("Tick error = %v, want publication error", err)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusRunning)
	if runtime.Stats().Allocated.IsZero() {
		t.Fatal("publication failure discarded recovery reservation")
	}
}

func TestCoordinatorRejectsArtifactsWithoutPublisher(t *testing.T) {
	coordinator, runtime := newTestCoordinator(t, processAgent("artifact"))
	task := coordinatorTask("missing-publisher", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Tick(context.Background(), time.Now()); !errors.Is(err, ErrOutputPublisherRequired) {
		t.Fatalf("Tick error = %v, want ErrOutputPublisherRequired", err)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusFailed)
}

func TestCoordinatorPublishesEmptyCompletionMarker(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	publisher := &spyOutputPublisher{}
	coordinator, err := NewWithOptions(
		runtime, worker.NewManager(executor.MinimalHostEnvironment(), nil), "worker-1",
		[]domain.AgentSpec{processAgent("success")}, Options{OutputPublisher: publisher},
	)
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("empty-output", nil)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 1 || publisher.taskID != task.ID || len(publisher.artifacts) != 0 {
		t.Fatalf("missing empty completion publication: %+v", publisher)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusSucceeded)
}

type spyOutputPublisher struct {
	inspect         func(string) domain.TaskStatus
	resultErr       error
	calls           int
	taskID          string
	artifacts       []agentprotocol.Artifact
	statusAtPublish domain.TaskStatus
}

func (p *spyOutputPublisher) PublishAgentOutput(
	_ context.Context,
	taskID string,
	attempt int,
	artifacts []agentprotocol.Artifact,
) error {
	p.calls++
	p.taskID = taskID
	p.artifacts = append([]agentprotocol.Artifact(nil), artifacts...)
	if p.inspect != nil {
		p.statusAtPublish = p.inspect(taskID)
	}
	return p.resultErr
}
