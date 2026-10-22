package runtimeapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

func TestContextControlReportsContentFreeDurableOutputSummary(t *testing.T) {
	repository, err := contextstate.New(contextstate.Config{
		Fingerprint: "output-summary-v1", RuntimeInstanceID: runtimeappTestInstanceID,
		Counter: contextvm.WhitespaceCounter{}, Limits: contextstate.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	task := runtimeTask("task-output", "agent-1")
	manifest, _, err := repository.EnsureEmptyWithOwnership(task.WorkflowID, task.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.PublishOutput(context.Background(), contextstate.OutputCommand{
		TaskID: task.ID, WorkflowID: task.WorkflowID, ProducerAgentID: task.AgentID,
		Targets: []contextstate.OutputTarget{{
			AgentID: task.AgentID, ExpectedManifestID: manifest.ID,
			TaskTokenBudget: 32, PrincipalPinnedBudget: 32,
		}},
		Artifacts: []contextstate.ArtifactCommand{{
			Name: "result", Content: "private payload", Visibility: contextstate.VisibilityWorkflow,
			Targets: []string{task.AgentID},
		}},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	underlying := &fakeControl{tasks: []taskgraph.TaskSnapshot{{Spec: task, Status: domain.TaskStatusSucceeded}}}
	control, err := NewContextControl(underlying, repository, []domain.AgentSpec{runtimeAgent("agent-1")})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := control.InspectOutputSummary(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary != (OutputSummary{RecordCount: 1, ArtifactCount: 1}) {
		t.Fatalf("summary = %+v", summary)
	}
	if _, err := control.InspectOutputSummary("missing"); !errors.Is(err, contextstate.ErrOutputNotFound) {
		t.Fatalf("missing output error = %v", err)
	}
}
