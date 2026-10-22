package benchmark

import (
	"reflect"
	"strings"
	"testing"
)

func TestFaultKernelEvidenceBindsTraceAndSampleIdentityAndSequence(t *testing.T) {
	trace, samples := validFaultKernelTreatmentEvidence()
	if err := ValidateFaultKernelEvidence(trace, samples); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*FaultKernelTrialTrace, []FaultKernelCgroupSample)
	}{
		{name: "run identity", mutate: func(_ *FaultKernelTrialTrace, values []FaultKernelCgroupSample) {
			values[0].RunID = "run-aaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{name: "phase identity", mutate: func(_ *FaultKernelTrialTrace, values []FaultKernelCgroupSample) { values[0].Phase = "warmup" }},
		{name: "sequence reference", mutate: func(value *FaultKernelTrialTrace, _ []FaultKernelCgroupSample) { value.CgroupSampleSequences[1] = 99 }},
		{name: "sample order", mutate: func(_ *FaultKernelTrialTrace, values []FaultKernelCgroupSample) {
			values[1].MonotonicNS = values[0].MonotonicNS - 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			traceCopy := cloneFaultKernelTrace(trace)
			sampleCopy := append([]FaultKernelCgroupSample(nil), samples...)
			test.mutate(&traceCopy, sampleCopy)
			if err := ValidateFaultKernelEvidence(traceCopy, sampleCopy); err == nil {
				t.Fatal("inconsistent evidence was accepted")
			}
		})
	}
}

func TestFaultKernelSuccessfulRoleGates(t *testing.T) {
	baseline, samples := validFaultKernelBaselineEvidence()
	if err := ValidateFaultKernelEvidence(baseline, samples); err != nil {
		t.Fatalf("valid baseline rejected: %v", err)
	}
	invalidBaseline := cloneFaultKernelTrace(baseline)
	invalidBaseline.Observations.DaemonSIGKILLObserved = true
	if err := ValidateFaultKernelEvidence(invalidBaseline, samples); err == nil {
		t.Fatal("baseline claiming SIGKILL was accepted")
	}

	treatment, treatmentSamples := validFaultKernelTreatmentEvidence()
	invalidTreatment := cloneFaultKernelTrace(treatment)
	invalidTreatment.StartupReceipt = nil
	if err := ValidateFaultKernelEvidence(invalidTreatment, treatmentSamples); err == nil {
		t.Fatal("treatment without recovery receipt was accepted")
	}
	treatmentSamples[1].Populated = boolPointer(false)
	if err := ValidateFaultKernelEvidence(treatment, treatmentSamples); err == nil {
		t.Fatal("treatment without populated old cgroup proof was accepted")
	}
}

func TestFaultKernelTraceCellsStayWithinTheirProfile(t *testing.T) {
	smoke, _ := validFaultKernelBaselineEvidence()
	smoke.Phase = "warmup"
	if err := ValidateFaultKernelTrace(smoke); err == nil {
		t.Fatal("smoke trace accepted a warmup cell")
	}

	formal, _ := validFaultKernelBaselineEvidence()
	formal.Profile, formal.PlanID, formal.CaseID = FaultKernelProfileFormal, FaultKernelFormalPlanID, FaultKernelFormalCaseID
	formal.Seed = FaultKernelFormalMeasuredSeeds()[0]
	if err := ValidateFaultKernelTrace(formal); err != nil {
		t.Fatalf("fixed formal measurement rejected: %v", err)
	}
	formal.Seed = 1
	if err := ValidateFaultKernelTrace(formal); err == nil {
		t.Fatal("formal trace accepted a seed outside its fixed phase")
	}
	formal.Status, formal.ReasonCode = "FAILED", "TYPO"
	if err := ValidateFaultKernelTrace(formal); err == nil {
		t.Fatal("trace accepted an unknown reason code")
	}
}

func TestFaultKernelTreatmentProofIsPostExitPreCleanupAndContained(t *testing.T) {
	trace, samples := validFaultKernelTreatmentEvidence()
	samples[1].MonotonicNS = *trace.Milestones.DaemonExitedNS - 1
	if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
		t.Fatal("pre-exit cgroup sample was accepted as post-exit proof")
	}
	_, samples = validFaultKernelTreatmentEvidence()
	samples[0].EscapedDescendantCount = 1
	if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
		t.Fatal("successful evidence with an isolation escape was accepted")
	}
}

