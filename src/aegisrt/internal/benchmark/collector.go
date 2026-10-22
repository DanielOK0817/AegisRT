package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

type collectorDeps struct {
	now         func() time.Time
	runID       func() (string, error)
	environment func() (Environment, error)
	software    func(string) (Software, error)
	publish     func(string, string) error
}

func CollectScheduler(ctx context.Context, options CollectOptions) (RunSummary, error) {
	return collectScheduler(ctx, options, defaultCollectorDeps())
}

func collectScheduler(ctx context.Context, options CollectOptions, deps collectorDeps) (RunSummary, error) {
	return collectExperiment(ctx, options.OutputDir, deps, func(
		ctx context.Context,
		runID string,
		now func() time.Time,
	) (experimentEvidence, error) {
		return buildSchedulerEvidence(ctx, runID, options.Plan, now)
	})
}

func buildSchedulerEvidence(
	ctx context.Context,
	runID string,
	plan SchedulerPlan,
	now func() time.Time,
) (experimentEvidence, error) {
	profile, err := resolveSchedulerProfile(plan)
	if err != nil {
		return experimentEvidence{}, err
	}
	evidence, err := simulateSchedulerEvidenceContext(ctx, plan, now)
	if err != nil {
		return experimentEvidence{}, err
	}
	result := evidence.Result
	for index := range result.Rows {
		result.Rows[index].RunID = runID
	}
	if err := validateSchedulerRows(result.Rows); err != nil {
		return experimentEvidence{}, err
	}
	workloadJSON, inputJSON := evidence.CanonicalWorkloadJSON(), evidence.CanonicalInputJSON()
	if err := validateCanonicalSchedulerInputs(result, workloadJSON, inputJSON); err != nil {
		return experimentEvidence{}, err
	}
	taskTraces, err := prepareSchedulerTaskTraces(runID, result, evidence.TaskTraceRecords())
	if err != nil {
		return experimentEvidence{}, err
	}
	payloads, err := schedulerArtifactPayloads(result, taskTraces, workloadJSON, inputJSON)
	if err != nil {
		return experimentEvidence{}, err
	}
	if profile.Name == SchedulerProfileFormal {
		analysis, analysisErr := analyzeSchedulerRows(profile.PlanID, result.Rows)
		if analysisErr != nil {
			return experimentEvidence{}, analysisErr
		}
		if analysisErr = validateFormalSchedulerAnalysis(analysis); analysisErr != nil {
			return experimentEvidence{}, analysisErr
		}
		analysisData, marshalErr := marshalJSON(analysis)
		if marshalErr != nil {
			return experimentEvidence{}, marshalErr
		}
		payloads = append(payloads, artifactPayload{
			Name: "scheduler-analysis.json", Kind: "scheduler-analysis-json", MediaType: "application/json", Records: len(analysis.Cells), Data: analysisData,
		})
	}
	trials, err := schedulerTrialObservations(result)
	if err != nil {
		return experimentEvidence{}, err
	}
	trials = append(append([]trialObservation(nil), evidence.warmupTrials...), trials...)
	caseIDs := make([]string, 0, len(profile.Cases))
	for _, definition := range profile.Cases {
		caseIDs = append(caseIDs, definition.ID)
	}
	return experimentEvidence{
		Kind: "scheduler", PlanID: profile.PlanID, EvidenceScope: "windows-functional",
		CaseIDs: caseIDs, WorkloadSHA256: result.WorkloadSHA256, InputSHA256: result.InputSHA256,
		Protocol: schedulerManifestProtocol(plan, profile), Artifacts: payloads, Trials: trials, RowCount: len(result.Rows),
	}, nil
}

func retainedStagingError(staging string, err error) error {
	return fmt.Errorf("collection failed; staging retained as %s: %w", filepath.Base(staging), err)
}

func schedulerArtifactPayloads(
	result SchedulerResult,
	taskTraces []SchedulerTaskTrace,
	workloadJSON, inputJSON []byte,
) ([]artifactPayload, error) {
	var schedulerBuffer bytes.Buffer
	if err := writeSchedulerCSV(&schedulerBuffer, result.Rows); err != nil {
		return nil, err
	}
	taskTraceData, err := marshalSchedulerTaskTraces(taskTraces)
	if err != nil {
		return nil, err
	}
	return []artifactPayload{
		{Name: "scheduler.csv", Kind: "scheduler-csv", MediaType: "text/csv", Records: len(result.Rows), Data: schedulerBuffer.Bytes()},
		{Name: "scheduler-task-traces.jsonl", Kind: "scheduler-task-trace-jsonl", MediaType: "application/x-ndjson", Records: len(taskTraces), Data: taskTraceData},
		{Name: "workload.json", Kind: "workload-json", MediaType: "application/json", Records: 1, Data: workloadJSON},
		{Name: "scheduler-input.json", Kind: "scheduler-input-json", MediaType: "application/json", Records: 1, Data: inputJSON},
	}, nil
}

func schedulerTrialObservations(result SchedulerResult) ([]trialObservation, error) {
	trials := make([]trialObservation, 0, len(result.traces))
	for index, trace := range result.traces {
		if trace.rowIndex < 0 || trace.rowIndex >= len(result.Rows) {
			return nil, fmt.Errorf("scheduler trial trace %d has invalid row index", index)
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

func schedulerManifestProtocol(plan SchedulerPlan, profile schedulerProfile) ManifestProtocol {
	pairsPerSeed := 0
	for _, definition := range profile.Cases {
		pairsPerSeed += len(definition.Treatments)
	}
	return ManifestProtocol{
		WarmupPairs:   len(profile.WarmupSeeds) * plan.Repetitions * pairsPerSeed,
		MeasuredPairs: len(plan.Seeds) * plan.Repetitions * pairsPerSeed,
		Seeds:         append([]uint32(nil), plan.Seeds...), Pairing: "seed-and-repetition-v1",
		Counterbalance: "alternating-seed-index-v1", TrialTimeoutMS: trialTimeoutMS,
		BootstrapSeed: 424242, BootstrapResamples: 10000,
	}
}

func appendArtifactData(data [][]byte, extra []byte) [][]byte {
	result := append([][]byte(nil), data...)
	return append(result, extra)
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
