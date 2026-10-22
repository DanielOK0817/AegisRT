package benchmark

import (
	"fmt"
	"math"
)

func validateContextTraceAndRow(trace ContextTrialTrace, row ContextRow) error {
	if trace.SchemaVersion != contextTrialTraceSchemaV2 || trace.EvidenceScope != "windows-functional" ||
		trace.PlanID != contextPlanID || trace.Status != "SUCCEEDED" || trace.CounterName != contextCounterName ||
		trace.ProviderCacheMode != contextProviderCacheMode || !validSummarizerMode(trace) {
		return fmt.Errorf("context trace has unsupported identity")
	}
	if trace.RunID != "" {
		if err := validateRunID(trace.RunID); err != nil {
			return err
		}
	}
	if !validLabel(trace.PairID, true) || !validContextMode(trace.CaseID, trace.Treatment, trace.Role, trace.ContextMode) ||
		len(trace.WorkloadSHA256) != 64 || !isLowerHex(trace.WorkloadSHA256) ||
		len(trace.InputSHA256) != 64 || !isLowerHex(trace.InputSHA256) || trace.ComparisonBudget <= 0 ||
		trace.ElapsedNS < 0 || trace.ManifestOperations <= 0 || trace.ManifestConflicts != 0 ||
		len(trace.Stores) == 0 || len(trace.Dispatches) == 0 {
		return fmt.Errorf("context trace has invalid protocol fields")
	}
	stores := make(map[string]struct{}, len(trace.Stores))
	for _, store := range trace.Stores {
		if !validLabel(store.StoreID, true) {
			return fmt.Errorf("invalid context store id %q", store.StoreID)
		}
		if _, exists := stores[store.StoreID]; exists {
			return fmt.Errorf("duplicate context store %q", store.StoreID)
		}
		stores[store.StoreID] = struct{}{}
		if store.PutCalls <= 0 || store.UniquePages <= 0 || store.UniqueBlobs <= 0 || store.ReusedPuts < 0 ||
			store.LogicalTokens <= 0 || store.PhysicalTokens <= 0 || store.ReusedTokens < 0 ||
			store.PutCalls != store.UniquePages || store.UniqueBlobs > store.UniquePages ||
			store.ReusedPuts != store.PutCalls-store.UniqueBlobs || store.LogicalTokens != store.PhysicalTokens+store.ReusedTokens {
			return fmt.Errorf("context store %q has inconsistent counters", store.StoreID)
		}
	}
	for _, dispatch := range trace.Dispatches {
		if err := validateContextDispatch(dispatch, stores); err != nil {
			return err
		}
	}
	for _, probe := range trace.ACLProbes {
		if len(probe.PageID) != 64 || !isLowerHex(probe.PageID) || !validLabel(probe.OwnerAgentID, true) ||
			!validLabel(probe.RequestAgentID, true) || probe.OwnerAgentID == probe.RequestAgentID || probe.ResultCode != "ACCESS_DENIED" {
			return fmt.Errorf("context ACL probe is invalid")
		}
	}
	if trace.ManifestOperations != len(trace.Dispatches) {
		return fmt.Errorf("manifest operation count does not match dispatch count")
	}
	if err := validateContextCaseSemantics(trace, row); err != nil {
		return err
	}
	if row.SchemaVersion != contextSchemaV2 || row.RunID != trace.RunID || row.Status != trace.Status ||
		row.EvidenceScope != trace.EvidenceScope || row.CaseID != trace.CaseID || row.PairID != trace.PairID ||
		row.Seed != trace.Seed || row.Repetition != trace.Repetition || row.TrialOrder != trace.TrialOrder ||
		row.Treatment != trace.Treatment || row.Role != trace.Role || row.CounterName != trace.CounterName ||
		row.ContextMode != trace.ContextMode || row.ProviderCacheMode != trace.ProviderCacheMode ||
		row.WorkloadSHA256 != trace.WorkloadSHA256 || row.TimeoutMS != trialTimeoutMS || row.ReasonCode != "NONE" ||
		math.Abs(row.ElapsedMS-float64(trace.ElapsedNS)/1e6) > 1e-9 {
		return fmt.Errorf("context row identity does not match trace")
	}
	return nil
}