func TestFaultKernelSuccessfulCgroupStagesAreExact(t *testing.T) {
	baseline, samples := validFaultKernelBaselineEvidence()
	samples[1].Stage = "attempt1_ready"
	if err := ValidateFaultKernelEvidence(baseline, samples); err == nil {
		t.Fatal("successful baseline accepted a repeated stage")
	}

	treatment, samples := validFaultKernelTreatmentEvidence()
	samples[2].Stage, samples[3].Stage = samples[3].Stage, samples[2].Stage
	if err := ValidateFaultKernelEvidence(treatment, samples); err == nil {
		t.Fatal("successful treatment accepted reordered stages")
	}
	treatment.CgroupSampleSequences = treatment.CgroupSampleSequences[:4]
	samples = samples[:4]
	if err := ValidateFaultKernelEvidence(treatment, samples); err == nil {
		t.Fatal("successful treatment accepted an incomplete stage set")
	}
}

func TestFaultKernelMilestonesAndCSVFormulasAreRecomputable(t *testing.T) {
	trace, _ := validFaultKernelTreatmentEvidence()
	metrics, err := RecomputeFaultKernelMetrics(trace)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.DaemonExitMS == nil || *metrics.DaemonExitMS != 10 ||
		metrics.CleanupMS == nil || *metrics.CleanupMS != 20 ||
		metrics.RetryStartMS == nil || *metrics.RetryStartMS != 40 ||
		metrics.RecoveryMS == nil || *metrics.RecoveryMS != 50 ||
		metrics.ElapsedMS != 80 || metrics.Retries != 1 {
		t.Fatalf("recomputed metrics = %#v", metrics)
	}
	if err := ValidateFaultKernelMetrics(trace, metrics); err != nil {
		t.Fatal(err)
	}
	drifted := metrics
	value := *metrics.CleanupMS + 1
	drifted.CleanupMS = &value
	if err := ValidateFaultKernelMetrics(trace, drifted); err == nil {
		t.Fatal("CSV metric drift was accepted")
	}
	unordered := cloneFaultKernelTrace(trace)
	*unordered.Milestones.CleanupCompleteNS = *unordered.Milestones.DaemonExitedNS - 1
	if _, err := RecomputeFaultKernelMetrics(unordered); err == nil {
		t.Fatal("non-monotonic milestones were accepted")
	}
}

func TestFaultKernelPublishedTypesHaveNoPIDOrPathFields(t *testing.T) {
	for _, value := range []any{
		FaultKernelInput{}, FaultKernelTrialTrace{}, FaultKernelCgroupSample{}, FaultKernelComponentManifest{},
	} {
		assertNoSensitiveIdentityFields(t, reflect.TypeOf(value))
	}
}

func assertNoSensitiveIdentityFields(t *testing.T, value reflect.Type) {
	t.Helper()
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Slice {
		assertNoSensitiveIdentityFields(t, value.Elem())
		return
	}
	if value.Kind() != reflect.Struct {
		return
	}
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		fieldName := strings.ToLower(field.Name)
		jsonName := strings.ToLower(strings.Split(field.Tag.Get("json"), ",")[0])
		exposesPID := fieldName == "pid" || strings.HasPrefix(fieldName, "pid") && !strings.HasPrefix(fieldName, "pids") ||
			jsonName == "pid" || strings.HasPrefix(jsonName, "pid_") || strings.HasSuffix(jsonName, "_pid")
		if exposesPID || strings.Contains(fieldName, "path") || strings.Contains(jsonName, "path") {
			t.Fatalf("published type %s exposes forbidden field %s", value, field.Name)
		}
		assertNoSensitiveIdentityFields(t, field.Type)
	}
}

