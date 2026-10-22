package benchmark

import (
	"encoding/csv"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildFaultKernelEvidenceEmitsStrictSmokeArtifacts(t *testing.T) {
	plan, err := NewFaultKernelSmokePlan([]uint32{7}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	bundle := faultKernelTestBundle(t, plan)
	evidence, err := buildFaultKernelExperimentEvidence(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Kind != FaultKernelKind || evidence.PlanID != plan.PlanID ||
		evidence.EvidenceScope != FaultKernelEvidenceScope || evidence.RowCount != 2 ||
		evidence.Protocol.WarmupPairs != 0 || evidence.Protocol.MeasuredPairs != 1 {
		t.Fatalf("evidence identity = %#v", evidence)
	}
	wantArtifacts := map[string]string{
		"fault-kernel.csv":                "fault-kernel-csv",
		"fault-kernel-trial-traces.jsonl": "fault-kernel-trial-trace-jsonl",
		"cgroup-samples.jsonl":            "cgroup-sample-jsonl",
		"workload.json":                   "workload-json",
		"fault-kernel-input.json":         "fault-kernel-input-json",
		"components.json":                 "component-manifest-json",
	}
	for _, artifact := range evidence.Artifacts {
		kind, exists := wantArtifacts[artifact.Name]
		if !exists || artifact.Kind != kind {
			t.Fatalf("unexpected artifact %q/%q", artifact.Name, artifact.Kind)
		}
		delete(wantArtifacts, artifact.Name)
	}
	if len(wantArtifacts) != 0 {
		t.Fatalf("missing artifacts: %v", wantArtifacts)
	}
	if findFaultKernelArtifact(t, evidence, "fault-kernel-warmup-traces.jsonl") != nil {
		t.Fatal("smoke evidence emitted a warmup artifact")
	}
	if hashBytes(findFaultKernelArtifact(t, evidence, "workload.json").Data) != evidence.WorkloadSHA256 ||
		hashBytes(findFaultKernelArtifact(t, evidence, "fault-kernel-input.json").Data) != evidence.InputSHA256 ||
		hashBytes(findFaultKernelArtifact(t, evidence, "components.json").Data) != bundle.Input.ComponentManifestSHA256 {
		t.Fatal("published canonical artifacts do not reproduce their bound hashes")
	}

	rows, err := csv.NewReader(strings.NewReader(string(findFaultKernelArtifact(t, evidence, "fault-kernel.csv").Data))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || strings.Join(rows[0], ",") != faultKernelCSVHeader {
		t.Fatalf("CSV shape = %d rows, header %q", len(rows), strings.Join(rows[0], ","))
	}
	columns := faultKernelColumnIndexes(rows[0])
	if rows[1][columns["schema_version"]] != FaultKernelCSVSchemaV1 || len(rows[1]) != len(rows[0]) || len(rows[2]) != len(rows[0]) {
		t.Fatalf("CSV row schema or width is invalid: %v", rows)
	}
	if rows[1][columns["analysis_eligible"]] != "true" || rows[2][columns["analysis_eligible"]] != "true" {
		t.Fatalf("valid pair was excluded from analysis: %v", rows)
	}
	if rows[1][columns["daemon_exit_ms"]] != "" || rows[1][columns["retry_start_ms"]] != "" || rows[1][columns["recovery_ms"]] != "" {
		t.Fatalf("baseline missing metrics were not empty: %v", rows[1])
	}
	if rows[2][columns["daemon_exit_ms"]] != "10.000000" || rows[2][columns["cleanup_ms"]] != "20.000000" ||
		rows[2][columns["retry_start_ms"]] != "40.000000" || rows[2][columns["recovery_ms"]] != "50.000000" {
		t.Fatalf("treatment metrics were not recomputed: %v", rows[2])
	}
	if rows[1][columns["duplicate_artifacts"]] != "0" || rows[2][columns["duplicate_artifacts"]] != "0" {
		t.Fatalf("observed zero duplicate-artifact counts were not emitted: %v", rows)
	}
}

func TestBuildFaultKernelEvidenceExcludesWholePairFromAnalysisWhenOneRowIsInvalid(t *testing.T) {
	plan, err := NewFaultKernelSmokePlan([]uint32{7}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	bundle := faultKernelTestBundle(t, plan)
	invalid := &bundle.Trials[1].Trace
	invalid.Status, invalid.ReasonCode = "INVALID", "HARNESS_FAILURE"
	invalid.StartupReceipt = nil
	invalid.Target = FaultKernelTargetTrace{
		AttemptBefore: 1, AttemptAfter: 1, FinalStatus: "FAILED",
		Attempts: []FaultKernelAttemptTrace{{
			Number: 1, ReadyObserved: true, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
		}},
	}
	invalid.Milestones.Attempt2ReadyNS, invalid.Milestones.RecoveredNS = nil, nil
	invalid.Observations = FaultKernelObservations{
		DaemonSIGKILLObserved: true, OrphanObservedBeforeRestart: true, RestartReady: true,
	}
	samples := bundle.Trials[1].Samples
	terminal := samples[len(samples)-1]
	terminal.AttemptNumber = 1
	bundle.Trials[1].Samples = []FaultKernelCgroupSample{samples[0], samples[1], terminal}
	invalid.CgroupSampleSequences = []int{
		samples[0].SampleSequence, samples[1].SampleSequence, terminal.SampleSequence,
	}
	evidence, err := buildFaultKernelExperimentEvidence(bundle)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(
		findFaultKernelArtifact(t, evidence, "fault-kernel.csv").Data,
	))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	columns := faultKernelColumnIndexes(rows[0])
	if rows[1][columns["status"]] != "SUCCEEDED" || rows[2][columns["status"]] != "INVALID" ||
		rows[1][columns["analysis_eligible"]] != "false" || rows[2][columns["analysis_eligible"]] != "false" {
		t.Fatalf("asymmetric invalid pair was not excluded pairwise: %v", rows)
	}
}

func TestBuildFaultKernelEvidenceRejectsUnvalidatedSamplesAndHashDrift(t *testing.T) {
	plan, err := NewFaultKernelSmokePlan([]uint32{7}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	bundle := faultKernelTestBundle(t, plan)
	bundle.Trials[1].Samples[1].MemberCount = intPointer(-1)
	if _, err := buildFaultKernelExperimentEvidence(bundle); err == nil {
		t.Fatal("invalid cgroup sample was serialized")
	}
	bundle = faultKernelTestBundle(t, plan)
	bundle.Input.WorkloadSHA256 = strings.Repeat("f", 64)
	if _, err := buildFaultKernelExperimentEvidence(bundle); err == nil {
		t.Fatal("workload hash drift was accepted")
	}
}

func TestBuildFaultKernelEvidenceRetainsFailedTrialInCompleteMatrix(t *testing.T) {
	plan, err := NewFaultKernelSmokePlan([]uint32{7}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	bundle := faultKernelTestBundle(t, plan)
	failed := &bundle.Trials[0].Trace
	failed.Status, failed.ReasonCode = "FAILED", "HARNESS_FAILURE"
	failed.Target.FinalStatus = "FAILED"
	failed.Target.Attempts[0].FinalStatus = "FAILED"
	failed.Target.Attempts[0].ArtifactObservations = 0
	failed.Target.Attempts[0].ReasonCode = "RUNTIME_INTERRUPTED"
	failed.Observations.DuplicateArtifactsObserved = false
	evidence, err := buildFaultKernelExperimentEvidence(bundle)
	if err != nil {
		t.Fatalf("complete matrix with failed F4 trial rejected: %v", err)
	}
	traceArtifact := findFaultKernelArtifact(t, evidence, "fault-kernel-trial-traces.jsonl")
	csvArtifact := findFaultKernelArtifact(t, evidence, "fault-kernel.csv")
	if traceArtifact == nil || !strings.Contains(string(traceArtifact.Data), `"status":"FAILED"`) ||
		csvArtifact == nil || !strings.Contains(string(csvArtifact.Data), ",FAILED,") {
		t.Fatal("failed F4 trial was not retained in trace and CSV artifacts")
	}
	rows, err := csv.NewReader(strings.NewReader(string(csvArtifact.Data))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	columns := faultKernelColumnIndexes(rows[0])
	if rows[1][columns["target_final_status"]] != "FAILED" ||
		rows[1][columns["duplicate_artifacts"]] != "" {
		t.Fatalf("failed row did not preserve terminal target or unknown duplicate count: %v", rows[1])
	}
}

func TestBuildFaultKernelEvidenceRejectsFailedHarnessWithSuccessfulTarget(t *testing.T) {
	plan, err := NewFaultKernelSmokePlan([]uint32{7}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	bundle := faultKernelTestBundle(t, plan)
	bundle.Trials[0].Trace.Status = "FAILED"
	bundle.Trials[0].Trace.ReasonCode = "HARNESS_FAILURE"
	if _, err := buildFaultKernelExperimentEvidence(bundle); err == nil {
		t.Fatal("successful target forged as FAILED/HARNESS_FAILURE was serialized")
	}
}

func TestBuildFaultKernelEvidenceRejectsFailedTrialWithoutTerminalAbsence(t *testing.T) {
	plan, err := NewFaultKernelSmokePlan([]uint32{7}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	bundle := faultKernelTestBundle(t, plan)
	failed := &bundle.Trials[0]
	failed.Trace.Status, failed.Trace.ReasonCode = "FAILED", "HARNESS_FAILURE"
	failed.Trace.Target.FinalStatus = "FAILED"
	failed.Trace.Target.Attempts[0].FinalStatus = "FAILED"
	failed.Trace.Target.Attempts[0].ArtifactObservations = 0
	failed.Trace.Target.Attempts[0].ReasonCode = "RUNTIME_INTERRUPTED"
	failed.Trace.Observations.DuplicateArtifactsObserved = false
	failed.Samples[1] = cgroupSample(failed.Trace, 2, "trial_terminal", 40_000_000, 1, true, true, 2)
	failed.Samples[1].PairID, failed.Samples[1].TrialOrder = failed.Trace.PairID, failed.Trace.TrialOrder
	if _, err := buildFaultKernelExperimentEvidence(bundle); err == nil {
		t.Fatal("failed trial without terminal cgroup absence was serialized")
	}
}

func faultKernelTestBundle(t *testing.T, plan FaultKernelPlan) faultKernelEvidenceBundle {
	t.Helper()
	workload, _, err := newFaultKernelWorkload(plan)
	if err != nil {
		t.Fatal(err)
	}
	workloadJSON, err := canonicalJSON(workload)
	if err != nil {
		t.Fatal(err)
	}
	components := validFaultKernelComponents()
	componentJSON, err := canonicalJSON(components)
	if err != nil {
		t.Fatal(err)
	}
	configHash, err := faultKernelRuntimeConfigHash(workload.RuntimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewFaultKernelInput(plan, hashBytes(workloadJSON), hashBytes(componentJSON), configHash)
	if err != nil {
		t.Fatal(err)
	}
	inputJSON, err := canonicalJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	inputHash := hashBytes(inputJSON)
	trials := make([]faultKernelExecutedTrial, 0, (len(plan.Protocol.WarmupSeeds)+len(plan.Protocol.MeasuredSeeds))*plan.Protocol.Repetitions*2)
	clock := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	appendPhase := func(phase string, seeds []uint32) {
		for seedIndex, seed := range seeds {
			for repetition := 0; repetition < plan.Protocol.Repetitions; repetition++ {
				pairID := "f4-" + phase + "-" + strings.ToLower(plan.Profile) + "-" + strconv.Itoa(seedIndex) + "-" + strconv.Itoa(repetition)
				baseline, baselineSamples := validFaultKernelBaselineEvidence()
				treatment, treatmentSamples := validFaultKernelTreatmentEvidence()
				for _, item := range []struct {
					trace   *FaultKernelTrialTrace
					samples []FaultKernelCgroupSample
					order   int
				}{{&baseline, baselineSamples, seedIndex % 2}, {&treatment, treatmentSamples, 1 - seedIndex%2}} {
					item.trace.Profile, item.trace.PlanID, item.trace.CaseID = plan.Profile, plan.PlanID, plan.CaseID
					item.trace.PairID, item.trace.Seed, item.trace.Repetition, item.trace.TrialOrder = pairID, seed, repetition, item.order
					item.trace.Phase, item.trace.WorkloadSHA256, item.trace.InputSHA256 = phase, input.WorkloadSHA256, inputHash
					item.trace.TimeoutMS = plan.Protocol.TrialTimeoutMS
					for index := range item.samples {
						item.samples[index].PairID, item.samples[index].TrialOrder = pairID, item.order
						item.samples[index].Phase = phase
					}
					trials = append(trials, faultKernelExecutedTrial{Trace: *item.trace, Samples: item.samples, StartedAt: clock, FinishedAt: clock.Add(100 * time.Millisecond)})
					clock = clock.Add(time.Second)
				}
			}
		}
	}
	appendPhase("warmup", plan.Protocol.WarmupSeeds)
	appendPhase("measurement", plan.Protocol.MeasuredSeeds)
	return faultKernelEvidenceBundle{RunID: "run-0123456789abcdef01234567", Plan: plan, Input: input,
		WorkloadJSON: workloadJSON, Components: components, Trials: trials}
}

func findFaultKernelArtifact(t *testing.T, evidence experimentEvidence, name string) *artifactPayload {
	t.Helper()
	for index := range evidence.Artifacts {
		if evidence.Artifacts[index].Name == name {
			return &evidence.Artifacts[index]
		}
	}
	return nil
}

func faultKernelColumnIndexes(header []string) map[string]int {
	result := make(map[string]int, len(header))
	for index, name := range header {
		result[name] = index
	}
	return result
}
