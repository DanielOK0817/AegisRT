package contextstate

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/statefile"
)

func TestPersistentRepositoryRestoresContentAndManifestLineage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.state")
	config := testConfig()
	firstStore, err := statefile.New(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewPersistent(config, firstStore, true, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	initial, owned, err := first.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil || !owned {
		t.Fatalf("provision: manifest=%#v owned=%v error=%v", initial, owned, err)
	}
	result, err := first.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: initial.ID, IdempotencyKey: "append-1",
		Content: "durable shared context", Visibility: VisibilityWorkflow,
		Pin: true, TaskTokenBudget: 32, PrincipalPinnedBudget: 32,
	}, time.Unix(101, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !result.ManifestChanged || result.Revision != 2 || result.TokenCount != 3 {
		t.Fatalf("unexpected append result: %#v", result)
	}

	secondStore, err := statefile.New(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPersistent(config, secondStore, false, time.Unix(102, 0))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := second.Inspect("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ManifestID != result.ManifestID || snapshot.Revision != 2 ||
		snapshot.PageCount != 1 || snapshot.LogicalTokens != 3 || snapshot.PinnedTokens != 3 {
		t.Fatalf("unexpected restored snapshot: %#v", snapshot)
	}
	resolution, err := second.Resolve(context.Background(), testTask(), testAgent(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.ManifestID != result.ManifestID || resolution.Text != "durable shared context" {
		t.Fatalf("unexpected restored resolution: %#v", resolution)
	}
	replay, err := second.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: initial.ID, IdempotencyKey: "append-1",
		Content: "durable shared context", Visibility: VisibilityWorkflow,
		Pin: true, TaskTokenBudget: 32, PrincipalPinnedBudget: 32,
	}, time.Unix(103, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay || replay.ManifestChanged != result.ManifestChanged || replay.ManifestID != result.ManifestID {
		t.Fatalf("idempotency record was not restored: %#v", replay)
	}
}

func TestPersistentRepositoryRejectsMissingStateWhenMigrationIsNotAllowed(t *testing.T) {
	store, err := statefile.New(filepath.Join(t.TempDir(), "missing.context"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewPersistent(testConfig(), store, false, time.Now())
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("error = %v, want ErrMigrationRequired", err)
	}
}

func TestDefiniteSaveFailureDoesNotPublishAndPoisonsRepository(t *testing.T) {
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(testConfig(), store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("disk unavailable")
	_, _, err = repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if repository.Healthy() {
		t.Fatal("repository remained healthy after a durability failure")
	}
	if _, err := repository.Get("workflow-1", "agent-1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("uncommitted manifest became visible or poison was ignored: %v", err)
	}
}

func TestUncertainSaveFailurePublishesCandidateAndPoisonsRepository(t *testing.T) {
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(testConfig(), store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store.saveErr = uncertainError{cause: errors.New("directory sync failed")}
	manifest, owned, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if !errors.Is(err, ErrUnavailable) || !owned {
		t.Fatalf("manifest=%#v owned=%v error=%v", manifest, owned, err)
	}
	if repository.Healthy() {
		t.Fatal("repository remained healthy after an uncertain commit")
	}
	repository.mu.RLock()
	visible, getErr := repository.registry.Get("workflow-1", "agent-1")
	repository.mu.RUnlock()
	if getErr != nil || visible.ID != manifest.ID {
		t.Fatalf("uncertain candidate was rolled back: visible=%#v error=%v", visible, getErr)
	}
}

func TestConcurrentAppendUsesManifestCompareAndSwap(t *testing.T) {
	repository, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	initial, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	const writers = 32
	var wait sync.WaitGroup
	results := make(chan error, writers)
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, appendErr := repository.Append(context.Background(), AppendCommand{
				TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
				ExpectedManifestID: initial.ID, IdempotencyKey: "writer-" + time.Unix(int64(index), 0).Format("150405"),
				Content:    "writer content " + time.Unix(int64(index), 0).Format("150405"),
				Visibility: VisibilityAgent, TaskTokenBudget: 32, PrincipalPinnedBudget: 32,
			}, time.Now())
			results <- appendErr
		}(index)
	}
	wait.Wait()
	close(results)
	successes := 0
	preconditions := 0
	for appendErr := range results {
		switch {
		case appendErr == nil:
			successes++
		case errors.Is(appendErr, ErrPreconditionFailed):
			preconditions++
		default:
			t.Errorf("unexpected append error: %v", appendErr)
		}
	}
	if successes != 1 || preconditions != writers-1 {
		t.Fatalf("successes=%d preconditions=%d", successes, preconditions)
	}
}

func TestPersistentRepositoryRejectsUnversionedSummarizer(t *testing.T) {
	config := testConfig()
	config.Summarizer = staticSummarizer{}
	_, err := NewPersistent(config, &scriptedStore{loadErr: fs.ErrNotExist}, true, time.Now())
	if err == nil || !strings.Contains(err.Error(), "named, versioned") {
		t.Fatalf("error = %v, want explicit transactional summarizer rejection", err)
	}
}

func TestCheckpointCapacityRejectionDoesNotPoisonOrPublish(t *testing.T) {
	config := testConfig()
	config.Limits.MaxCheckpointBytes = 2500
	config.Limits.MaxPageBytes = 4096
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(config, store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	generation := repository.Stats().Generation
	_, err = repository.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: manifest.ID, IdempotencyKey: "large-write",
		Content: strings.Repeat("x", 3000), Visibility: VisibilityAgent,
		TaskTokenBudget: 4096, PrincipalPinnedBudget: 4096,
	}, time.Now())
	if !errors.Is(err, ErrCheckpointCapacity) {
		t.Fatalf("error = %v, want ErrCheckpointCapacity", err)
	}
	if !repository.Healthy() || repository.Stats().Generation != generation {
		t.Fatalf("capacity rejection poisoned or advanced Repository: %#v", repository.Stats())
	}
	snapshot, err := repository.Inspect("workflow-1", "agent-1")
	if err != nil || snapshot.PageCount != 0 || snapshot.ManifestID != manifest.ID {
		t.Fatalf("capacity rejection published candidate: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestDeleteIfIDOnlyReclaimsPristineRollbackManifest(t *testing.T) {
	repository, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: root.ID, IdempotencyKey: "retain-nonempty",
		Content: "must survive", Visibility: VisibilityAgent,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if deleted, err := repository.DeleteIfID("workflow-1", "agent-1", result.ManifestID); err != nil || deleted {
		t.Fatalf("DeleteIfID nonempty head = (%v, %v), want false", deleted, err)
	}
	snapshot, err := repository.Inspect("workflow-1", "agent-1")
	if err != nil || snapshot.ManifestID != result.ManifestID || snapshot.PageCount != 1 {
		t.Fatalf("nonempty head changed: snapshot=%+v error=%v", snapshot, err)
	}
}

type scriptedStore struct {
	mu      sync.Mutex
	payload []byte
	loadErr error
	saveErr error
}

func (s *scriptedStore) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return append([]byte(nil), s.payload...), nil
}

func (s *scriptedStore) Save(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.payload = append([]byte(nil), payload...)
	s.loadErr = nil
	return nil
}

type uncertainError struct{ cause error }

func (e uncertainError) Error() string       { return e.cause.Error() }
func (e uncertainError) Unwrap() error       { return e.cause }
func (uncertainError) CommitUncertain() bool { return true }

type staticSummarizer struct{}

func (staticSummarizer) Summarize(context.Context, contextvm.SummaryRequest) (string, error) {
	return "summary", nil
}

func testConfig() Config {
	return Config{
		Fingerprint: "test-context-v1", RuntimeInstanceID: testRuntimeInstanceID,
		Counter: contextvm.WhitespaceCounter{},
		Limits:  DefaultLimits(),
	}
}

const testRuntimeInstanceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testTask() domain.TaskSpec {
	return domain.TaskSpec{
		ID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1", Goal: "test context",
		Resources: domain.ResourceVector{TokenBudget: 32}, MaxAttempts: 1,
		Timeout: time.Second, IdempotencyKey: "task-key-1",
	}
}

func testAgent() domain.AgentSpec {
	return domain.AgentSpec{
		ID: "agent-1", Name: "Agent 1", Runtime: "process", Entrypoint: "agent-runner",
		Resources: domain.ResourceVector{TokenBudget: 32}, MaxConcurrency: 1, ExecutionTimeout: time.Second,
	}
}
