package benchmark

import (
	"bytes"
	"context"
	"fmt"
	"time"
)

func CollectContext(ctx context.Context, options ContextCollectOptions) (RunSummary, error) {
	return collectContext(ctx, options, defaultCollectorDeps())
}

func collectContext(ctx context.Context, options ContextCollectOptions, deps collectorDeps) (RunSummary, error) {
	return collectExperiment(ctx, options.OutputDir, deps, func(
		ctx context.Context,
		runID string,
		now func() time.Time,
	) (experimentEvidence, error) {
		return buildContextEvidence(ctx, runID, options.Plan, now)
	})
}

func buildContextEvidence(ctx context.Context, runID string, plan ContextPlan, now func() time.Time) (experimentEvidence, error) {
	evidence, err := simulateContextEvidenceContext(ctx, plan, now)
	if err != nil {
		return experimentEvidence{}, err
	}
	result, traces, err := prepareContextEvidence(runID, evidence.Result, evidence.TrialTraceRecords())
	if err != nil {
		return experimentEvidence{}, err
	}
	workloadJSON, inputJSON := evidence.CanonicalWorkloadJSON(), evidence.CanonicalInputJSON()
	if err := validateCanonicalContextInputs(result, workloadJSON, inputJSON); err != nil {
		return experimentEvidence{}, err
	}
	if err := validateContextTracesAgainstWorkload(ctx, traces, workloadJSON); err != nil {
		return experimentEvidence{}, err
	}
	payloads, err := contextArtifactPayloads(result, traces, workloadJSON, inputJSON)
	if err != nil {
		return experimentEvidence{}, err
	}
	trials, err := contextTrialObservations(result)
	if err != nil {
		return experimentEvidence{}, err
	}
	return experimentEvidence{
		Kind: "context", PlanID: contextPlanID, EvidenceScope: "windows-functional",
		CaseIDs: []string{"C1-smoke", "C2-smoke", "C3-smoke"}, WorkloadSHA256: result.WorkloadSHA256, InputSHA256: result.InputSHA256,
		Protocol: contextManifestProtocol(plan), Artifacts: payloads, Trials: trials, RowCount: len(result.Rows),
	}, nil
}

func contextArtifactPayloads(
	result ContextResult,
	traces []ContextTrialTrace,
	workloadJSON, inputJSON []byte,
) ([]artifactPayload, error) {
	var csvBuffer bytes.Buffer
	if err := writeContextCSV(&csvBuffer, result.Rows); err != nil {
		return nil, err
	}
	traceData, err := marshalContextTrialTraces(traces)
	if err != nil {
		return nil, err
	}
	return []artifactPayload{
		{Name: "context.csv", Kind: "context-csv", MediaType: "text/csv", Records: len(result.Rows), Data: csvBuffer.Bytes()},
		{Name: "context-trial-traces.jsonl", Kind: "context-trial-trace-jsonl", MediaType: "application/x-ndjson", Records: len(traces), Data: traceData},
		{Name: "workload.json", Kind: "workload-json", MediaType: "application/json", Records: 1, Data: workloadJSON},
		{Name: "context-input.json", Kind: "context-input-json", MediaType: "application/json", Records: 1, Data: inputJSON},
	}, nil
}

func contextTrialObservations(result ContextResult) ([]trialObservation, error) {
	trials := make([]trialObservation, 0, len(result.traces))
	for index, trace := range result.traces {
		if trace.rowIndex < 0 || trace.rowIndex >= len(result.Rows) {
			return nil, fmt.Errorf("context trial trace %d has invalid row index", index)
		}
		row := result.Rows[trace.rowIndex]
		trials = append(trials, trialObservation{
			CaseID: row.CaseID, PairID: row.PairID, Seed: row.Seed, Repetition: row.Repetition,
			TrialOrder: row.TrialOrder, Treatment: row.Treatment, StartedAt: trace.started, FinishedAt: trace.finished,
		})
	}
	return trials, nil
}

func contextManifestProtocol(plan ContextPlan) ManifestProtocol {
	return ManifestProtocol{
		WarmupPairs: 0, MeasuredPairs: len(plan.Seeds) * plan.Repetitions * 3,
		Seeds: append([]uint32(nil), plan.Seeds...), Pairing: "seed-and-repetition-v1",
		Counterbalance: "alternating-seed-index-v1", TrialTimeoutMS: trialTimeoutMS,
		BootstrapSeed: 424242, BootstrapResamples: 10000,
	}
}
