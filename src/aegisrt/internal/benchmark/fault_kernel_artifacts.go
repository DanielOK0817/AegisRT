package benchmark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

func buildFaultKernelExperimentEvidence(bundle faultKernelEvidenceBundle) (experimentEvidence, error) {
	if err := ValidateFaultKernelPlan(bundle.Plan); err != nil {
		return experimentEvidence{}, err
	}
	if err := ValidateFaultKernelInput(bundle.Input); err != nil {
		return experimentEvidence{}, err
	}
	if !faultKernelInputMatchesPlan(bundle.Input, bundle.Plan) {
		return experimentEvidence{}, fmt.Errorf("fault-kernel input does not match plan")
	}
	if err := ValidateFaultKernelComponentManifest(bundle.Components); err != nil {
		return experimentEvidence{}, err
	}
	componentJSON, err := canonicalJSON(bundle.Components)
	if err != nil {
		return experimentEvidence{}, err
	}
	inputJSON, err := canonicalJSON(bundle.Input)
	if err != nil {
		return experimentEvidence{}, err
	}
	inputHash := hashBytes(inputJSON)
	if err := validateFaultKernelCanonicalBindings(bundle, componentJSON); err != nil {
		return experimentEvidence{}, err
	}
	prepared, err := prepareFaultKernelTrials(bundle.RunID, bundle.Trials)
	if err != nil {
		return experimentEvidence{}, err
	}
	if err := validateFaultKernelTrialMatrix(bundle.Plan, bundle.Input, inputHash, prepared); err != nil {
		return experimentEvidence{}, err
	}
	measured, warmup, samples, observations := splitFaultKernelTrials(prepared)
	payloads, err := faultKernelArtifactPayloads(measured, warmup, samples,
		bundle.WorkloadJSON, inputJSON, componentJSON, bundle.Plan.Profile)
	if err != nil {
		return experimentEvidence{}, err
	}
	evidence := experimentEvidence{
		Kind: FaultKernelKind, PlanID: bundle.Plan.PlanID, EvidenceScope: FaultKernelEvidenceScope,
		CaseIDs: []string{bundle.Plan.CaseID}, WorkloadSHA256: bundle.Input.WorkloadSHA256, InputSHA256: inputHash,
		Protocol: faultKernelManifestProtocol(bundle.Plan), Artifacts: payloads, Trials: observations, RowCount: len(measured),
	}
	if err := validateArtifactPayloads(evidence.Artifacts); err != nil {
		return experimentEvidence{}, err
	}
	if err := validateExperimentEvidence(evidence); err != nil {
		return experimentEvidence{}, err
	}
	return evidence, nil
}

