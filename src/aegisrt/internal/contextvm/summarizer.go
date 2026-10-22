package contextvm

import (
	"context"
	"fmt"
	"strings"
)

type NamedSummarizer interface {
	Summarizer
	Name() string
}

type SummarizerFunc func(context.Context, SummaryRequest) (string, error)

type SummarizerLimits struct {
	MaxPages      int
	MaxInputBytes int
}

// SummarizerAdapter validates calls to an external summarization provider.
// The Assembler validates and stores the returned summary only after this call
// succeeds, so a failed or oversized response cannot mutate the Context store.
type SummarizerAdapter struct {
	name   string
	call   SummarizerFunc
	limits SummarizerLimits
}

func NewSummarizerAdapter(name string, call SummarizerFunc, limits SummarizerLimits) (*SummarizerAdapter, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("summarizer name is required")
	}
	if call == nil {
		return nil, fmt.Errorf("summarizer function is required")
	}
	if limits.MaxPages <= 0 || limits.MaxInputBytes <= 0 {
		return nil, fmt.Errorf("positive summarizer limits are required")
	}
	return &SummarizerAdapter{name: name, call: call, limits: limits}, nil
}

func (a *SummarizerAdapter) Name() string { return a.name }

func (a *SummarizerAdapter) Summarize(ctx context.Context, request SummaryRequest) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if request.TokenBudget <= 0 {
		return "", fmt.Errorf("positive summary token budget is required")
	}
	if len(request.Pages) == 0 || len(request.Pages) > a.limits.MaxPages {
		return "", fmt.Errorf("summary page count must be between 1 and %d", a.limits.MaxPages)
	}
	totalBytes := 0
	for _, page := range request.Pages {
		if page.ID == "" || strings.TrimSpace(page.Content) == "" || totalBytes > a.limits.MaxInputBytes-len(page.Content) {
			return "", fmt.Errorf("summary input is invalid or exceeds %d bytes", a.limits.MaxInputBytes)
		}
		totalBytes += len(page.Content)
	}
	result, err := a.call(ctx, request)
	if err != nil {
		return "", fmt.Errorf("summarizer %q: %w", a.name, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return result, nil
}

// ExtractiveSummarizer is a deterministic, provider-independent reference
// implementation for tests and controlled ablations. It keeps the longest
// source prefix that fits the injected counter; it does not claim semantic
// equivalence to an LLM summary.
type ExtractiveSummarizer struct {
	counter Counter
}

func NewExtractiveSummarizer(counter Counter) (*ExtractiveSummarizer, error) {
	if counter == nil {
		return nil, fmt.Errorf("counter is required")
	}
	return &ExtractiveSummarizer{counter: counter}, nil
}

func (*ExtractiveSummarizer) Name() string { return "deterministic-extractive-v1" }

func (s *ExtractiveSummarizer) Summarize(ctx context.Context, request SummaryRequest) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is required")
	}
	if request.TokenBudget <= 0 || len(request.Pages) == 0 {
		return "", fmt.Errorf("pages and positive token budget are required")
	}
	parts := make([]string, 0, len(request.Pages))
	for _, page := range request.Pages {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if content := strings.TrimSpace(page.Content); content != "" {
			parts = append(parts, content)
		}
	}
	source := strings.Join(parts, "\n\n")
	if source == "" {
		return "", fmt.Errorf("summary source is empty")
	}
	if tokens, err := s.counter.Count(source); err != nil {
		return "", err
	} else if tokens <= request.TokenBudget {
		return source, nil
	}
	// Only these local counters guarantee monotone prefix counts. Arbitrary
	// adapters retain exhaustive selection rather than assuming that property.
	switch s.counter.(type) {
	case WhitespaceCounter, UnicodeSegmentCounter:
		return localBudgetPrefix(ctx, source, request.TokenBudget, s.counter)
	}
	best := ""
	for index := range source {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if index == 0 || !isRuneBoundary(source[index]) {
			continue
		}
		candidate := strings.TrimSpace(source[:index])
		if candidate == "" {
			continue
		}
		tokens, err := s.counter.Count(candidate)
		if err != nil {
			return "", err
		}
		if tokens <= request.TokenBudget {
			best = candidate
		}
	}
	if best == "" {
		return "", fmt.Errorf("no source prefix fits summary budget")
	}
	return best, nil
}

func isRuneBoundary(value byte) bool { return value&0xc0 != 0x80 }

func localBudgetPrefix(ctx context.Context, source string, budget int, counter Counter) (string, error) {
	boundaries := make([]int, 0, len(source)+1)
	for index := range source {
		boundaries = append(boundaries, index)
	}
	boundaries = append(boundaries, len(source))
	low, high := 0, len(boundaries)
	best := ""
	for low < high {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		middle := low + (high-low)/2
		candidate := strings.TrimSpace(source[:boundaries[middle]])
		tokens, err := counter.Count(candidate)
		if err != nil {
			return "", err
		}
		if tokens <= budget {
			best, low = candidate, middle+1
		} else {
			high = middle
		}
	}
	if best == "" {
		return "", fmt.Errorf("no source prefix fits summary budget")
	}
	return best, nil
}
