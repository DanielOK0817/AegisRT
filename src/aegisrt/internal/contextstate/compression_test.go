package contextstate

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"testing"
	"time"

	"aegisrt/internal/contextvm"
)

type countedSummary struct {
	contextvm.Summarizer
	calls   int
	fail    bool
	content *string
}

func (*countedSummary) Name() string { return "local-test-prefix-v1" }
func (s *countedSummary) Summarize(ctx context.Context, request contextvm.SummaryRequest) (string, error) {
	s.calls++
	if s.fail {
		return "", errors.New("compression failed")
	}
	if s.content != nil {
		return *s.content, nil
	}
	return s.Summarizer.Summarize(ctx, request)
}

func TestPersistentCompressionRetainsSourcesAndReusesAfterRestart(t *testing.T) {
	config := testConfig()
	prefix, _ := contextvm.NewExtractiveSummarizer(config.Counter)
	summary := &countedSummary{Summarizer: prefix}
	config.Summarizer = summary
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(config, store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	head, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	added, err := repository.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1", ExpectedManifestID: head.ID,
		IdempotencyKey: "cold-input", Content: "alpha beta gamma delta", Visibility: VisibilityAgent,
		Cold: true, TaskTokenBudget: 32, PrincipalPinnedBudget: 32,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := repository.Resolve(context.Background(), testTask(), testAgent(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if summary.calls != 1 || resolved.Metrics.SummaryPages != 1 {
		t.Fatalf("compression missing: %+v calls=%d", resolved, summary.calls)
	}
	page, err := repository.store.Lookup(resolved.Refs[0].PageID)
	if err != nil || len(page.SourceRefs) != 1 || page.SourceRefs[0].PageID != added.PageID {
		t.Fatalf("source lost: %+v error=%v", page, err)
	}
	restored, err := NewPersistent(config, store, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	summary.fail = true
	before := restored.Stats().Generation
	replayed, err := restored.Resolve(context.Background(), testTask(), testAgent(), 2)
	if err != nil || replayed.Text != resolved.Text || summary.calls != 1 || restored.Stats().Generation != before {
		t.Fatalf("restart recomputed cached summary: %+v error=%v calls=%d", replayed, err, summary.calls)
	}
	other := testTask()
	other.ID = "unrelated"
	assertTaskInput(t, restored, other, "")
}

func TestPersistentCompressionFailureNeverPublishesCandidate(t *testing.T) {
	for _, scenario := range []string{"summary", "capacity", "save"} {
		t.Run(scenario, func(t *testing.T) {
			repository, store, summary := compressionRepository(t)
			before, payload := repository.Stats(), append([]byte(nil), store.payload...)
			switch scenario {
			case "summary":
				summary.fail = true
			case "capacity":
				repository.config.Limits.MaxCheckpointBytes = len(payload)
			case "save":
				store.saveErr = errors.New("checkpoint write failed")
			}
			resolved, err := repository.Resolve(context.Background(), testTask(), testAgent(), 2)
			if err == nil || resolved.Text != "" {
				t.Fatalf("failed commit exposed context: %+v error=%v", resolved, err)
			}
			after := repository.Stats()
			if after.Generation != before.Generation || after.SummaryCount != 0 || !reflect.DeepEqual(store.payload, payload) {
				t.Fatalf("failed candidate changed durable state: before=%+v after=%+v", before, after)
			}
			if scenario != "save" && !repository.Healthy() {
				t.Fatal("ordinary compression rejection poisoned repository")
			}
		})
	}
}

func TestPersistentCompressionRejectsMetadataAndSourceTampering(t *testing.T) {
	repository, store, _ := compressionRepository(t)
	if _, err := repository.Resolve(context.Background(), testTask(), testAgent(), 2); err != nil {
		t.Fatal(err)
	}
	baseline := append([]byte(nil), store.payload...)
	for _, scenario := range []string{"counter", "summarizer", "missing-identity", "source", "budget", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			var stored checkpoint
			if err := json.Unmarshal(baseline, &stored); err != nil {
				t.Fatal(err)
			}
			want := ErrInvalidCheckpoint
			switch scenario {
			case "counter":
				stored.CounterIdentity = "other-v1"
			case "summarizer":
				stored.SummarizerIdentity = "other-v1"
			case "missing-identity":
				stored.CounterIdentity = ""
				want = ErrMigrationRequired
			case "source":
				stored.Summaries[0].SourceID = stored.Summaries[0].SummaryID
			case "budget":
				stored.Summaries[0].TokenBudget = 1
			case "duplicate":
				stored.Summaries = append(stored.Summaries, stored.Summaries[0])
			}
			var err error
			store.payload, err = json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewPersistent(repository.config, store, false, time.Now()); !errors.Is(err, want) {
				t.Fatalf("restore error=%v want=%v", err, want)
			}
		})
	}
}

func TestPersistentCompressionCacheSeparatesBudgets(t *testing.T) {
	repository, _, summary := compressionRepository(t)
	for _, budget := range []int{1, 2, 1, 2} {
		resolved, err := repository.Resolve(context.Background(), testTask(), testAgent(), budget)
		if err != nil || resolved.Metrics.PhysicalTokens > budget {
			t.Fatalf("budget=%d resolution=%+v error=%v", budget, resolved, err)
		}
	}
	if summary.calls != 2 || repository.Stats().SummaryCount != 2 {
		t.Fatalf("cache did not separate budgets: calls=%d stats=%+v", summary.calls, repository.Stats())
	}
}

func TestPersistentCompressionSuccessfulReadsPreserveAccessCounters(t *testing.T) {
	repository, _, _ := compressionRepository(t)
	for _, budget := range []int{2, 2, 32, 32} {
		before := repository.Stats()
		_, err := repository.Resolve(context.Background(), testTask(), testAgent(), budget)
		after := repository.Stats()
		if err != nil || after.Store.GetCalls <= before.Store.GetCalls {
			t.Fatalf("successful read lost counters: before=%+v after=%+v error=%v", before, after, err)
		}
		if before.SummaryCount > 0 && after.Generation != before.Generation {
			t.Fatal("read-only access changed checkpoint generation")
		}
	}
}

func TestPersistentCompressionRejectsNonTextSummaryBeforeCommit(t *testing.T) {
	for _, content := range []string{string([]byte{0xff}), "a\x00b"} {
		repository, store, summary := compressionRepository(t)
		summary.content = &content
		before, payload := repository.Stats(), append([]byte(nil), store.payload...)
		if resolved, err := repository.Resolve(context.Background(), testTask(), testAgent(), 2); err == nil || resolved.Text != "" {
			t.Fatalf("invalid summary committed: %+v error=%v", resolved, err)
		}
		if repository.Stats() != before || !reflect.DeepEqual(store.payload, payload) {
			t.Fatal("invalid summary mutated repository")
		}
	}
}

type acknowledgedSummaryStore struct {
	*scriptedStore
	loseAcknowledgment bool
}

func (s *acknowledgedSummaryStore) Save(payload []byte) error {
	if err := s.scriptedStore.Save(payload); err != nil {
		return err
	}
	if s.loseAcknowledgment {
		return uncertainError{cause: errors.New("commit acknowledgment lost")}
	}
	return nil
}

func TestPersistentCompressionRecoversCommitWithoutAcknowledgment(t *testing.T) {
	repository, store, summary := compressionRepository(t)
	repository.checkpoint = &acknowledgedSummaryStore{scriptedStore: store, loseAcknowledgment: true}
	if resolved, err := repository.Resolve(context.Background(), testTask(), testAgent(), 2); !errors.Is(err, ErrUnavailable) || resolved.Text != "" {
		t.Fatalf("ambiguous commit result=%+v error=%v", resolved, err)
	}
	restored, err := NewPersistent(repository.config, store, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	summary.fail = true
	resolved, err := restored.Resolve(context.Background(), testTask(), testAgent(), 2)
	if err != nil || resolved.Text == "" || summary.calls != 1 {
		t.Fatalf("recovery lost committed summary: %+v error=%v calls=%d", resolved, err, summary.calls)
	}
}

func compressionRepository(t *testing.T) (*Repository, *scriptedStore, *countedSummary) {
	t.Helper()
	config := testConfig()
	prefix, _ := contextvm.NewExtractiveSummarizer(config.Counter)
	summary := &countedSummary{Summarizer: prefix}
	config.Summarizer = summary
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(config, store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	head, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1", ExpectedManifestID: head.ID,
		IdempotencyKey: "cold-input", Content: "alpha beta gamma delta", Visibility: VisibilityAgent,
		Cold: true, TaskTokenBudget: 32, PrincipalPinnedBudget: 32,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return repository, store, summary
}