func validFaultKernelBaselineEvidence() (FaultKernelTrialTrace, []FaultKernelCgroupSample) {
	cleanup := int64(40_000_000)
	trace := baseFaultKernelTrace()
	trace.Milestones = FaultKernelMilestones{StartedNS: 0, Attempt1ReadyNS: 10_000_000, CleanupCompleteNS: &cleanup, FinishedNS: 40_000_000}
	trace.Target = FaultKernelTargetTrace{AttemptBefore: 1, AttemptAfter: 1, Attempts: []FaultKernelAttemptTrace{{
		Number: 1, ReadyObserved: true, FinalStatus: "SUCCEEDED", ArtifactObservations: 1, ReasonCode: "NONE",
	}}, FinalStatus: "SUCCEEDED"}
	trace.Observations.DuplicateArtifactsObserved = true
	trace.CgroupSampleSequences = []int{1, 2}
	return trace, []FaultKernelCgroupSample{
		cgroupSample(trace, 1, "attempt1_ready", 10_000_000, 1, true, true, 2),
		cgroupSample(trace, 2, "trial_terminal", 40_000_000, 1, false, false, 0),
	}
}

func validFaultKernelTreatmentEvidence() (FaultKernelTrialTrace, []FaultKernelCgroupSample) {
	trace := baseFaultKernelTrace()
	trace.Treatment, trace.Role = "daemon-sigkill", "treatment"
	trace.Protocol = FaultKernelTraceProtocol{FaultMode: "daemon_sigkill", InjectionBoundary: "attempt1-ready-and-populated", IsolationMode: FaultKernelIsolationMode, DaemonControl: FaultKernelDaemonControl, ExpectedRecovery: "restart-cleanup-requeue-attempt2"}
	injected, exited, cleanup := int64(20_000_000), int64(30_000_000), int64(50_000_000)
	retry, recovered := int64(60_000_000), int64(70_000_000)
	trace.Milestones = FaultKernelMilestones{StartedNS: 0, Attempt1ReadyNS: 10_000_000, InjectedNS: &injected, DaemonExitedNS: &exited, CleanupCompleteNS: &cleanup, Attempt2ReadyNS: &retry, RecoveredNS: &recovered, FinishedNS: 80_000_000}
	trace.StartupReceipt = &FaultKernelStartupReceipt{SchemaVersion: 1, StateLoaded: true, RecoveryPerformed: true, RequeuedCount: 1}
	trace.Target = FaultKernelTargetTrace{AttemptBefore: 1, AttemptAfter: 2, Attempts: []FaultKernelAttemptTrace{
		{Number: 1, ReadyObserved: true, FinalStatus: "LOST", ReasonCode: "RUNTIME_INTERRUPTED"},
		{Number: 2, ReadyObserved: true, FinalStatus: "SUCCEEDED", ArtifactObservations: 1, ReasonCode: "NONE"},
	}, FinalStatus: "SUCCEEDED"}
	trace.CgroupSampleSequences = []int{1, 2, 3, 4, 5}
	trace.Observations = FaultKernelObservations{
		DaemonSIGKILLObserved: true, OrphanObservedBeforeRestart: true,
		CheckpointLoaded: true, RecoveryRequeued: true, RestartReady: true,
		OldProcessesAfterCleanupObserved: true, OldCgroupsAfterCleanupObserved: true,
		DuplicateArtifactsObserved: true,
	}
	return trace, []FaultKernelCgroupSample{
		cgroupSample(trace, 1, "attempt1_ready", 10_000_000, 1, true, true, 2),
		cgroupSample(trace, 2, "post_daemon_exit", 30_000_000, 1, true, true, 2),
		cgroupSample(trace, 3, "post_runtime_restart", 50_000_000, 1, false, false, 0),
		cgroupSample(trace, 4, "attempt2_ready", 60_000_000, 2, true, true, 2),
		cgroupSample(trace, 5, "trial_terminal", 80_000_000, 2, false, false, 0),
	}
}

