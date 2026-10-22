package main

import (
	"context"
	"testing"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/contextstate"
	"aegisrt/internal/domain"
)

func TestDaemonStartupFinishesSavedOutputWithoutStartingService(t *testing.T) {
	for _, alreadyPublished := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-publication", true: "after-publication"}[alreadyPublished], func(t *testing.T) {
			options := contextChannelDaemonOptions(t, t.TempDir())
			first, err := buildDaemon(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = first.close() })
			task := domain.TaskSpec{ID: "produce", WorkflowID: "channel-e2e", AgentID: "producer", Goal: "verify saved completion", Resources: domain.ResourceVector{TokenBudget: 128}, Priority: 1, MaxAttempts: 1, Timeout: 2 * time.Second, IdempotencyKey: "channel-produce"}
			manifest, _, err := first.contexts.EnsureEmptyWithOwnership(task.WorkflowID, task.AgentID)
			if err != nil {
				t.Fatal(err)
			}
			if err := first.kernel.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := first.kernel.Schedule(time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := first.kernel.MarkStarted(task.ID, 1, time.Now()); err != nil {
				t.Fatal(err)
			}
			artifacts := []agentprotocol.Artifact{{Name: "analysis", Content: "frozen output before restart", Visibility: "workflow"}}
			if err := first.kernel.PrepareCompletion(task.ID, 1, artifacts, time.Now()); err != nil {
				t.Fatal(err)
			}
			if alreadyPublished {
				_, err := first.contexts.PublishOutput(context.Background(), contextstate.OutputCommand{
					TaskID: task.ID, WorkflowID: task.WorkflowID, ProducerAgentID: task.AgentID, AttemptNumber: 1,
					Targets:   []contextstate.OutputTarget{{AgentID: task.AgentID, ExpectedManifestID: manifest.ID, TaskTokenBudget: task.Resources.TokenBudget, PrincipalPinnedBudget: task.Resources.TokenBudget}},
					Artifacts: []contextstate.ArtifactCommand{{Name: "analysis", Content: artifacts[0].Content, Visibility: contextstate.VisibilityWorkflow, Targets: []string{task.AgentID}}},
				}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := first.close(); err != nil {
				t.Fatal(err)
			}
			second, err := buildDaemon(options)
			if err != nil {
				t.Fatal(err)
			}
			defer second.close()
			snapshot, _ := second.kernel.Task(task.ID)
			if snapshot.Status != domain.TaskStatusSucceeded || second.kernel.AttemptNumber(task.ID) != 1 {
				t.Fatalf("startup recovery=%+v", snapshot)
			}
			output, err := second.contexts.Output(task.ID)
			if err != nil || output.AttemptNumber != 1 || len(output.Artifacts) != 1 {
				t.Fatalf("output=%+v err=%v", output, err)
			}
			if second.service.Stats().TicksStarted != 0 || second.contexts.Stats().OutputCount != 1 {
				t.Fatal("startup reran Agent or duplicated output")
			}
		})
	}
}
