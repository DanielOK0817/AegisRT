package contextstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"aegisrt/internal/contextvm"
)

func TestAppendIdempotencyConflictAndContentDedupDoNotAddRevision(t *testing.T) {
	repository := newAppendRepository(t, DefaultLimits())
	initial := ensureAppendManifest(t, repository)
	first := appendContext(t, repository, AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: initial.ID, IdempotencyKey: "write-1",
		Content: "same context", Visibility: VisibilityAgent,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	})
	_, err := repository.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: first.ManifestID, IdempotencyKey: "write-1",
		Content: "different context", Visibility: VisibilityAgent,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	}, time.Now())
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
	deduplicated := appendContext(t, repository, AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: first.ManifestID, IdempotencyKey: "write-2",
		Content: "same context", Visibility: VisibilityAgent,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	})
	if deduplicated.ManifestChanged || deduplicated.ManifestID != first.ManifestID || deduplicated.Revision != first.Revision {
		t.Fatalf("exact page dedup created a revision: %#v", deduplicated)
	}
	stats := repository.Stats()
	if stats.Store.UniquePages != 1 || stats.ManifestCount != 2 {
		t.Fatalf("dedup stats = %#v", stats)
	}
}

func TestAppendRejectsStaleHeadBeforeStoreMutation(t *testing.T) {
	repository := newAppendRepository(t, DefaultLimits())
	initial := ensureAppendManifest(t, repository)
	first := appendContext(t, repository, AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: initial.ID, IdempotencyKey: "write-1",
		Content: "first page", Visibility: VisibilityWorkflow,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	})
	_, err := repository.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: initial.ID, IdempotencyKey: "write-stale",
		Content: "must not persist", Visibility: VisibilityWorkflow,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	}, time.Now())
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("stale append error = %v", err)
	}
	if snapshot, inspectErr := repository.Inspect("workflow-1", "agent-1"); inspectErr != nil ||
		snapshot.ManifestID != first.ManifestID || snapshot.PageCount != 1 {
		t.Fatalf("stale append mutated head: snapshot=%#v error=%v", snapshot, inspectErr)
	}
	if repository.Stats().Store.UniquePages != 1 {
		t.Fatal("stale append left an unreachable page")
	}
}

func TestAppendEnforcesContentAndTokenLimits(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxPageBytes = 16
	limits.MaxPageTokens = 2
	tests := []struct {
		name    string
		command AppendCommand
		want    error
	}{
		{name: "blank", command: AppendCommand{Content: "  ", Visibility: VisibilityAgent}, want: ErrInvalidContent},
		{name: "nul", command: AppendCommand{Content: "secret\x00tail", Visibility: VisibilityAgent}, want: ErrInvalidContent},
		{name: "pin and cold", command: AppendCommand{Content: "safe", Visibility: VisibilityAgent, Pin: true, Cold: true}, want: ErrInvalidContent},
		{name: "bytes", command: AppendCommand{Content: "0123456789abcdefg", Visibility: VisibilityAgent}, want: ErrPageTooLarge},
		{name: "tokens", command: AppendCommand{Content: "one two three", Visibility: VisibilityAgent}, want: ErrPageTokenLimit},
		{name: "scope", command: AppendCommand{Content: "safe", Visibility: Visibility("system")}, want: ErrScopeForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newAppendRepository(t, limits)
			manifest := ensureAppendManifest(t, repository)
			command := test.command
			command.TaskID = "task-1"
			command.WorkflowID = "workflow-1"
			command.AgentID = "agent-1"
			command.ExpectedManifestID = manifest.ID
			command.IdempotencyKey = "write-1"
			command.TaskTokenBudget = 16
			command.PrincipalPinnedBudget = 16
			if _, err := repository.Append(context.Background(), command, time.Now()); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if repository.Stats().Store.UniquePages != 0 {
				t.Fatal("rejected content mutated Store")
			}
		})
	}
}

func TestAppendRejectsInvalidTaskBindingBeforeDurableLedgerMutation(t *testing.T) {
	repository := newAppendRepository(t, DefaultLimits())
	manifest := ensureAppendManifest(t, repository)
	_, err := repository.Append(context.Background(), AppendCommand{
		WorkflowID: "workflow-1", AgentID: "agent-1", ExpectedManifestID: manifest.ID,
		IdempotencyKey: "write-1", Content: "safe", Visibility: VisibilityAgent,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	}, time.Now())
	if !errors.Is(err, ErrInvalidTaskBinding) {
		t.Fatalf("error = %v, want ErrInvalidTaskBinding", err)
	}
	if repository.Stats().LedgerEntries != 0 || repository.Stats().Store.UniquePages != 0 {
		t.Fatal("invalid task binding mutated durable Context state")
	}
}

func TestAppendEnforcesManifestAndPinnedQuotas(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxManifestPages = 1
	repository := newAppendRepository(t, limits)
	initial := ensureAppendManifest(t, repository)
	first := appendContext(t, repository, AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: initial.ID, IdempotencyKey: "write-1",
		Content: "one two", Visibility: VisibilityWorkflow,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	})
	_, err := repository.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: first.ManifestID, IdempotencyKey: "write-2",
		Content: "second", Visibility: VisibilityAgent,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	}, time.Now())
	if !errors.Is(err, ErrManifestPageLimit) {
		t.Fatalf("page quota error = %v", err)
	}

	other := newAppendRepository(t, DefaultLimits())
	manifest := ensureAppendManifest(t, other)
	_, err = other.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: manifest.ID, IdempotencyKey: "pinned-write",
		Content: "one two", Visibility: VisibilityWorkflow, Pin: true,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 1,
	}, time.Now())
	if !errors.Is(err, ErrPinnedTokenLimit) {
		t.Fatalf("pinned quota error = %v", err)
	}
}

func newAppendRepository(t *testing.T, limits Limits) *Repository {
	t.Helper()
	repository, err := New(Config{
		Fingerprint: "append-test-v1", RuntimeInstanceID: testRuntimeInstanceID,
		Counter: contextvm.WhitespaceCounter{}, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func ensureAppendManifest(t *testing.T, repository *Repository) contextvm.Manifest {
	t.Helper()
	manifest, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func appendContext(t *testing.T, repository *Repository, command AppendCommand) AppendResult {
	t.Helper()
	result, err := repository.Append(context.Background(), command, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return result
}
