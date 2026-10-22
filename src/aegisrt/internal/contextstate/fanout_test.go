package contextstate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFanoutCommitsAllTargetsAtomicallyAndReplays(t *testing.T) {
	repository, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-2")
	if err != nil {
		t.Fatal(err)
	}
	commands := []AppendCommand{
		fanoutCommand("agent-1", first.ID, "fanout-1"),
		fanoutCommand("agent-2", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fanout-2"),
	}
	if _, err := repository.Fanout(context.Background(), commands, time.Now()); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("Fanout error = %v, want ErrPreconditionFailed", err)
	}
	assertEmptyContextHead(t, repository, "agent-1", first.ID)
	assertEmptyContextHead(t, repository, "agent-2", second.ID)

	commands[1].ExpectedManifestID = second.ID
	results, err := repository.Fanout(context.Background(), commands, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].PageID != results[1].PageID || !results[0].ManifestChanged || !results[1].ManifestChanged {
		t.Fatalf("unexpected fanout results: %+v", results)
	}
	stats := repository.Stats().Store
	if stats.UniquePages != 1 || stats.UniqueBlobs != 1 || stats.ReusedPuts != 1 {
		t.Fatalf("fanout did not reuse one physical page: %+v", stats)
	}

	replayed, err := repository.Fanout(context.Background(), commands, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 || !replayed[0].IdempotentReplay || !replayed[1].IdempotentReplay {
		t.Fatalf("fanout replay was not idempotent: %+v", replayed)
	}
}

func fanoutCommand(agentID, manifestID, key string) AppendCommand {
	return AppendCommand{
		TaskID: "producer-task", WorkflowID: "workflow-1", AgentID: agentID,
		ExpectedManifestID: manifestID, IdempotencyKey: key,
		Content: "one shared artifact", Visibility: VisibilityWorkflow,
		TaskTokenBudget: 64, PrincipalPinnedBudget: 64,
	}
}

func assertEmptyContextHead(t *testing.T, repository *Repository, agentID, manifestID string) {
	t.Helper()
	snapshot, err := repository.Inspect("workflow-1", agentID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ManifestID != manifestID || snapshot.PageCount != 0 {
		t.Fatalf("head for %s mutated: %+v", agentID, snapshot)
	}
}
