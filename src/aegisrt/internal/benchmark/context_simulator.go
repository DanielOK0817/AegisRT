package benchmark

import (
	"context"
	"fmt"
	"time"
)

type contextTrialMode struct {
	treatment  string
	role       string
	mode       string
	summarizer string
}

func SimulateContextEvidence(plan ContextPlan) (ContextEvidence, error) {
	return simulateContextEvidenceContext(context.Background(), plan, time.Now)
}

func simulateContextEvidenceContext(ctx context.Context, plan ContextPlan, clock func() time.Time) (ContextEvidence, error) {
	if ctx == nil || clock == nil {
		return ContextEvidence{}, fmt.Errorf("context and clock are required")
	}
	if err := validateContextPlan(plan); err != nil {
		return ContextEvidence{}, err
	}
	workloads, err := buildContextWorkloads(plan)
	if err != nil {
		return ContextEvidence{}, err
	}
	workloadJSON, err := canonicalJSON(workloads)
	if err != nil {
		return ContextEvidence{}, err
	}
	workloadHash := hashBytes(workloadJSON)
	inputJSON, err := canonicalJSON(buildContextInput(plan, workloadHash))
	if err != nil {
		return ContextEvidence{}, err
	}
	inputHash := hashBytes(inputJSON)
	result := ContextResult{WorkloadSHA256: workloadHash, InputSHA256: inputHash}
	evidence := ContextEvidence{
		workloadJSON: append([]byte(nil), workloadJSON...), inputJSON: append([]byte(nil), inputJSON...),
	}
	for seedIndex := range plan.Seeds {
		for _, workload := range workloads.Cases[seedIndex*3 : seedIndex*3+3] {
			for repetition := 0; repetition < plan.Repetitions; repetition++ {
				modes, err := contextModes(workload.CaseID)
				if err != nil {
					return ContextEvidence{}, err
				}
				if seedIndex%2 == 1 {
					modes[0], modes[1] = modes[1], modes[0]
				}
				pairID := contextPairID(workload.CaseID, workload.Seed, repetition)
				for order, mode := range modes {
					if err := ctx.Err(); err != nil {
						return ContextEvidence{}, err
					}
					started := clock()
					trial, runErr := runContextTrial(ctx, workload, mode.treatment)
					if runErr != nil {
						return ContextEvidence{}, fmt.Errorf("run %s seed %d: %w", mode.treatment, workload.Seed, runErr)
					}
					if err := ctx.Err(); err != nil {
						return ContextEvidence{}, err
					}
					finished := clock()
					elapsed := finished.Sub(started)
					if elapsed < 0 {
						return ContextEvidence{}, fmt.Errorf("trial clock moved backwards")
					}
					trace := makeContextTrialTrace(
						workload, pairID, repetition, order, mode, workloadHash, inputHash, elapsed, trial,
					)
					row, err := contextRowFromTrace(trace)
					if err != nil {
						return ContextEvidence{}, err
					}
					result.Rows = append(result.Rows, row)
					result.traces = append(result.traces, trialTrace{rowIndex: len(result.Rows) - 1, started: started, finished: finished})
					evidence.trialTraces = append(evidence.trialTraces, trace)
				}
			}
		}
	}
	if err := validateContextEvidencePairs(result.Rows, evidence.trialTraces); err != nil {
		return ContextEvidence{}, err
	}
	evidence.Result = result
	return evidence, nil
}

func runContextTrial(ctx context.Context, workload contextCaseWorkload, treatment string) (contextTrialData, error) {
	switch workload.CaseID {
	case "C1-smoke":
		return runC1Trial(ctx, workload, treatment)
	case "C2-smoke":
		return runC2Trial(ctx, workload, treatment)
	case "C3-smoke":
		return runC3Trial(ctx, workload, treatment)
	default:
		return contextTrialData{}, fmt.Errorf("unsupported context case %q", workload.CaseID)
	}
}