func failedFaultKernelTreatmentBeforeRestartEvidence(escapes int) (
	FaultKernelTrialTrace, []FaultKernelCgroupSample,
) {
	trace, samples := validFaultKernelTreatmentEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "ISOLATION_ESCAPE"
	trace.StartupReceipt = nil
	trace.Milestones.Attempt2ReadyNS = nil
	trace.Milestones.RecoveredNS = nil
	trace.Target = FaultKernelTargetTrace{
		AttemptBefore: 1, AttemptAfter: 1, FinalStatus: "FAILED",
		Attempts: []FaultKernelAttemptTrace{{
			Number: 1, ReadyObserved: true, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
		}},
	}
	trace.Observations = FaultKernelObservations{
		DaemonSIGKILLObserved: true, IsolationEscapeCount: escapes,
	}
	terminal := samples[len(samples)-1]
	terminal.AttemptNumber = 1
	samples = []FaultKernelCgroupSample{samples[0], samples[1], terminal}
	samples[1].EscapedDescendantCount = escapes
	trace.CgroupSampleSequences = []int{
		samples[0].SampleSequence, samples[1].SampleSequence, samples[2].SampleSequence,
	}
	return trace, samples
}

func baseFaultKernelTrace() FaultKernelTrialTrace {
	return FaultKernelTrialTrace{
		SchemaVersion: FaultKernelTrialTraceSchemaV1, RunID: "run-0123456789abcdef01234567", EvidenceScope: FaultKernelEvidenceScope,
		Profile: FaultKernelProfileSmoke, PlanID: FaultKernelSmokePlanID, CaseID: FaultKernelSmokeCaseID,
		PairID: "f4-kernel-smoke-7-0", Seed: 7, Repetition: 0, TrialOrder: 0, Phase: "measurement",
		Treatment: "no-injection", Role: "baseline", WorkloadSHA256: strings.Repeat("a", 64), InputSHA256: strings.Repeat("b", 64),
		Status: "SUCCEEDED", TimeoutMS: FaultKernelDefaultTrialTimeoutMS, ClockMode: FaultKernelClockMode,
		Protocol:   FaultKernelTraceProtocol{FaultMode: "none", InjectionBoundary: "matched-attempt-control", IsolationMode: FaultKernelIsolationMode, DaemonControl: FaultKernelDaemonControl, ExpectedRecovery: "attempt1-normal-completion"},
		Survivor:   FaultKernelSurvivorTrace{CompletedBeforeInjection: true, FinalStatus: "SUCCEEDED", ArtifactObservations: 1},
		ReasonCode: "NONE",
	}
}

func cgroupSample(trace FaultKernelTrialTrace, sequence int, stage string, monotonic int64, attempt int, exists, populated bool, members int) FaultKernelCgroupSample {
	sample := FaultKernelCgroupSample{SchemaVersion: FaultKernelCgroupSampleSchemaV1, RunID: trace.RunID, PairID: trace.PairID, TrialOrder: trace.TrialOrder, Phase: trace.Phase, SampleSequence: sequence, Stage: stage, MonotonicNS: monotonic, AttemptNumber: attempt}
	if exists {
		readback := expectedFaultKernelControllerReadback()
		cpu, memory, pids := readback.cpuMax, readback.memoryMax, readback.pidsMax
		sample.Exists, sample.Populated, sample.MemberCount = true, boolPointer(populated), intPointer(members)
		sample.ControllerReadback = FaultKernelControllerReadback{CPUMax: &cpu, MemoryMax: &memory, PIDsMax: &pids}
	}
	return sample
}

func cloneFaultKernelTrace(trace FaultKernelTrialTrace) FaultKernelTrialTrace {
	trace.CgroupSampleSequences = append([]int(nil), trace.CgroupSampleSequences...)
	trace.Target.Attempts = append([]FaultKernelAttemptTrace(nil), trace.Target.Attempts...)
	if trace.StartupReceipt != nil {
		value := *trace.StartupReceipt
		trace.StartupReceipt = &value
	}
	trace.Milestones = cloneFaultKernelMilestones(trace.Milestones)
	return trace
}

func cloneFaultKernelMilestones(value FaultKernelMilestones) FaultKernelMilestones {
	value.InjectedNS = cloneInt64(value.InjectedNS)
	value.DaemonExitedNS = cloneInt64(value.DaemonExitedNS)
	value.CleanupCompleteNS = cloneInt64(value.CleanupCompleteNS)
	value.Attempt2ReadyNS = cloneInt64(value.Attempt2ReadyNS)
	value.RecoveredNS = cloneInt64(value.RecoveredNS)
	return value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func boolPointer(value bool) *bool { return &value }
