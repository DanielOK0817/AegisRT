package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"aegisrt/internal/contextvm"
)

type contextPageInstance struct {
	descriptor contextPageDescriptor
	page       contextvm.Page
	ref        contextvm.ContextRef
}

type contextTrialData struct {
	stores             []ContextStoreTrace
	dispatches         []ContextDispatchTrace
	aclProbes          []ContextACLProbe
	manifestOperations int
	manifestConflicts  int
}

func instantiateContextPage(workload contextCaseWorkload, descriptor contextPageDescriptor) (contextPageInstance, error) {
	content, err := generatedContextContent(descriptor.Key, workload.Seed)
	if err != nil {
		return contextPageInstance{}, err
	}
	page, err := contextvm.NewPage(contextvm.PageSpec{
		Content: content, Scope: contextvm.Scope(descriptor.Scope), WorkflowID: workload.WorkflowID, AgentID: descriptor.AgentID,
	}, contextvm.WhitespaceCounter{})
	if err != nil {
		return contextPageInstance{}, err
	}
	if page.ContentHash != descriptor.ContentSHA256 || page.TokenCount != descriptor.TokenCount {
		return contextPageInstance{}, fmt.Errorf("generated page %q differs from canonical descriptor", descriptor.Key)
	}
	return contextPageInstance{descriptor: descriptor, page: page}, nil
}

func putContextPage(store *contextvm.Store, instance contextPageInstance) (contextPageInstance, error) {
	ref, exactReused, err := store.Put(instance.page)
	if err != nil {
		return contextPageInstance{}, err
	}
	if exactReused {
		return contextPageInstance{}, fmt.Errorf("page %s was put more than once", instance.page.ID)
	}
	ref.Pin, ref.Cold = instance.descriptor.Pin, instance.descriptor.Cold
	instance.ref = ref
	return instance, nil
}

func contextStoreObservation(storeID string, stats contextvm.StoreStats) ContextStoreTrace {
	return ContextStoreTrace{
		StoreID: storeID, PutCalls: int(stats.PutCalls), UniquePages: int(stats.UniquePages),
		UniqueBlobs: int(stats.UniqueBlobs), ReusedPuts: int(stats.ReusedPuts),
		LogicalTokens: int(stats.LogicalTokens), PhysicalTokens: int(stats.PhysicalTokens),
		ReusedTokens: int(stats.ReusedTokens),
	}
}

func contextDispatchObservation(
	taskID, agentID, storeID string,
	instances []contextPageInstance,
	assembly contextvm.Assembly,
	appliedBudget int,
) (ContextDispatchTrace, error) {
	selected := make(map[contextvm.PageID]contextvm.ContextRef, len(assembly.Refs))
	summaries := make(map[contextvm.PageID]contextvm.Page)
	for index, ref := range assembly.Refs {
		if _, exists := selected[ref.PageID]; exists {
			return ContextDispatchTrace{}, fmt.Errorf("assembly selected page %s more than once", ref.PageID)
		}
		selected[ref.PageID] = ref
		page := assembly.Pages[index]
		for _, source := range page.SourceRefs {
			if _, exists := summaries[source.PageID]; exists {
				return ContextDispatchTrace{}, fmt.Errorf("source page %s was summarized more than once", source.PageID)
			}
			summaries[source.PageID] = page
		}
	}
	record := ContextDispatchTrace{
		TaskID: taskID, AgentID: agentID, StoreID: storeID, AppliedBudget: appliedBudget,
		ReferencedTokens: assembly.Metrics.LogicalTokens, AssembledTokens: assembly.Metrics.PhysicalTokens,
		ReusedRefTokens: assembly.Metrics.ReusedTokens, DroppedTokens: assembly.Metrics.DroppedTokens,
		SelectedPages: assembly.Metrics.SelectedPages, DroppedPages: assembly.Metrics.DroppedPages,
		SummaryPages: assembly.Metrics.SummaryPages, SummarizedSourceTokens: assembly.Metrics.SummarizedSourceTokens,
		SummaryTokens: assembly.Metrics.SummaryTokens, PinnedPagePreserved: true,
	}
	sum := sha256.Sum256([]byte(assembly.Text))
	record.TextSHA256 = hex.EncodeToString(sum[:])
	for _, instance := range instances {
		_, isSelected := selected[instance.page.ID]
		outcome := "dropped"
		if isSelected {
			outcome = "selected"
		} else if _, summarized := summaries[instance.page.ID]; summarized {
			outcome = "summarized"
		}
		if instance.descriptor.Pin {
			record.PinnedPageRequired = true
			record.PinnedPagePreserved = record.PinnedPagePreserved && isSelected
		}
		pageRecord := ContextPageOutcomeTrace{
			PageID: string(instance.page.ID), ContentSHA256: instance.page.ContentHash,
			Scope: string(instance.page.Scope), OwnerAgentID: instance.page.AgentID,
			TokenCount: instance.page.TokenCount, Pin: instance.ref.Pin, Cold: instance.ref.Cold, Outcome: outcome,
		}
		if summary, summarized := summaries[instance.page.ID]; summarized {
			pageRecord.SummaryPageID = string(summary.ID)
			pageRecord.SummaryContentHash = summary.ContentHash
			pageRecord.SummaryTokenCount = summary.TokenCount
		}
		record.Pages = append(record.Pages, pageRecord)
	}
	if !record.PinnedPageRequired {
		record.PinnedPagePreserved = true
	}
	if record.SelectedPages+record.DroppedPages != len(instances) {
		return ContextDispatchTrace{}, fmt.Errorf("assembly page counts do not match workload")
	}
	return record, nil
}

func refsForInstances(instances []contextPageInstance) []contextvm.ContextRef {
	refs := make([]contextvm.ContextRef, 0, len(instances))
	for _, instance := range instances {
		refs = append(refs, instance.ref)
	}
	return refs
}