func contextModes(caseID string) ([2]contextTrialMode, error) {
	switch caseID {
	case "C1-smoke":
		return [2]contextTrialMode{
			{treatment: "copy-per-task", role: "baseline", mode: "isolated-store-copy-v1", summarizer: "disabled"},
			{treatment: "contextvm-dedup", role: "treatment", mode: "shared-store-blob-dedup-v1", summarizer: "disabled"},
		}, nil
	case "C2-smoke":
		return [2]contextTrialMode{
			{treatment: "unbudgeted-logical", role: "baseline", mode: "unbudgeted-assembly-v1", summarizer: "disabled"},
			{treatment: "contextvm-budgeted", role: "treatment", mode: "budgeted-drop-no-summary-v1", summarizer: "disabled"},
		}, nil
	case "C3-smoke":
		return [2]contextTrialMode{
			{treatment: "contextvm-drop", role: "baseline", mode: "budgeted-drop-no-summary-v1", summarizer: "disabled"},
			{treatment: "contextvm-compress", role: "treatment", mode: "budgeted-extractive-summary-v1", summarizer: "deterministic-extractive-v1"},
		}, nil
	default:
		return [2]contextTrialMode{}, fmt.Errorf("unsupported context case %q", caseID)
	}
}

func contextPairID(caseID string, seed uint32, repetition int) string {
	prefix := "c1-smoke"
	if caseID == "C2-smoke" {
		prefix = "c2-smoke"
	} else if caseID == "C3-smoke" {
		prefix = "c3-smoke"
	}
	return fmt.Sprintf("%s-%d-%d", prefix, seed, repetition)
}

func makeContextTrialTrace(
	workload contextCaseWorkload,
	pairID string,
	repetition, order int,
	mode contextTrialMode,
	workloadHash, inputHash string,
	elapsed time.Duration,
	trial contextTrialData,
) ContextTrialTrace {
	return ContextTrialTrace{
		SchemaVersion: contextTrialTraceSchemaV2, EvidenceScope: "windows-functional", PlanID: contextPlanID,
		CaseID: workload.CaseID, PairID: pairID, Seed: workload.Seed, Repetition: repetition,
		TrialOrder: order, Treatment: mode.treatment, Role: mode.role, CounterName: contextCounterName,
		ContextMode: mode.mode, ProviderCacheMode: contextProviderCacheMode,
		WorkloadSHA256: workloadHash, InputSHA256: inputHash, Status: "SUCCEEDED",
		ComparisonBudget: workload.ComparisonBudget, SummarizerMode: mode.summarizer, ElapsedNS: elapsed.Nanoseconds(),
		ManifestOperations: trial.manifestOperations, ManifestConflicts: trial.manifestConflicts,
		Stores: trial.stores, Dispatches: trial.dispatches, ACLProbes: trial.aclProbes,
	}
}

func contextRowFromTrace(trace ContextTrialTrace) (ContextRow, error) {
	row := ContextRow{
		SchemaVersion: contextSchemaV2, RunID: trace.RunID, Status: trace.Status, EvidenceScope: trace.EvidenceScope,
		CaseID: trace.CaseID, PairID: trace.PairID, Seed: trace.Seed, Repetition: trace.Repetition,
		TrialOrder: trace.TrialOrder, Treatment: trace.Treatment, Role: trace.Role,
		CounterName: trace.CounterName, ContextMode: trace.ContextMode, ProviderCacheMode: trace.ProviderCacheMode,
		WorkloadSHA256: trace.WorkloadSHA256, ElapsedMS: float64(trace.ElapsedNS) / float64(time.Millisecond),
		ManifestOperations: trace.ManifestOperations, ManifestConflicts: trace.ManifestConflicts,
		TimeoutMS: trialTimeoutMS, ReasonCode: "NONE",
	}
	for _, store := range trace.Stores {
		row.PageCount += store.UniquePages
		row.StorageLogicalTokens += store.LogicalTokens
		row.StoragePhysicalTokens += store.PhysicalTokens
		row.StorageReusedTokens += store.ReusedTokens
	}
	for _, dispatch := range trace.Dispatches {
		row.DroppedTokens += dispatch.DroppedTokens
		row.SummaryPages += dispatch.SummaryPages
		row.SummarizedSourceTokens += dispatch.SummarizedSourceTokens
		row.SummaryTokens += dispatch.SummaryTokens
		row.DispatchInputTokens += dispatch.AssembledTokens
		if dispatch.AssembledTokens > trace.ComparisonBudget {
			row.TargetBudgetExceedances++
		}
	}
	if err := validateContextTraceAndRow(trace, row); err != nil {
		return ContextRow{}, err
	}
	return row, nil
}