func validateFaultKernelCanonicalBindings(bundle faultKernelEvidenceBundle, componentJSON []byte) error {
	if len(bundle.WorkloadJSON) == 0 || !json.Valid(bundle.WorkloadJSON) ||
		hashBytes(bundle.WorkloadJSON) != bundle.Input.WorkloadSHA256 {
		return fmt.Errorf("canonical fault-kernel workload does not reproduce its hash")
	}
	var workload faultKernelWorkload
	if err := json.Unmarshal(bundle.WorkloadJSON, &workload); err != nil {
		return fmt.Errorf("decode canonical fault-kernel workload: %w", err)
	}
	declaredJSON, err := canonicalJSON(workload)
	if err != nil {
		return err
	}
	if !bytes.Equal(bundle.WorkloadJSON, declaredJSON) {
		return fmt.Errorf("fault-kernel workload is not canonical")
	}
	configHash, err := faultKernelRuntimeConfigHash(workload.RuntimeConfig)
	if err != nil {
		return err
	}
	if configHash != bundle.Input.RuntimeConfigSHA256 {
		return fmt.Errorf("fault-kernel runtime config does not reproduce its hash")
	}
	expected, _, err := newFaultKernelWorkload(bundle.Plan)
	if err != nil {
		return err
	}
	expectedJSON, err := canonicalJSON(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(declaredJSON, expectedJSON) {
		return fmt.Errorf("fault-kernel workload does not match plan")
	}
	if hashBytes(componentJSON) != bundle.Input.ComponentManifestSHA256 {
		return fmt.Errorf("canonical fault-kernel component manifest does not reproduce its hash")
	}
	inputJSON, err := canonicalJSON(bundle.Input)
	if err != nil {
		return err
	}
	if err := scanAnonymous(bundle.WorkloadJSON, componentJSON, inputJSON); err != nil {
		return fmt.Errorf("fault-kernel canonical artifacts are not anonymous: %w", err)
	}
	return nil
}

func faultKernelInputMatchesPlan(input FaultKernelInput, plan FaultKernelPlan) bool {
	p, want := input.Protocol, plan.Protocol
	return input.Profile == plan.Profile && input.PlanID == plan.PlanID && input.CaseID == plan.CaseID &&
		p.Repetitions == want.Repetitions && p.Pairing == want.Pairing && p.Counterbalance == want.Counterbalance &&
		p.TrialTimeoutMS == want.TrialTimeoutMS && p.BootstrapSeed == want.BootstrapSeed &&
		p.BootstrapResamples == want.BootstrapResamples && slices.Equal(p.WarmupSeeds, want.WarmupSeeds) &&
		slices.Equal(p.MeasuredSeeds, want.MeasuredSeeds)
}

func validateFaultKernelTrialMatrix(plan FaultKernelPlan, input FaultKernelInput, inputHash string, trials []faultKernelExecutedTrial) error {
	expected := faultKernelExpectedCells(plan)
	groups := make(map[string][]FaultKernelTrialTrace, len(expected))
	pairCells := make(map[string]string, len(expected))
	for index, executed := range trials {
		trace := executed.Trace
		if trace.Profile != plan.Profile || trace.PlanID != plan.PlanID || trace.CaseID != plan.CaseID ||
			trace.WorkloadSHA256 != input.WorkloadSHA256 || trace.InputSHA256 != inputHash ||
			trace.TimeoutMS != plan.Protocol.TrialTimeoutMS {
			return fmt.Errorf("fault-kernel trial %d changed a controlled input", index)
		}
		key := faultKernelCellKey(trace.Phase, trace.Seed, trace.Repetition)
		seedIndex, exists := expected[key]
		if !exists {
			return fmt.Errorf("fault-kernel trial %d is outside the plan", index)
		}
		if prior, exists := pairCells[trace.PairID]; exists && prior != key {
			return fmt.Errorf("fault-kernel pair id is reused across cells")
		}
		pairCells[trace.PairID] = key
		groups[key] = append(groups[key], trace)
		if trace.Role == "baseline" && trace.TrialOrder != seedIndex%2 ||
			trace.Role == "treatment" && trace.TrialOrder != 1-seedIndex%2 {
			return fmt.Errorf("fault-kernel trial %d violates counterbalance", index)
		}
	}
	if len(groups) != len(expected) {
		return fmt.Errorf("fault-kernel trial matrix is incomplete")
	}
	for key := range expected {
		if err := validateFaultKernelPair(key, groups[key]); err != nil {
			return err
		}
	}
	return nil
}

func validateFaultKernelPair(key string, pair []FaultKernelTrialTrace) error {
	if len(pair) != 2 {
		return fmt.Errorf("fault-kernel cell %q must contain two trials", key)
	}
	var baseline, treatment *FaultKernelTrialTrace
	for index := range pair {
		switch pair[index].Role {
		case "baseline":
			if baseline != nil {
				return fmt.Errorf("fault-kernel cell %q duplicates baseline", key)
			}
			baseline = &pair[index]
		case "treatment":
			if treatment != nil {
				return fmt.Errorf("fault-kernel cell %q duplicates treatment", key)
			}
			treatment = &pair[index]
		}
	}
	if baseline == nil || treatment == nil || baseline.PairID != treatment.PairID ||
		baseline.TrialOrder == treatment.TrialOrder || baseline.RunID != treatment.RunID ||
		baseline.Phase != treatment.Phase || baseline.Seed != treatment.Seed || baseline.Repetition != treatment.Repetition ||
		baseline.WorkloadSHA256 != treatment.WorkloadSHA256 || baseline.InputSHA256 != treatment.InputSHA256 ||
		baseline.TimeoutMS != treatment.TimeoutMS {
		return fmt.Errorf("fault-kernel cell %q has an invalid pair", key)
	}
	return nil
}

func faultKernelExpectedCells(plan FaultKernelPlan) map[string]int {
	expected := make(map[string]int)
	add := func(phase string, seeds []uint32) {
		for seedIndex, seed := range seeds {
			for repetition := 0; repetition < plan.Protocol.Repetitions; repetition++ {
				expected[faultKernelCellKey(phase, seed, repetition)] = seedIndex
			}
		}
	}
	add("warmup", plan.Protocol.WarmupSeeds)
	add("measurement", plan.Protocol.MeasuredSeeds)
	return expected
}

func faultKernelCellKey(phase string, seed uint32, repetition int) string {
	return fmt.Sprintf("%s/%d/%d", phase, seed, repetition)
}

func splitFaultKernelTrials(trials []faultKernelExecutedTrial) ([]FaultKernelTrialTrace, []FaultKernelTrialTrace, []FaultKernelCgroupSample, []trialObservation) {
	measured := make([]FaultKernelTrialTrace, 0, len(trials))
	warmup := make([]FaultKernelTrialTrace, 0)
	samples := make([]FaultKernelCgroupSample, 0)
	observations := make([]trialObservation, 0, len(trials))
	for _, executed := range trials {
		trace := executed.Trace
		if trace.Phase == "measurement" {
			measured = append(measured, trace)
		} else {
			warmup = append(warmup, trace)
		}
		samples = append(samples, executed.Samples...)
		observations = append(observations, trialObservation{Phase: trace.Phase, CaseID: trace.CaseID,
			Status: trace.Status,
			PairID: trace.PairID, Seed: trace.Seed, Repetition: trace.Repetition, TrialOrder: trace.TrialOrder,
			Treatment: trace.Treatment, StartedAt: executed.StartedAt, FinishedAt: executed.FinishedAt})
	}
	return measured, warmup, samples, observations
}

func faultKernelArtifactPayloads(measured, warmup []FaultKernelTrialTrace, samples []FaultKernelCgroupSample,
	workloadJSON, inputJSON, componentJSON []byte, profile string) ([]artifactPayload, error) {
	var csvBuffer bytes.Buffer
	if err := writeFaultKernelCSV(&csvBuffer, measured); err != nil {
		return nil, err
	}
	traceJSONL, err := marshalFaultKernelTraces(measured)
	if err != nil {
		return nil, err
	}
	sampleJSONL, err := marshalFaultKernelSamples(samples)
	if err != nil {
		return nil, err
	}
	payloads := []artifactPayload{
		{Name: "fault-kernel.csv", Kind: "fault-kernel-csv", MediaType: "text/csv", Records: len(measured), Data: csvBuffer.Bytes()},
		{Name: "fault-kernel-trial-traces.jsonl", Kind: "fault-kernel-trial-trace-jsonl", MediaType: "application/x-ndjson", Records: len(measured), Data: traceJSONL},
		{Name: "cgroup-samples.jsonl", Kind: "cgroup-sample-jsonl", MediaType: "application/x-ndjson", Records: len(samples), Data: sampleJSONL},
		{Name: "workload.json", Kind: "workload-json", MediaType: "application/json", Records: 1, Data: append([]byte(nil), workloadJSON...)},
		{Name: "fault-kernel-input.json", Kind: "fault-kernel-input-json", MediaType: "application/json", Records: 1, Data: inputJSON},
		{Name: "components.json", Kind: "component-manifest-json", MediaType: "application/json", Records: 1, Data: componentJSON},
	}
	if profile == FaultKernelProfileFormal {
		warmupJSONL, err := marshalFaultKernelTraces(warmup)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, artifactPayload{Name: "fault-kernel-warmup-traces.jsonl",
			Kind: "fault-kernel-warmup-trace-jsonl", MediaType: "application/x-ndjson", Records: len(warmup), Data: warmupJSONL})
	} else if len(warmup) != 0 {
		return nil, fmt.Errorf("fault-kernel smoke evidence contains warmup trials")
	}
	return payloads, nil
}

func faultKernelManifestProtocol(plan FaultKernelPlan) ManifestProtocol {
	return ManifestProtocol{WarmupPairs: len(plan.Protocol.WarmupSeeds) * plan.Protocol.Repetitions,
		MeasuredPairs: len(plan.Protocol.MeasuredSeeds) * plan.Protocol.Repetitions,
		Seeds:         append([]uint32(nil), plan.Protocol.MeasuredSeeds...), Pairing: plan.Protocol.Pairing,
		Counterbalance: plan.Protocol.Counterbalance, TrialTimeoutMS: plan.Protocol.TrialTimeoutMS,
		BootstrapSeed: plan.Protocol.BootstrapSeed, BootstrapResamples: plan.Protocol.BootstrapResamples}
}
