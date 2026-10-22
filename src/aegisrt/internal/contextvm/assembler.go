package contextvm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var errPageDoesNotFit = errors.New("page does not fit token budget")

type SummaryRequest struct {
	Pages       []Page `json:"pages"`
	TokenBudget int    `json:"token_budget"`
}

type Summarizer interface {
	Summarize(context.Context, SummaryRequest) (string, error)
}

type AssembleRequest struct {
	Manifest    Manifest      `json:"manifest"`
	Access      AccessRequest `json:"access"`
	TokenBudget int           `json:"token_budget"`
}

type AssemblyMetrics struct {
	LogicalTokens          int `json:"logical_tokens"`
	PhysicalTokens         int `json:"physical_tokens"`
	ReusedTokens           int `json:"reused_tokens"`
	DroppedTokens          int `json:"dropped_tokens"`
	SelectedPages          int `json:"selected_pages"`
	DroppedPages           int `json:"dropped_pages"`
	SummaryPages           int `json:"summary_pages"`
	SummarizedSourceTokens int `json:"summarized_source_tokens"`
	SummaryTokens          int `json:"summary_tokens"`
}

type Assembly struct {
	Text    string          `json:"text"`
	Refs    []ContextRef    `json:"refs"`
	Pages   []Page          `json:"pages"`
	Metrics AssemblyMetrics `json:"metrics"`
}

type Assembler struct {
	store      *Store
	counter    Counter
	summarizer Summarizer
}

func NewAssembler(store *Store, counter Counter, summarizer Summarizer) (*Assembler, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if counter == nil {
		return nil, fmt.Errorf("counter is required")
	}
	return &Assembler{store: store, counter: counter, summarizer: summarizer}, nil
}

type assemblyCandidate struct {
	page   Page
	ref    ContextRef
	tokens int
	order  int
}

func (a *Assembler) Assemble(ctx context.Context, request AssembleRequest) (Assembly, error) {
	if ctx == nil {
		return Assembly{}, fmt.Errorf("context is required")
	}
	if request.TokenBudget < 0 {
		return Assembly{}, fmt.Errorf("%w: %d", ErrInvalidBudget, request.TokenBudget)
	}
	if err := request.Access.Validate(); err != nil {
		return Assembly{}, err
	}
	if request.Access.WorkflowID != request.Manifest.WorkflowID || request.Access.AgentID != request.Manifest.AgentID {
		return Assembly{}, fmt.Errorf("%w: access principal does not own manifest", ErrAccessDenied)
	}
	if err := request.Manifest.Validate(a.store); err != nil {
		return Assembly{}, err
	}

	candidates, metrics, err := a.loadCandidates(ctx, request)
	if err != nil {
		return Assembly{}, err
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].ref.Pin != candidates[j].ref.Pin {
			return candidates[i].ref.Pin
		}
		iSystem := candidates[i].page.Scope == ScopeSystem
		jSystem := candidates[j].page.Scope == ScopeSystem
		if iSystem != jSystem {
			return iSystem
		}
		return candidates[i].order < candidates[j].order
	})

	result := Assembly{Metrics: metrics}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return Assembly{}, err
		}
		if candidate.ref.Pin {
			if err := a.appendPage(&result, candidate.page, candidate.ref, request.TokenBudget); err != nil {
				if errors.Is(err, errPageDoesNotFit) {
					return Assembly{}, fmt.Errorf("%w: page %s", ErrPinnedBudget, candidate.page.ID)
				}
				return Assembly{}, err
			}
			continue
		}
		appendErr := a.appendPage(&result, candidate.page, candidate.ref, request.TokenBudget)
		if appendErr == nil {
			continue
		}
		if !errors.Is(appendErr, errPageDoesNotFit) {
			return Assembly{}, appendErr
		}
		if candidate.ref.Cold && a.summarizer != nil {
			remaining := request.TokenBudget - result.Metrics.PhysicalTokens
			if remaining > 0 {
				summary, summaryRef, err := a.summarize(ctx, candidate, remaining)
				if err != nil {
					return Assembly{}, err
				}
				if err := a.appendPage(&result, summary, summaryRef, request.TokenBudget); err != nil {
					if errors.Is(err, errPageDoesNotFit) {
						return Assembly{}, fmt.Errorf("%w: page %s", ErrSummaryBudget, candidate.page.ID)
					}
					return Assembly{}, err
				}
				result.Metrics.SummaryPages++
				result.Metrics.SummarizedSourceTokens += candidate.tokens
				result.Metrics.SummaryTokens += summary.TokenCount
				continue
			}
		}
		result.Metrics.DroppedPages++
		result.Metrics.DroppedTokens += candidate.tokens
	}
	return result, nil
}