func validateContextDispatch(dispatch ContextDispatchTrace, stores map[string]struct{}) error {
	if !validLabel(dispatch.TaskID, true) || !validLabel(dispatch.AgentID, true) ||
		len(dispatch.TextSHA256) != 64 || !isLowerHex(dispatch.TextSHA256) || dispatch.AppliedBudget <= 0 {
		return fmt.Errorf("context dispatch has invalid identity")
	}
	if _, exists := stores[dispatch.StoreID]; !exists {
		return fmt.Errorf("context dispatch references unknown store %q", dispatch.StoreID)
	}
	if dispatch.ReferencedTokens <= 0 || dispatch.AssembledTokens < 0 || dispatch.ReusedRefTokens < 0 ||
		dispatch.DroppedTokens < 0 || dispatch.SelectedPages < 0 || dispatch.DroppedPages < 0 ||
		dispatch.SummaryPages < 0 || dispatch.SummarizedSourceTokens < 0 || dispatch.SummaryTokens < 0 ||
		dispatch.ReusedRefTokens != 0 || dispatch.AssembledTokens > dispatch.AppliedBudget ||
		dispatch.SelectedPages+dispatch.DroppedPages != len(dispatch.Pages) {
		return fmt.Errorf("context dispatch has inconsistent counters")
	}
	pageIDs := make(map[string]struct{}, len(dispatch.Pages))
	selected, dropped, summarized := 0, 0, 0
	referencedTokens, selectedTokens, droppedTokens := 0, 0, 0
	summarizedSourceTokens, summaryTokens := 0, 0
	pinnedRequired, pinnedPreserved := false, true
	for _, page := range dispatch.Pages {
		if len(page.PageID) != 64 || !isLowerHex(page.PageID) || len(page.ContentSHA256) != 64 || !isLowerHex(page.ContentSHA256) ||
			page.TokenCount <= 0 || page.Pin && page.Cold ||
			page.Outcome != "selected" && page.Outcome != "dropped" && page.Outcome != "summarized" {
			return fmt.Errorf("context dispatch contains invalid page evidence")
		}
		if (page.Scope == "agent" && (!validLabel(page.OwnerAgentID, true) || page.OwnerAgentID != dispatch.AgentID)) ||
			(page.Scope == "workflow" && page.OwnerAgentID != "") ||
			(page.Scope != "agent" && page.Scope != "workflow") {
			return fmt.Errorf("context dispatch page has invalid scope ownership")
		}
		if _, exists := pageIDs[page.PageID]; exists {
			return fmt.Errorf("context dispatch contains duplicate page %q", page.PageID)
		}
		pageIDs[page.PageID] = struct{}{}
		referencedTokens += page.TokenCount
		if page.Outcome == "summarized" {
			if !page.Cold || len(page.SummaryPageID) != 64 || !isLowerHex(page.SummaryPageID) ||
				len(page.SummaryContentHash) != 64 || !isLowerHex(page.SummaryContentHash) || page.SummaryTokenCount <= 0 {
				return fmt.Errorf("context dispatch contains invalid summary evidence")
			}
			summarized++
			summarizedSourceTokens += page.TokenCount
			summaryTokens += page.SummaryTokenCount
			selectedTokens += page.SummaryTokenCount
		} else if page.SummaryPageID != "" || page.SummaryContentHash != "" || page.SummaryTokenCount != 0 {
			return fmt.Errorf("non-summary page contains summary evidence")
		} else if page.Outcome == "selected" {
			selected++
			selectedTokens += page.TokenCount
		} else {
			dropped++
			droppedTokens += page.TokenCount
		}
		if page.Pin {
			pinnedRequired = true
			pinnedPreserved = pinnedPreserved && page.Outcome == "selected"
		}
	}
	if selected+summarized != dispatch.SelectedPages || dropped != dispatch.DroppedPages || summarized != dispatch.SummaryPages ||
		referencedTokens != dispatch.ReferencedTokens || selectedTokens != dispatch.AssembledTokens || droppedTokens != dispatch.DroppedTokens ||
		summarizedSourceTokens != dispatch.SummarizedSourceTokens || summaryTokens != dispatch.SummaryTokens ||
		pinnedRequired != dispatch.PinnedPageRequired || pinnedPreserved != dispatch.PinnedPagePreserved {
		return fmt.Errorf("context dispatch page outcomes do not match aggregates")
	}
	return nil
}

