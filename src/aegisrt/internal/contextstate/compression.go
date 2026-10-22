package contextstate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"aegisrt/internal/contextvm"
)

type summaryKey struct {
	source contextvm.PageID
	budget int
}

type summaryRecord struct {
	SourceID    contextvm.PageID `json:"source_id"`
	SummaryID   contextvm.PageID `json:"summary_id"`
	TokenBudget int              `json:"token_budget"`
}

// cachedSummarizer belongs exclusively to one candidate transaction. New
// records become durable only if assembly and the checkpoint commit succeed.
type cachedSummarizer struct {
	config  Config
	store   *contextvm.Store
	records map[summaryKey]summaryRecord
}

func (s *cachedSummarizer) Summarize(ctx context.Context, request contextvm.SummaryRequest) (string, error) {
	if len(request.Pages) != 1 || request.TokenBudget <= 0 {
		return "", contextvm.ErrSummaryFailed
	}
	source := request.Pages[0]
	key := summaryKey{source.ID, request.TokenBudget}
	if record, exists := s.records[key]; exists {
		page, err := s.store.Lookup(record.SummaryID)
		return page.Content, err
	}
	if len(s.records) >= s.config.Limits.MaxLedgerEntries {
		return "", ErrSummaryCapacity
	}
	content, err := s.config.Summarizer.Summarize(ctx, request)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !utf8.ValidString(content) || strings.ContainsRune(content, '\x00') {
		return "", ErrInvalidContent
	}
	page, err := contextvm.NewPage(contextvm.PageSpec{
		Content: content, Scope: source.Scope, WorkflowID: source.WorkflowID, AgentID: source.AgentID,
		SourceRefs: []contextvm.ContextRef{{PageID: source.ID}},
	}, s.config.Counter)
	if err != nil {
		return "", err
	}
	if len(content) > s.config.Limits.MaxPageBytes || page.TokenCount > s.config.Limits.MaxPageTokens || page.TokenCount > request.TokenBudget {
		return "", contextvm.ErrSummaryBudget
	}
	// Assembler creates and stores this exact page before it returns success.
	s.records[key] = summaryRecord{SourceID: source.ID, SummaryID: page.ID, TokenBudget: request.TokenBudget}
	return content, nil
}

func restoreSummaries(config Config, store *contextvm.Store, records []summaryRecord) (map[summaryKey]summaryRecord, error) {
	if len(records) > config.Limits.MaxLedgerEntries || len(records) > 0 && config.Summarizer == nil {
		return nil, fmt.Errorf("%w: invalid summary cache configuration", ErrInvalidCheckpoint)
	}
	result := make(map[summaryKey]summaryRecord, len(records))
	for _, record := range records {
		key := summaryKey{record.SourceID, record.TokenBudget}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("%w: duplicate summary record", ErrInvalidCheckpoint)
		}
		source, sourceErr := store.Lookup(record.SourceID)
		summary, summaryErr := store.Lookup(record.SummaryID)
		if sourceErr != nil || summaryErr != nil || record.TokenBudget <= 0 || summary.TokenCount > record.TokenBudget ||
			summary.Scope != source.Scope || summary.WorkflowID != source.WorkflowID || summary.AgentID != source.AgentID ||
			len(summary.SourceRefs) != 1 || summary.SourceRefs[0] != (contextvm.ContextRef{PageID: source.ID}) {
			return nil, fmt.Errorf("%w: invalid summary provenance or budget", ErrInvalidCheckpoint)
		}
		result[key] = record
	}
	return result, nil
}

func sortedSummaries(value components) []summaryRecord {
	result := make([]summaryRecord, 0, len(value.summaries))
	for _, record := range value.summaries {
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SourceID != result[j].SourceID {
			return result[i].SourceID < result[j].SourceID
		}
		return result[i].TokenBudget < result[j].TokenBudget
	})
	return result
}

func componentIdentities(config Config) (string, string) {
	counter, summarizer := "unversioned", "none-v1"
	if named, ok := config.Counter.(contextvm.NamedCounter); ok {
		counter = named.Name()
	}
	if config.Summarizer != nil {
		summarizer = "unversioned"
		if named, ok := config.Summarizer.(contextvm.NamedSummarizer); ok {
			summarizer = named.Name()
		}
	}
	return counter, summarizer
}

func validatePersistentIdentities(config Config) error {
	counter, summarizer := componentIdentities(config)
	for _, identity := range []string{counter, summarizer} {
		if identity == "unversioned" || identity == "" || len(identity) > 128 || strings.TrimSpace(identity) != identity || strings.ContainsAny(identity, "\r\n\x00") {
			return fmt.Errorf("persistent Context requires named, versioned counter and summarizer identities")
		}
	}
	return nil
}