func (a *Assembler) loadCandidates(ctx context.Context, request AssembleRequest) ([]assemblyCandidate, AssemblyMetrics, error) {
	refs := request.Manifest.Refs()
	unique := make([]assemblyCandidate, 0, len(refs))
	byID := make(map[PageID]int, len(refs))
	metrics := AssemblyMetrics{}
	for order, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, metrics, err
		}
		page, err := a.store.Get(ref, request.Access)
		if err != nil {
			return nil, metrics, err
		}
		tokens, err := a.counter.Count(page.Content)
		if err != nil {
			return nil, metrics, fmt.Errorf("count page %s tokens: %w", page.ID, err)
		}
		if tokens < 0 {
			return nil, metrics, fmt.Errorf("count page %s tokens: counter returned %d", page.ID, tokens)
		}
		metrics.LogicalTokens += tokens
		if index, exists := byID[page.ID]; exists {
			metrics.ReusedTokens += tokens
			unique[index].ref.Pin = unique[index].ref.Pin || ref.Pin
			unique[index].ref.Cold = unique[index].ref.Cold && ref.Cold
			continue
		}
		byID[page.ID] = len(unique)
		unique = append(unique, assemblyCandidate{page: page, ref: ref, tokens: tokens, order: order})
	}
	return unique, metrics, nil
}

func (a *Assembler) appendPage(result *Assembly, page Page, ref ContextRef, budget int) error {
	text := page.Content
	if result.Text != "" {
		text = result.Text + "\n\n" + page.Content
	}
	tokens, err := a.counter.Count(text)
	if err != nil {
		return fmt.Errorf("count assembled tokens: %w", err)
	}
	if tokens < 0 {
		return fmt.Errorf("count assembled tokens: counter returned %d", tokens)
	}
	if tokens > budget {
		return errPageDoesNotFit
	}
	result.Text = text
	result.Pages = append(result.Pages, clonePage(page))
	result.Refs = append(result.Refs, ref)
	result.Metrics.PhysicalTokens = tokens
	result.Metrics.SelectedPages++
	return nil
}

func (a *Assembler) summarize(ctx context.Context, candidate assemblyCandidate, budget int) (Page, ContextRef, error) {
	request := SummaryRequest{Pages: []Page{clonePage(candidate.page)}, TokenBudget: budget}
	content, err := a.summarizer.Summarize(ctx, request)
	if err != nil {
		return Page{}, ContextRef{}, fmt.Errorf("%w: %v", ErrSummaryFailed, err)
	}
	if strings.TrimSpace(content) == "" {
		return Page{}, ContextRef{}, fmt.Errorf("%w: empty summary", ErrSummaryFailed)
	}
	summary, err := NewPage(PageSpec{
		Content:    content,
		Scope:      candidate.page.Scope,
		WorkflowID: candidate.page.WorkflowID,
		AgentID:    candidate.page.AgentID,
		SourceRefs: []ContextRef{{PageID: candidate.page.ID}},
	}, a.counter)
	if err != nil {
		return Page{}, ContextRef{}, fmt.Errorf("%w: %v", ErrSummaryFailed, err)
	}
	if summary.TokenCount > budget {
		return Page{}, ContextRef{}, fmt.Errorf("%w: got %d, budget %d", ErrSummaryBudget, summary.TokenCount, budget)
	}
	ref, _, err := a.store.Put(summary)
	if err != nil {
		return Page{}, ContextRef{}, fmt.Errorf("%w: store summary: %v", ErrSummaryFailed, err)
	}
	return summary, ref, nil
}
