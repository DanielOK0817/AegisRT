package contextdispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
)

func TestResolverReturnsExplicitMissingManifestError(t *testing.T) {
	resolver, _, _ := newTestResolver(t, nil)
	result, err := resolver.Resolve(context.Background(), validTask("workflow-1", "agent-1"), validAgent("agent-1"), 10)
	if !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("Resolve error = %v, want ErrManifestNotFound", err)
	}
	if result.Text != "" || len(result.Refs) != 0 {
		t.Fatalf("missing manifest leaked a result: %#v", result)
	}
}

func TestResolverStrictlyValidatesTaskAgentAndWorkflow(t *testing.T) {
	resolver, registry, _ := newTestResolver(t, nil)
	if err := registry.Set(mustManifest(t, "workflow-1", "agent-1", nil, nil)); err != nil {
		t.Fatal(err)
	}

	task := validTask("workflow-1", "agent-2")
	_, err := resolver.Resolve(context.Background(), task, validAgent("agent-1"), 10)
	if !errors.Is(err, ErrTaskAgentMismatch) {
		t.Fatalf("agent mismatch error = %v, want ErrTaskAgentMismatch", err)
	}

	_, err = resolver.Resolve(context.Background(), validTask("workflow-2", "agent-1"), validAgent("agent-1"), 10)
	if !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("cross-workflow error = %v, want ErrManifestNotFound", err)
	}
}

func TestResolverRejectsInvalidRequests(t *testing.T) {
	resolver, _, _ := newTestResolver(t, nil)
	tests := []struct {
		name   string
		ctx    context.Context
		task   domain.TaskSpec
		agent  domain.AgentSpec
		budget int
		want   error
	}{
		{name: "nil context", task: validTask("workflow-1", "agent-1"), agent: validAgent("agent-1"), budget: 1, want: ErrInvalidResolutionRequest},
		{name: "invalid task", ctx: context.Background(), agent: validAgent("agent-1"), budget: 1, want: ErrInvalidResolutionRequest},
		{name: "invalid agent", ctx: context.Background(), task: validTask("workflow-1", "agent-1"), budget: 1, want: ErrInvalidResolutionRequest},
		{name: "negative budget", ctx: context.Background(), task: validTask("workflow-1", "agent-1"), agent: validAgent("agent-1"), budget: -1, want: contextvm.ErrInvalidBudget},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolver.Resolve(test.ctx, test.task, test.agent, test.budget)
			if !errors.Is(err, test.want) {
				t.Fatalf("Resolve error = %v, want %v", err, test.want)
			}
		})
	}

	if _, err := NewResolver(nil, nil); !errors.Is(err, ErrInvalidResolver) {
		t.Fatalf("NewResolver error = %v, want ErrInvalidResolver", err)
	}
	var unavailable *Resolver
	if _, err := unavailable.Resolve(context.Background(), validTask("workflow-1", "agent-1"), validAgent("agent-1"), 1); !errors.Is(err, ErrInvalidResolver) {
		t.Fatalf("nil Resolve error = %v, want ErrInvalidResolver", err)
	}
}

func TestResolverRejectsRegistryPrincipalMismatch(t *testing.T) {
	resolver, registry, _ := newTestResolver(t, nil)
	other := mustManifest(t, "workflow-2", "agent-1", nil, nil)
	registry.mu.Lock()
	registry.manifests[manifestKey{workflowID: "workflow-1", agentID: "agent-1"}] = other
	registry.mu.Unlock()

	_, err := resolver.Resolve(context.Background(), validTask("workflow-1", "agent-1"), validAgent("agent-1"), 1)
	if !errors.Is(err, ErrManifestPrincipal) {
		t.Fatalf("Resolve error = %v, want ErrManifestPrincipal", err)
	}
}

func TestResolverPreventsCrossAgentACLLeak(t *testing.T) {
	resolver, registry, store := newTestResolver(t, nil)
	private := mustPage(t, contextvm.PageSpec{
		Content: "agent one secret", Scope: contextvm.ScopeAgent,
		WorkflowID: "workflow-1", AgentID: "agent-1",
	})
	privateRef := mustPut(t, store, private)
	malicious := mustManifest(t, "workflow-1", "agent-2", nil, []contextvm.ContextRef{privateRef})
	if err := registry.Set(malicious); err != nil {
		t.Fatal(err)
	}

	result, err := resolver.Resolve(context.Background(), validTask("workflow-1", "agent-2"), validAgent("agent-2"), 10)
	if !errors.Is(err, contextvm.ErrAccessDenied) {
		t.Fatalf("Resolve error = %v, want ErrAccessDenied", err)
	}
	if result.Text != "" || len(result.Refs) != 0 {
		t.Fatalf("ACL failure leaked content: %#v", result)
	}
}