func validSummarizerMode(trace ContextTrialTrace) bool {
	if trace.CaseID == "C3-smoke" && trace.Role == "treatment" {
		return trace.SummarizerMode == "deterministic-extractive-v1"
	}
	return trace.SummarizerMode == "disabled"
}

func validateContextCaseSemantics(trace ContextTrialTrace, row ContextRow) error {
	if row.StorageLogicalTokens != row.StoragePhysicalTokens+row.StorageReusedTokens ||
		row.ManifestOperations != trace.ManifestOperations || row.ManifestConflicts != trace.ManifestConflicts {
		return fmt.Errorf("context row storage or manifest counters are inconsistent")
	}
	switch trace.CaseID {
	case "C1-smoke":
		if len(trace.Dispatches) != 4 || len(trace.ACLProbes) != 1 || trace.ComparisonBudget != 6 ||
			row.PageCount != 4 || row.StorageLogicalTokens != 24 || row.DispatchInputTokens != 24 ||
			row.DroppedTokens != 0 || row.SummaryPages != 0 || row.TargetBudgetExceedances != 0 ||
			trace.ACLProbes[0].ResultCode != "ACCESS_DENIED" || trace.ACLProbes[0].OwnerAgentID == trace.ACLProbes[0].RequestAgentID {
			return fmt.Errorf("C1 evidence violates controlled workload semantics")
		}
		if err := validateC1Dispatches(trace); err != nil {
			return err
		}
		if err := validateC1Stores(trace); err != nil {
			return fmt.Errorf("C1 storage topology does not match role")
		}
	case "C2-smoke":
		if len(trace.Stores) != 1 || len(trace.Dispatches) != 1 || len(trace.Dispatches[0].Pages) != 3 || len(trace.ACLProbes) != 0 || trace.ComparisonBudget != 6 ||
			row.PageCount != 3 || row.StorageLogicalTokens != 12 || row.StoragePhysicalTokens != 12 || row.StorageReusedTokens != 0 ||
			row.SummaryPages != 0 || !trace.Dispatches[0].PinnedPageRequired || !trace.Dispatches[0].PinnedPagePreserved {
			return fmt.Errorf("C2 evidence violates controlled workload semantics")
		}
		if trace.Role == "baseline" && (row.DispatchInputTokens != 12 || row.DroppedTokens != 0 || row.TargetBudgetExceedances != 1) ||
			trace.Role == "treatment" && (row.DispatchInputTokens != 6 || row.DroppedTokens != 6 || row.TargetBudgetExceedances != 0) {
			return fmt.Errorf("C2 budget behavior does not match role")
		}
		if err := validateC2Trace(trace); err != nil {
			return err
		}
	case "C3-smoke":
		if len(trace.Stores) != 1 || len(trace.Dispatches) != 1 || len(trace.Dispatches[0].Pages) != 3 ||
			len(trace.ACLProbes) != 0 || trace.ComparisonBudget != 8 || row.TargetBudgetExceedances != 0 ||
			!trace.Dispatches[0].PinnedPageRequired || !trace.Dispatches[0].PinnedPagePreserved {
			return fmt.Errorf("C3 evidence violates controlled workload semantics")
		}
		if trace.Role == "baseline" && (row.PageCount != 3 || row.StorageLogicalTokens != 12 ||
			row.StoragePhysicalTokens != 12 || row.DispatchInputTokens != 6 || row.DroppedTokens != 6 ||
			row.SummaryPages != 0 || row.SummarizedSourceTokens != 0 || row.SummaryTokens != 0) {
			return fmt.Errorf("C3 baseline behavior does not match controlled semantics")
		}
		if trace.Role == "treatment" && (row.PageCount != 4 || row.StorageLogicalTokens != 17 ||
			row.StoragePhysicalTokens != 17 || row.DispatchInputTokens != 8 || row.DroppedTokens != 3 ||
			row.SummaryPages != 1 || row.SummarizedSourceTokens != 6 || row.SummaryTokens != 5) {
			return fmt.Errorf("C3 compression behavior does not match controlled semantics")
		}
		if err := validateC3Trace(trace); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported context case %q", trace.CaseID)
	}
	return nil
}

