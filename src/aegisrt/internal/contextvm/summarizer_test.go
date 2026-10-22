package contextvm

import (
	"context"
	"errors"
	"testing"
)

func TestSummarizerAdapterValidatesProviderBoundary(t *testing.T) {
	page, err := NewPage(PageSpec{Content: "one two three", Scope: ScopeSystem}, WhitespaceCounter{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	adapter, err := NewSummarizerAdapter("provider/model/prompt-v1", func(_ context.Context, request SummaryRequest) (string, error) {
		calls++
		return "short", nil
	}, SummarizerLimits{MaxPages: 1, MaxInputBytes: len(page.Content)})
	if err != nil {
		t.Fatal(err)
	}
	request := SummaryRequest{Pages: []Page{page}, TokenBudget: 1}
	if got, callErr := adapter.Summarize(context.Background(), request); callErr != nil || got != "short" || calls != 1 {
		t.Fatalf("Summarize() = (%q, %v), calls=%d", got, callErr, calls)
	}
	request.Pages = append(request.Pages, page)
	if _, callErr := adapter.Summarize(context.Background(), request); callErr == nil || calls != 1 {
		t.Fatalf("oversized request reached provider: %v, calls=%d", callErr, calls)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, callErr := adapter.Summarize(canceled, SummaryRequest{Pages: []Page{page}, TokenBudget: 1}); !errors.Is(callErr, context.Canceled) || calls != 1 {
		t.Fatalf("canceled request = %v, calls=%d", callErr, calls)
	}
}

func TestSummarizerAdapterRejectsInvalidConfiguration(t *testing.T) {
	valid := func(context.Context, SummaryRequest) (string, error) { return "summary", nil }
	if _, err := NewSummarizerAdapter("", valid, SummarizerLimits{MaxPages: 1, MaxInputBytes: 1}); err == nil {
		t.Fatal("empty identity was accepted")
	}
	if _, err := NewSummarizerAdapter("valid", nil, SummarizerLimits{MaxPages: 1, MaxInputBytes: 1}); err == nil {
		t.Fatal("nil provider was accepted")
	}
	if _, err := NewSummarizerAdapter("valid", valid, SummarizerLimits{}); err == nil {
		t.Fatal("zero limits were accepted")
	}
}

func TestExtractiveSummarizerFitsInjectedCounter(t *testing.T) {
	page, err := NewPage(PageSpec{Content: "alpha beta gamma delta", Scope: ScopeSystem}, WhitespaceCounter{})
	if err != nil {
		t.Fatal(err)
	}
	summarizer, err := NewExtractiveSummarizer(WhitespaceCounter{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := summarizer.Summarize(context.Background(), SummaryRequest{Pages: []Page{page}, TokenBudget: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha beta" || summarizer.Name() != "deterministic-extractive-v1" {
		t.Fatalf("summary = %q", got)
	}
}

func TestExtractiveSummarizerHandlesUnicodeBoundary(t *testing.T) {
	counter := UnicodeSegmentCounter{}
	page, err := NewPage(PageSpec{Content: "上下文 alpha", Scope: ScopeSystem}, counter)
	if err != nil {
		t.Fatal(err)
	}
	summarizer, _ := NewExtractiveSummarizer(counter)
	got, err := summarizer.Summarize(context.Background(), SummaryRequest{Pages: []Page{page}, TokenBudget: 2})
	if err != nil || got != "上下" {
		t.Fatalf("summary = (%q, %v)", got, err)
	}
}
