package contextvm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type fixedSummarizer struct {
	mu       sync.Mutex
	content  string
	err      error
	requests []SummaryRequest
}

func (s *fixedSummarizer) Summarize(_ context.Context, request SummaryRequest) (string, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	return s.content, s.err
}

func (s *fixedSummarizer) lastRequest() SummaryRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[len(s.requests)-1]
}

type runeCounter struct{}

func (runeCounter) Count(text string) (int, error) { return len([]rune(text)), nil }

func TestAssemblerPrioritizesPinnedPagesWithinBudget(t *testing.T) {
	store := NewStore()
	huge := mustPut(t, store, mustPage(t, PageSpec{Content: "large page has four", Scope: ScopeWorkflow, WorkflowID: "workflow-1"}))
	system := mustPut(t, store, mustPage(t, PageSpec{Content: "system rule", Scope: ScopeSystem}))
	tiny := mustPut(t, store, mustPage(t, PageSpec{Content: "tiny", Scope: ScopeWorkflow, WorkflowID: "workflow-1"}))
	current := mustPut(t, store, mustPage(t, PageSpec{Content: "current task", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1"}))
	system.Pin = true
	current.Pin = true
	manifest, err := NewManifest("workflow-1", "agent-1", []ContextRef{huge, system, tiny}, []ContextRef{current})
	if err != nil {
		t.Fatal(err)
	}
	assembler, err := NewAssembler(store, WhitespaceCounter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := assembler.Assemble(context.Background(), AssembleRequest{
		Manifest: manifest, Access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}, TokenBudget: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "system rule\n\ncurrent task\n\ntiny" {
		t.Fatalf("Text = %q", result.Text)
	}
	want := AssemblyMetrics{LogicalTokens: 9, PhysicalTokens: 5, DroppedTokens: 4, SelectedPages: 3, DroppedPages: 1}
	if result.Metrics != want {
		t.Fatalf("Metrics = %#v, want %#v", result.Metrics, want)
	}
	if _, err := assembler.Assemble(context.Background(), AssembleRequest{
		Manifest: manifest, Access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}, TokenBudget: 3,
	}); !errors.Is(err, ErrPinnedBudget) {
		t.Fatalf("pinned overflow error = %v, want ErrPinnedBudget", err)
	}
}

func TestAssemblerReportsExactReferenceReuse(t *testing.T) {
	store := NewStore()
	ref := mustPut(t, store, mustPage(t, PageSpec{Content: "shared facts", Scope: ScopeWorkflow, WorkflowID: "workflow-1"}))
	manifest, err := NewManifest("workflow-1", "agent-1", []ContextRef{ref, ref}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assembler, _ := NewAssembler(store, WhitespaceCounter{}, nil)
	result, err := assembler.Assemble(context.Background(), AssembleRequest{
		Manifest: manifest, Access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}, TokenBudget: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "shared facts" || len(result.Pages) != 1 {
		t.Fatalf("deduplicated assembly = %#v", result)
	}
	if result.Metrics.LogicalTokens != 4 || result.Metrics.PhysicalTokens != 2 || result.Metrics.ReusedTokens != 2 {
		t.Fatalf("reuse metrics = %#v", result.Metrics)
	}
}

func TestAssemblerSummarizesColdPageWithTraceability(t *testing.T) {
	store := NewStore()
	system := mustPut(t, store, mustPage(t, PageSpec{Content: "system rule", Scope: ScopeSystem}))
	system.Pin = true
	coldPage := mustPage(t, PageSpec{Content: "one two three four five six seven eight", Scope: ScopeWorkflow, WorkflowID: "workflow-1"})
	cold := mustPut(t, store, coldPage)
	cold.Cold = true
	manifest, _ := NewManifest("workflow-1", "agent-1", []ContextRef{system, cold}, nil)
	summarizer := &fixedSummarizer{content: "short summary"}
	assembler, _ := NewAssembler(store, WhitespaceCounter{}, summarizer)
	access := AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}
	result, err := assembler.Assemble(context.Background(), AssembleRequest{Manifest: manifest, Access: access, TokenBudget: 5})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "system rule\n\nshort summary" || len(result.Pages) != 2 {
		t.Fatalf("summary assembly = %#v", result)
	}
	summary := result.Pages[1]
	if len(summary.SourceRefs) != 1 || summary.SourceRefs[0].PageID != coldPage.ID {
		t.Fatalf("summary SourceRefs = %#v", summary.SourceRefs)
	}
	if _, err := store.Get(result.Refs[1], access); err != nil {
		t.Fatalf("summary page was not persisted: %v", err)
	}
	want := AssemblyMetrics{LogicalTokens: 10, PhysicalTokens: 4, SelectedPages: 2, SummaryPages: 1, SummarizedSourceTokens: 8, SummaryTokens: 2}
	if result.Metrics != want {
		t.Fatalf("Metrics = %#v, want %#v", result.Metrics, want)
	}
	request := summarizer.lastRequest()
	if request.TokenBudget != 3 || len(request.Pages) != 1 || request.Pages[0].ID != coldPage.ID {
		t.Fatalf("SummaryRequest = %#v", request)
	}
}

func TestAssemblerSummaryFailuresAreExplicit(t *testing.T) {
	store := NewStore()
	pinned := mustPut(t, store, mustPage(t, PageSpec{Content: "system rule", Scope: ScopeSystem}))
	pinned.Pin = true
	cold := mustPut(t, store, mustPage(t, PageSpec{Content: "one two three four five", Scope: ScopeWorkflow, WorkflowID: "workflow-1"}))
	cold.Cold = true
	manifest, _ := NewManifest("workflow-1", "agent-1", []ContextRef{pinned, cold}, nil)
	access := AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}
	tests := []struct {
		name       string
		summarizer Summarizer
		want       error
	}{
		{name: "summary too large", summarizer: &fixedSummarizer{content: "one two three four"}, want: ErrSummaryBudget},
		{name: "empty summary", summarizer: &fixedSummarizer{content: " "}, want: ErrSummaryFailed},
		{name: "provider failure", summarizer: &fixedSummarizer{err: errors.New("provider unavailable")}, want: ErrSummaryFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assembler, _ := NewAssembler(store, WhitespaceCounter{}, test.summarizer)
			_, err := assembler.Assemble(context.Background(), AssembleRequest{Manifest: manifest, Access: access, TokenBudget: 5})
			if !errors.Is(err, test.want) {
				t.Fatalf("Assemble() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestAssemblerWithoutSummarizerDropsColdPage(t *testing.T) {
	store := NewStore()
	ref := mustPut(t, store, mustPage(t, PageSpec{Content: "one two three", Scope: ScopeWorkflow, WorkflowID: "workflow-1"}))
	ref.Cold = true
	manifest, _ := NewManifest("workflow-1", "agent-1", []ContextRef{ref}, nil)
	assembler, _ := NewAssembler(store, WhitespaceCounter{}, nil)
	result, err := assembler.Assemble(context.Background(), AssembleRequest{
		Manifest: manifest, Access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}, TokenBudget: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "" || result.Metrics.DroppedPages != 1 || result.Metrics.DroppedTokens != 3 {
		t.Fatalf("drop result = %#v", result)
	}
}

func TestAssemblerEnforcesManifestPrincipal(t *testing.T) {
	store := NewStore()
	private := mustPut(t, store, mustPage(t, PageSpec{Content: "secret", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-2"}))
	manifest, _ := NewManifest("workflow-1", "agent-1", nil, []ContextRef{private})
	assembler, _ := NewAssembler(store, WhitespaceCounter{}, nil)
	_, err := assembler.Assemble(context.Background(), AssembleRequest{
		Manifest: manifest, Access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}, TokenBudget: 10,
	})
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("cross-agent manifest error = %v, want ErrAccessDenied", err)
	}
	validManifest, _ := NewManifest("workflow-1", "agent-1", nil, nil)
	_, err = assembler.Assemble(context.Background(), AssembleRequest{
		Manifest: validManifest, Access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-2"}, TokenBudget: 10,
	})
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("principal mismatch error = %v, want ErrAccessDenied", err)
	}
}

func TestAssemblerUsesInjectedCounterForExactBudget(t *testing.T) {
	store := NewStore()
	first, _ := NewPage(PageSpec{Content: "aa", Scope: ScopeSystem}, runeCounter{})
	second, _ := NewPage(PageSpec{Content: "b", Scope: ScopeWorkflow, WorkflowID: "workflow-1"}, runeCounter{})
	firstRef := mustPut(t, store, first)
	secondRef := mustPut(t, store, second)
	firstRef.Pin, secondRef.Pin = true, true
	manifest, _ := NewManifest("workflow-1", "agent-1", []ContextRef{firstRef, secondRef}, nil)
	assembler, _ := NewAssembler(store, runeCounter{}, nil)
	request := AssembleRequest{Manifest: manifest, Access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}, TokenBudget: 4}
	if _, err := assembler.Assemble(context.Background(), request); !errors.Is(err, ErrPinnedBudget) {
		t.Fatalf("separator-aware budget error = %v", err)
	}
	request.TokenBudget = 5
	result, err := assembler.Assemble(context.Background(), request)
	if err != nil || result.Metrics.PhysicalTokens != 5 {
		t.Fatalf("exact budget result = (%#v, %v)", result, err)
	}
}

func TestAssemblerConcurrentReads(t *testing.T) {
	store := NewStore()
	shared := mustPut(t, store, mustPage(t, PageSpec{Content: "shared context", Scope: ScopeWorkflow, WorkflowID: "workflow-1"}))
	private := mustPut(t, store, mustPage(t, PageSpec{Content: "private context", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1"}))
	manifest, _ := NewManifest("workflow-1", "agent-1", []ContextRef{shared}, []ContextRef{private})
	assembler, _ := NewAssembler(store, WhitespaceCounter{}, nil)
	request := AssembleRequest{Manifest: manifest, Access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}, TokenBudget: 10}
	const workers = 32
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := assembler.Assemble(context.Background(), request)
			if err != nil {
				errs <- err
			} else if result.Text != "shared context\n\nprivate context" {
				errs <- fmt.Errorf("unexpected text %q", result.Text)
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
