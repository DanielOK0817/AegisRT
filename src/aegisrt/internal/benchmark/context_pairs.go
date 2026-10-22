package benchmark

import "fmt"

func validateContextEvidencePairs(rows []ContextRow, traces []ContextTrialTrace) error {
	if len(rows) == 0 || len(rows) != len(traces) {
		return fmt.Errorf("context rows and traces must be non-empty and one-to-one")
	}
	pairs := make(map[string][]ContextTrialTrace)
	seenTrials := make(map[string]struct{}, len(traces))
	runID, workloadHash, inputHash := traces[0].RunID, traces[0].WorkloadSHA256, traces[0].InputSHA256
	for index, trace := range traces {
		if trace.RunID != runID || trace.WorkloadSHA256 != workloadHash || trace.InputSHA256 != inputHash {
			return fmt.Errorf("context trace %d changed run or canonical input identity", index)
		}
		derived, err := contextRowFromTrace(trace)
		if err != nil {
			return fmt.Errorf("context trace %d: %w", index, err)
		}
		if derived != rows[index] {
			return fmt.Errorf("context row %d cannot be reproduced from its trace", index)
		}
		key := trace.PairID + "/" + trace.Treatment
		if _, exists := seenTrials[key]; exists {
			return fmt.Errorf("duplicate context trial %q", key)
		}
		seenTrials[key] = struct{}{}
		pairs[trace.PairID] = append(pairs[trace.PairID], trace)
	}
	for pairID, pair := range pairs {
		if len(pair) != 2 {
			return fmt.Errorf("context pair %q must contain two trials", pairID)
		}
		baseline, treatment, err := orderedContextPair(pair)
		if err != nil {
			return fmt.Errorf("context pair %q: %w", pairID, err)
		}
		if baseline.RunID != treatment.RunID || baseline.CaseID != treatment.CaseID || baseline.Seed != treatment.Seed ||
			baseline.Repetition != treatment.Repetition || baseline.WorkloadSHA256 != treatment.WorkloadSHA256 ||
			baseline.InputSHA256 != treatment.InputSHA256 || baseline.ComparisonBudget != treatment.ComparisonBudget {
			return fmt.Errorf("context pair %q changed a controlled input", pairID)
		}
		switch baseline.CaseID {
		case "C1-smoke":
			if err := validateC1PairEquivalence(baseline, treatment); err != nil {
				return fmt.Errorf("context pair %q: %w", pairID, err)
			}
		case "C2-smoke":
			baseRow, _ := contextRowFromTrace(baseline)
			treatmentRow, _ := contextRowFromTrace(treatment)
			if baseRow.PageCount != treatmentRow.PageCount ||
				baseRow.StorageLogicalTokens != treatmentRow.StorageLogicalTokens ||
				baseRow.StoragePhysicalTokens != treatmentRow.StoragePhysicalTokens ||
				baseRow.StorageReusedTokens != treatmentRow.StorageReusedTokens {
				return fmt.Errorf("C2 storage inputs changed between modes")
			}
		case "C3-smoke":
			if len(baseline.Dispatches) != 1 || len(treatment.Dispatches) != 1 ||
				baseline.Dispatches[0].ReferencedTokens != treatment.Dispatches[0].ReferencedTokens ||
				len(baseline.Dispatches[0].Pages) != len(treatment.Dispatches[0].Pages) {
				return fmt.Errorf("C3 source inputs changed between modes")
			}
			for index, basePage := range baseline.Dispatches[0].Pages {
				treatmentPage := treatment.Dispatches[0].Pages[index]
				if basePage.PageID != treatmentPage.PageID || basePage.ContentSHA256 != treatmentPage.ContentSHA256 ||
					basePage.TokenCount != treatmentPage.TokenCount || basePage.Pin != treatmentPage.Pin || basePage.Cold != treatmentPage.Cold {
					return fmt.Errorf("C3 source page %d changed between modes", index)
				}
			}
		}
	}
	return nil
}

func orderedContextPair(pair []ContextTrialTrace) (ContextTrialTrace, ContextTrialTrace, error) {
	var baseline, treatment *ContextTrialTrace
	orders := map[int]int{}
	for index := range pair {
		orders[pair[index].TrialOrder]++
		switch pair[index].Role {
		case "baseline":
			baseline = &pair[index]
		case "treatment":
			treatment = &pair[index]
		default:
			return ContextTrialTrace{}, ContextTrialTrace{}, fmt.Errorf("invalid role %q", pair[index].Role)
		}
	}
	if baseline == nil || treatment == nil || orders[0] != 1 || orders[1] != 1 || len(orders) != 2 {
		return ContextTrialTrace{}, ContextTrialTrace{}, fmt.Errorf("invalid roles or trial order")
	}
	return *baseline, *treatment, nil
}

func validateC1PairEquivalence(baseline, treatment ContextTrialTrace) error {
	baseHashes := make(map[string]string, len(baseline.Dispatches))
	for _, dispatch := range baseline.Dispatches {
		baseHashes[dispatch.AgentID] = dispatch.TextSHA256
	}
	for _, dispatch := range treatment.Dispatches {
		if baseHashes[dispatch.AgentID] != dispatch.TextSHA256 {
			return fmt.Errorf("assembled text changed for agent %q", dispatch.AgentID)
		}
	}
	for _, trace := range []ContextTrialTrace{baseline, treatment} {
		pageIDs := make(map[string]struct{}, 4)
		contentHash := ""
		for _, dispatch := range trace.Dispatches {
			page := dispatch.Pages[0]
			if _, exists := pageIDs[page.PageID]; exists {
				return fmt.Errorf("C1 ACL pages are not distinct")
			}
			pageIDs[page.PageID] = struct{}{}
			if contentHash == "" {
				contentHash = page.ContentSHA256
			} else if contentHash != page.ContentSHA256 {
				return fmt.Errorf("C1 page content is not identical")
			}
		}
	}
	return nil
}