func validateC3Trace(trace ContextTrialTrace) error {
	store, dispatch := trace.Stores[0], trace.Dispatches[0]
	if store.StoreID != "store-context" || dispatch.TaskID != "task-context" || dispatch.AgentID != "agent-context" ||
		dispatch.StoreID != "store-context" || dispatch.AppliedBudget != 8 || dispatch.ReferencedTokens != 12 ||
		len(dispatch.Pages) != 3 {
		return fmt.Errorf("C3 trace identity violates controlled semantics")
	}
	wantOutcomes := []string{"dropped", "selected", "selected"}
	if trace.Role == "baseline" {
		if store.PutCalls != 3 || store.UniquePages != 3 || store.UniqueBlobs != 3 || store.LogicalTokens != 12 ||
			store.PhysicalTokens != 12 || dispatch.AssembledTokens != 6 || dispatch.DroppedTokens != 6 ||
			dispatch.SelectedPages != 2 || dispatch.DroppedPages != 1 || dispatch.SummaryPages != 0 {
			return fmt.Errorf("C3 baseline counters violate controlled semantics")
		}
	} else {
		wantOutcomes = []string{"summarized", "dropped", "selected"}
		if store.PutCalls != 4 || store.UniquePages != 4 || store.UniqueBlobs != 4 || store.LogicalTokens != 17 ||
			store.PhysicalTokens != 17 || dispatch.AssembledTokens != 8 || dispatch.DroppedTokens != 3 ||
			dispatch.SelectedPages != 2 || dispatch.DroppedPages != 1 || dispatch.SummaryPages != 1 ||
			dispatch.SummarizedSourceTokens != 6 || dispatch.SummaryTokens != 5 {
			return fmt.Errorf("C3 compression counters violate controlled semantics")
		}
	}
	wantTokens := []int{6, 3, 3}
	wantPins := []bool{false, false, true}
	wantCold := []bool{true, false, false}
	for index, page := range dispatch.Pages {
		if page.Scope != "workflow" || page.OwnerAgentID != "" || page.TokenCount != wantTokens[index] ||
			page.Pin != wantPins[index] || page.Cold != wantCold[index] || page.Outcome != wantOutcomes[index] {
			return fmt.Errorf("C3 page %d violates controlled semantics", index)
		}
	}
	return nil
}

func validateC1Dispatches(trace ContextTrialTrace) error {
	wantStores := map[string]string{
		"agent-1": "store-0", "agent-2": "store-1", "agent-3": "store-2", "agent-4": "store-3",
	}
	pageByAgent := make(map[string]string, len(trace.Dispatches))
	for _, dispatch := range trace.Dispatches {
		wantStore, exists := wantStores[dispatch.AgentID]
		if !exists {
			return fmt.Errorf("C1 contains unknown agent %q", dispatch.AgentID)
		}
		if trace.Role == "treatment" {
			wantStore = "store-shared"
		}
		if _, duplicate := pageByAgent[dispatch.AgentID]; duplicate || dispatch.TaskID != "task-"+dispatch.AgentID ||
			dispatch.StoreID != wantStore || dispatch.AppliedBudget != 6 || dispatch.ReferencedTokens != 6 ||
			dispatch.AssembledTokens != 6 || dispatch.DroppedTokens != 0 || dispatch.SelectedPages != 1 ||
			dispatch.DroppedPages != 0 || dispatch.PinnedPageRequired || !dispatch.PinnedPagePreserved || len(dispatch.Pages) != 1 {
			return fmt.Errorf("C1 dispatch for %q violates controlled semantics", dispatch.AgentID)
		}
		page := dispatch.Pages[0]
		if page.OwnerAgentID != dispatch.AgentID || page.TokenCount != 6 || page.Pin || page.Cold || page.Outcome != "selected" {
			return fmt.Errorf("C1 page for %q violates controlled semantics", dispatch.AgentID)
		}
		pageByAgent[dispatch.AgentID] = page.PageID
	}
	probe := trace.ACLProbes[0]
	if probe.OwnerAgentID != "agent-1" || probe.RequestAgentID != "agent-2" || probe.PageID != pageByAgent["agent-1"] {
		return fmt.Errorf("C1 ACL probe does not target the canonical owner page")
	}
	return nil
}