func TestResolverPassesBudgetAndSummaryThroughAssembler(t *testing.T) {
	summarizer := &recordingSummarizer{content: "short summary"}
	resolver, registry, store := newTestResolver(t, summarizer)
	systemRef := mustPut(t, store, mustPage(t, contextvm.PageSpec{Content: "system rule", Scope: contextvm.ScopeSystem}))
	systemRef.Pin = true
	coldPage := mustPage(t, contextvm.PageSpec{
		Content: "one two three four five six seven eight",
		Scope:   contextvm.ScopeWorkflow, WorkflowID: "workflow-1",
	})
	coldRef := mustPut(t, store, coldPage)
	coldRef.Cold = true
	manifest := mustManifest(t, "workflow-1", "agent-1", []contextvm.ContextRef{systemRef, coldRef}, nil)
	if err := registry.Set(manifest); err != nil {
		t.Fatal(err)
	}

	result, err := resolver.Resolve(context.Background(), validTask("workflow-1", "agent-1"), validAgent("agent-1"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "system rule\n\nshort summary" || result.ManifestID != manifest.ID {
		t.Fatalf("unexpected resolution: %#v", result)
	}
	if len(result.Refs) != 2 || result.Metrics.SummaryPages != 1 ||
		result.Metrics.SummarizedSourceTokens != 8 || result.Metrics.SummaryTokens != 2 {
		t.Fatalf("summary metrics were not passed through: %#v", result.Metrics)
	}
	request := summarizer.LastRequest()
	if request.TokenBudget != 3 || len(request.Pages) != 1 || request.Pages[0].ID != coldPage.ID {
		t.Fatalf("summary request = %#v", request)
	}

	_, err = resolver.Resolve(context.Background(), validTask("workflow-1", "agent-1"), validAgent("agent-1"), 1)
	if !errors.Is(err, contextvm.ErrPinnedBudget) {
		t.Fatalf("budget error = %v, want ErrPinnedBudget", err)
	}
}

func TestResolverConcurrentReads(t *testing.T) {
	resolver, registry, store := newTestResolver(t, nil)
	shared := mustPut(t, store, mustPage(t, contextvm.PageSpec{
		Content: "shared context", Scope: contextvm.ScopeWorkflow, WorkflowID: "workflow-1",
	}))
	private := mustPut(t, store, mustPage(t, contextvm.PageSpec{
		Content: "private context", Scope: contextvm.ScopeAgent,
		WorkflowID: "workflow-1", AgentID: "agent-1",
	}))
	if err := registry.Set(mustManifest(t, "workflow-1", "agent-1", []contextvm.ContextRef{shared}, []contextvm.ContextRef{private})); err != nil {
		t.Fatal(err)
	}

	const workers = 64
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := resolver.Resolve(context.Background(), validTask("workflow-1", "agent-1"), validAgent("agent-1"), 10)
			if err != nil {
				errs <- err
				return
			}
			if result.Text != "shared context\n\nprivate context" {
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

func newTestResolver(t *testing.T, summarizer contextvm.Summarizer) (*Resolver, *Registry, *contextvm.Store) {
	t.Helper()
	store := contextvm.NewStore()
	assembler, err := contextvm.NewAssembler(store, contextvm.WhitespaceCounter{}, summarizer)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	resolver, err := NewResolver(registry, assembler)
	if err != nil {
		t.Fatal(err)
	}
	return resolver, registry, store
}

func mustPage(t *testing.T, spec contextvm.PageSpec) contextvm.Page {
	t.Helper()
	page, err := contextvm.NewPage(spec, contextvm.WhitespaceCounter{})
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func mustPut(t *testing.T, store *contextvm.Store, page contextvm.Page) contextvm.ContextRef {
	t.Helper()
	ref, _, err := store.Put(page)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func validAgent(id string) domain.AgentSpec {
	return domain.AgentSpec{
		ID: id, Name: "Context Agent", Runtime: "process", Entrypoint: "agent-runner",
		MaxConcurrency: 1, ExecutionTimeout: time.Minute,
	}
}

func validTask(workflowID, agentID string) domain.TaskSpec {
	return domain.TaskSpec{
		ID: "task-1", WorkflowID: workflowID, AgentID: agentID,
		Goal: "resolve task context", MaxAttempts: 1, Timeout: time.Minute,
		IdempotencyKey: "task-key-1",
	}
}

type recordingSummarizer struct {
	mu      sync.Mutex
	content string
	request contextvm.SummaryRequest
}

func (s *recordingSummarizer) Summarize(_ context.Context, request contextvm.SummaryRequest) (string, error) {
	s.mu.Lock()
	s.request = request
	s.mu.Unlock()
	return s.content, nil
}

func (s *recordingSummarizer) LastRequest() contextvm.SummaryRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.request
}
