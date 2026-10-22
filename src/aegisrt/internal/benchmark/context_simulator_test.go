package benchmark

import "testing"

func TestContextSmokeExercisesDedupAndBudgetDropWithoutFalseTokenClaims(t *testing.T) {
	evidence, err := SimulateContextEvidence(ContextPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Result.Rows) != 6 || len(evidence.TrialTraceRecords()) != 6 {
		t.Fatalf("rows/traces = %d/%d, want 6/6", len(evidence.Result.Rows), len(evidence.TrialTraceRecords()))
	}
	c1Base := contextRow(t, evidence.Result.Rows, "C1-smoke", "baseline")
	c1Treatment := contextRow(t, evidence.Result.Rows, "C1-smoke", "treatment")
	if c1Base.PageCount != 4 || c1Base.StorageLogicalTokens != 24 || c1Base.StoragePhysicalTokens != 24 || c1Base.StorageReusedTokens != 0 {
		t.Fatalf("C1 baseline storage metrics = %#v", c1Base)
	}
	if c1Treatment.PageCount != 4 || c1Treatment.StorageLogicalTokens != 24 || c1Treatment.StoragePhysicalTokens != 6 || c1Treatment.StorageReusedTokens != 18 {
		t.Fatalf("C1 treatment storage metrics = %#v", c1Treatment)
	}
	if c1Base.DispatchInputTokens != 24 || c1Treatment.DispatchInputTokens != 24 {
		t.Fatalf("C1 dispatch inputs changed: baseline=%d treatment=%d", c1Base.DispatchInputTokens, c1Treatment.DispatchInputTokens)
	}
	if c1Base.DroppedTokens != 0 || c1Treatment.DroppedTokens != 0 || c1Base.SummaryPages != 0 || c1Treatment.SummaryPages != 0 {
		t.Fatal("C1 unexpectedly dropped or summarized context")
	}

	c2Base := contextRow(t, evidence.Result.Rows, "C2-smoke", "baseline")
	c2Treatment := contextRow(t, evidence.Result.Rows, "C2-smoke", "treatment")
	if c2Base.StorageLogicalTokens != 12 || c2Base.StoragePhysicalTokens != 12 || c2Base.DispatchInputTokens != 12 || c2Base.TargetBudgetExceedances != 1 {
		t.Fatalf("C2 baseline metrics = %#v", c2Base)
	}
	if c2Treatment.StorageLogicalTokens != 12 || c2Treatment.StoragePhysicalTokens != 12 || c2Treatment.DispatchInputTokens != 6 ||
		c2Treatment.DroppedTokens != 6 || c2Treatment.SummaryPages != 0 || c2Treatment.TargetBudgetExceedances != 0 {
		t.Fatalf("C2 treatment metrics = %#v", c2Treatment)
	}
	if c2Treatment.StorageReusedTokens != 0 {
		t.Fatal("C2 distinct content was reported as storage reuse")
	}

	c3Base := contextRow(t, evidence.Result.Rows, "C3-smoke", "baseline")
	c3Treatment := contextRow(t, evidence.Result.Rows, "C3-smoke", "treatment")
	if c3Base.DispatchInputTokens != 6 || c3Base.DroppedTokens != 6 || c3Base.SummaryPages != 0 {
		t.Fatalf("C3 baseline metrics = %#v", c3Base)
	}
	if c3Treatment.DispatchInputTokens != 8 || c3Treatment.DroppedTokens != 3 || c3Treatment.SummaryPages != 1 ||
		c3Treatment.SummarizedSourceTokens != 6 || c3Treatment.SummaryTokens != 5 || c3Treatment.TargetBudgetExceedances != 0 {
		t.Fatalf("C3 treatment metrics = %#v", c3Treatment)
	}
	assertContextTraceSemantics(t, evidence.TrialTraceRecords())
	if hashBytes(evidence.CanonicalWorkloadJSON()) != evidence.Result.WorkloadSHA256 ||
		hashBytes(evidence.CanonicalInputJSON()) != evidence.Result.InputSHA256 {
		t.Fatal("canonical context bytes do not reproduce result hashes")
	}
}

func TestContextSmokeCounterbalancesOddSeedIndex(t *testing.T) {
	evidence, err := SimulateContextEvidence(ContextPlan{Seeds: []uint32{7, 11}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, row := range evidence.Result.Rows {
		if row.Seed != 11 {
			continue
		}
		seen++
		if row.Role == "treatment" && row.TrialOrder != 0 || row.Role == "baseline" && row.TrialOrder != 1 {
			t.Fatalf("odd seed row is not counterbalanced: %#v", row)
		}
	}
	if seen != 6 {
		t.Fatalf("odd seed rows = %d, want 6", seen)
	}
}

func contextRow(t *testing.T, rows []ContextRow, caseID, role string) ContextRow {
	t.Helper()
	for _, row := range rows {
		if row.CaseID == caseID && row.Role == role {
			return row
		}
	}
	t.Fatalf("missing %s/%s row", caseID, role)
	return ContextRow{}
}

func assertContextTraceSemantics(t *testing.T, records []ContextTrialTrace) {
	t.Helper()
	var c1Baseline, c1Treatment ContextTrialTrace
	for _, record := range records {
		if record.CaseID == "C1-smoke" {
			if len(record.Dispatches) != 4 || len(record.ACLProbes) != 1 || record.ACLProbes[0].ResultCode != "ACCESS_DENIED" {
				t.Fatalf("C1 trace lacks dispatch or ACL evidence: %#v", record)
			}
			if record.Role == "baseline" {
				c1Baseline = record
			} else {
				c1Treatment = record
			}
		}
		if record.CaseID == "C2-smoke" {
			if len(record.Dispatches) != 1 || !record.Dispatches[0].PinnedPageRequired || !record.Dispatches[0].PinnedPagePreserved {
				t.Fatalf("C2 trace lacks pinned-page evidence: %#v", record)
			}
		}
	}
	baseHashes := dispatchHashes(c1Baseline.Dispatches)
	for agentID, hash := range dispatchHashes(c1Treatment.Dispatches) {
		if baseHashes[agentID] != hash {
			t.Fatalf("C1 assembled text changed for %s", agentID)
		}
	}
}

func dispatchHashes(records []ContextDispatchTrace) map[string]string {
	result := make(map[string]string, len(records))
	for _, record := range records {
		result[record.AgentID] = record.TextSHA256
	}
	return result
}