func validateC1Stores(trace ContextTrialTrace) error {
	if trace.Role == "treatment" {
		store := trace.Stores[0]
		if len(trace.Stores) != 1 || store.StoreID != "store-shared" || store.PutCalls != 4 || store.UniquePages != 4 ||
			store.UniqueBlobs != 1 || store.ReusedPuts != 3 || store.LogicalTokens != 24 ||
			store.PhysicalTokens != 6 || store.ReusedTokens != 18 {
			return fmt.Errorf("invalid shared store counters")
		}
		return nil
	}
	if len(trace.Stores) != 4 {
		return fmt.Errorf("isolated baseline must contain four stores")
	}
	seen := make(map[string]struct{}, len(trace.Stores))
	for _, store := range trace.Stores {
		if _, duplicate := seen[store.StoreID]; duplicate ||
			(store.StoreID != "store-0" && store.StoreID != "store-1" && store.StoreID != "store-2" && store.StoreID != "store-3") ||
			store.PutCalls != 1 || store.UniquePages != 1 || store.UniqueBlobs != 1 || store.ReusedPuts != 0 ||
			store.LogicalTokens != 6 || store.PhysicalTokens != 6 || store.ReusedTokens != 0 {
			return fmt.Errorf("invalid isolated store counters")
		}
		seen[store.StoreID] = struct{}{}
	}
	return nil
}

func validateC2Trace(trace ContextTrialTrace) error {
	store := trace.Stores[0]
	if store.StoreID != "store-context" || store.PutCalls != 3 || store.UniquePages != 3 || store.UniqueBlobs != 3 ||
		store.ReusedPuts != 0 || store.LogicalTokens != 12 || store.PhysicalTokens != 12 || store.ReusedTokens != 0 {
		return fmt.Errorf("C2 store counters violate controlled semantics")
	}
	dispatch := trace.Dispatches[0]
	if dispatch.TaskID != "task-context" || dispatch.AgentID != "agent-context" || dispatch.StoreID != "store-context" ||
		dispatch.ReferencedTokens != 12 || len(dispatch.Pages) != 3 {
		return fmt.Errorf("C2 dispatch identity violates controlled semantics")
	}
	wantOutcomes := []string{"selected", "selected", "selected"}
	wantBudget, wantAssembled, wantDropped, wantSelected, wantDroppedPages := 12, 12, 0, 3, 0
	if trace.Role == "treatment" {
		wantOutcomes[0] = "dropped"
		wantBudget, wantAssembled, wantDropped, wantSelected, wantDroppedPages = 6, 6, 6, 2, 1
	}
	if dispatch.AppliedBudget != wantBudget || dispatch.AssembledTokens != wantAssembled ||
		dispatch.DroppedTokens != wantDropped || dispatch.SelectedPages != wantSelected || dispatch.DroppedPages != wantDroppedPages {
		return fmt.Errorf("C2 dispatch counters violate controlled semantics")
	}
	wantTokens := []int{6, 3, 3}
	wantPins := []bool{false, false, true}
	wantCold := []bool{true, false, false}
	for index, page := range dispatch.Pages {
		if page.Scope != "workflow" || page.OwnerAgentID != "" || page.TokenCount != wantTokens[index] ||
			page.Pin != wantPins[index] || page.Cold != wantCold[index] || page.Outcome != wantOutcomes[index] {
			return fmt.Errorf("C2 page %d violates controlled semantics", index)
		}
	}
	return nil
}

func validContextMode(caseID, treatment, role, mode string) bool {
	return caseID == "C1-smoke" && treatment == "copy-per-task" && role == "baseline" && mode == "isolated-store-copy-v1" ||
		caseID == "C1-smoke" && treatment == "contextvm-dedup" && role == "treatment" && mode == "shared-store-blob-dedup-v1" ||
		caseID == "C2-smoke" && treatment == "unbudgeted-logical" && role == "baseline" && mode == "unbudgeted-assembly-v1" ||
		caseID == "C2-smoke" && treatment == "contextvm-budgeted" && role == "treatment" && mode == "budgeted-drop-no-summary-v1" ||
		caseID == "C3-smoke" && treatment == "contextvm-drop" && role == "baseline" && mode == "budgeted-drop-no-summary-v1" ||
		caseID == "C3-smoke" && treatment == "contextvm-compress" && role == "treatment" && mode == "budgeted-extractive-summary-v1"
}
