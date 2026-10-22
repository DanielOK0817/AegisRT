package benchmark

import (
	"context"
	"testing"

	"aegisrt/internal/agentprotocol"
)

func TestFaultArtifactRecorderRejectsSemanticConflictAtomically(t *testing.T) {
	recorder := newFaultArtifactRecorder()
	initial := agentprotocol.Artifact{Name: "result", Content: "same", Visibility: "workflow"}
	if err := recorder.PublishAgentOutput(context.Background(), "task-1", 1, []agentprotocol.Artifact{initial}); err != nil {
		t.Fatal(err)
	}
	conflict := initial
	conflict.Visibility = "agent"
	batch := []agentprotocol.Artifact{
		{Name: "new-result", Content: "new", Visibility: "workflow"},
		conflict,
	}
	if err := recorder.PublishAgentOutput(context.Background(), "task-1", 1, batch); err == nil {
		t.Fatal("semantic artifact conflict was accepted")
	}
	records, duplicates := recorder.snapshot()
	if len(records) != 1 || records[0].ArtifactKey != "result" || records[0].ObservedCount != 1 || duplicates != 0 {
		t.Fatalf("conflicting batch partially mutated recorder: %#v duplicates=%d", records, duplicates)
	}
}

func TestStopFaultServiceReturnsWhenJoinDeadlineExpires(t *testing.T) {
	done := make(chan error)
	canceled := false
	err := stopFaultServiceWithin(func() { canceled = true }, done, 0)
	if err == nil || !canceled {
		t.Fatalf("stop result = %v canceled=%v", err, canceled)
	}
}
