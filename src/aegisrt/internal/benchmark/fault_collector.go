package benchmark

import (
	"bytes"
	"context"
	"fmt"
	"time"
)

func CollectFault(ctx context.Context, options FaultCollectOptions) (RunSummary, error) {
	return collectFault(ctx, options, defaultCollectorDeps())
}

func collectFault(ctx context.Context, options FaultCollectOptions, deps collectorDeps) (RunSummary, error) {
	return collectExperiment(ctx, options.OutputDir, deps, func(
		ctx context.Context,
		runID string,
		now func() time.Time,
	) (experimentEvidence, error) {
		return buildFaultEvidence(ctx, runID, options.Plan, options.Agent, now)
	})
}

func buildFaultEvidence(
	ctx context.Context,
	runID string,
	plan FaultPlan,
	command FaultAgentCommand,
	_ func() time.Time,
) (experimentEvidence, error) {
	evidence, err := SimulateFaultEvidence(ctx, plan, command)
	if err != nil {
		return experimentEvidence{}, err
	}
	result, traces, err := prepareFaultEvidence(runID, evidence.Result, evidence.TrialTraceRecords())
	if err != nil {
		return experimentEvidence{}, err
	}
	workloadJSON, inputJSON := evidence.CanonicalWorkloadJSON(), evidence.CanonicalInputJSON()
	if err := validateCanonicalFaultInputs(result, workloadJSON, inputJSON); err != nil {
		return experimentEvidence{}, err
	}
	if err := validateFaultTracesAgainstWorkload(ctx, traces, workloadJSON); err != nil {
		return experimentEvidence{}, err
	}
	payloads, err := faultArtifactPayloads(result, traces, workloadJSON, inputJSON)
	if err != nil {
		return experimentEvidence{}, err
	}
	trials, err := faultTrialObservations(result)
	if err != nil {
		return experimentEvidence{}, err
	}
	return experimentEvidence{
		Kind: "fault", PlanID: faultPlanID, EvidenceScope: "windows-functional",
		CaseIDs:        []string{"F1-smoke", "F2-smoke", "F3-service-smoke"},
		WorkloadSHA256: result.WorkloadSHA256, InputSHA256: result.InputSHA256,
		Protocol: faultManifestProtocol(plan), Artifacts: payloads, Trials: trials, RowCount: len(result.Rows),
	}, nil
}

func faultArtifactPayloads(
	result FaultResult,
	traces []FaultTrialTrace,
	workloadJSON, inputJSON []byte,
) ([]artifactPayload, error) {
	var csvBuffer bytes.Buffer
	if err := writeFaultCSV(&csvBuffer, result.Rows); err != nil {
		return nil, err
	}
	traceData, err := marshalFaultTrialTraces(traces)
	if err != nil {
		return nil, err
	}
	return []artifactPayload{
		{Name: "fault.csv", Kind: "fault-csv", MediaType: "text/csv", Records: len(result.Rows), Data: csvBuffer.Bytes()},
		{Name: "fault-trial-traces.jsonl", Kind: "fault-trial-trace-jsonl", MediaType: "application/x-ndjson", Records: len(traces), Data: traceData},
		{Name: "workload.json", Kind: "workload-json", MediaType: "application/json", Records: 1, Data: workloadJSON},
		{Name: "fault-input.json", Kind: "fault-input-json", MediaType: "application/json", Records: 1, Data: inputJSON},
	}, nil
}

func faultTrialObservations(result FaultResult) ([]trialObservation, error) {
	trials := make([]trialObservation, 0, len(result.traces))
	for index, trace := range result.traces {
		if trace.rowIndex < 0 || trace.rowIndex >= len(result.Rows) {
			return nil, fmt.Errorf("fault trial trace %d has invalid row index", index)
		}
		row := result.Rows[trace.rowIndex]
		trials = append(trials, trialObservation{
			CaseID: row.CaseID, PairID: row.PairID, Seed: row.Seed, Repetition: row.Repetition,
			TrialOrder: row.TrialOrder, Treatment: row.Treatment,
			StartedAt: trace.started, FinishedAt: trace.finished,
		})
	}
	return trials, nil
}

func faultManifestProtocol(plan FaultPlan) ManifestProtocol {
	return ManifestProtocol{
		WarmupPairs: 0, MeasuredPairs: len(plan.Seeds) * plan.Repetitions * 3,
		Seeds: append([]uint32(nil), plan.Seeds...), Pairing: "seed-and-repetition-v1",
		Counterbalance: "alternating-seed-index-v1", TrialTimeoutMS: trialTimeoutMS,
		BootstrapSeed: 424242, BootstrapResamples: 10000,
	}
}
