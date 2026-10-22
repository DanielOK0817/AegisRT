package benchmark

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

const faultKernelMaxMonotonicNS int64 = 315_000_000_000

func ValidateFaultKernelPlan(plan FaultKernelPlan) error {
	p := plan.Protocol
	if p.Pairing != FaultKernelPairing || p.Counterbalance != FaultKernelCounterbalance || p.BootstrapSeed != FaultKernelBootstrapSeed || p.BootstrapResamples != FaultKernelBootstrapResamples || p.TrialTimeoutMS < faultKernelMinimumTrialTimeoutMS || p.TrialTimeoutMS > 300000 || p.Repetitions < 1 || p.Repetitions > 4 || len(p.MeasuredSeeds) == 0 || len(p.MeasuredSeeds) > 30 {
		return fmt.Errorf("fault-kernel plan has invalid protocol")
	}
	if err := validateFaultKernelSeeds(p.WarmupSeeds, p.MeasuredSeeds); err != nil {
		return err
	}
	switch plan.Profile {
	case FaultKernelProfileSmoke:
		if plan.PlanID != FaultKernelSmokePlanID || plan.CaseID != FaultKernelSmokeCaseID || len(p.WarmupSeeds) != 0 || len(p.MeasuredSeeds)*p.Repetitions > 4 {
			return fmt.Errorf("fault-kernel smoke plan exceeds its fixed profile")
		}
	case FaultKernelProfileFormal:
		if plan.PlanID != FaultKernelFormalPlanID || plan.CaseID != FaultKernelFormalCaseID || p.Repetitions != 1 || !slices.Equal(p.WarmupSeeds, faultKernelFormalWarmupSeeds) || !slices.Equal(p.MeasuredSeeds, faultKernelFormalMeasuredSeeds) {
			return fmt.Errorf("fault-kernel formal plan differs from the fixed protocol")
		}
	default:
		return fmt.Errorf("unsupported fault-kernel profile %q", plan.Profile)
	}
	return nil
}

func ValidateFaultKernelInput(input FaultKernelInput) error {
	if input.SchemaVersion != FaultKernelInputSchemaV1 || input.Kind != FaultKernelKind || input.WorkloadVersion != FaultKernelWorkloadVersion || input.FixtureProtocol != FaultKernelFixtureProtocol || input.EvidenceScope != FaultKernelEvidenceScope || input.ClockMode != FaultKernelClockMode || input.IsolationMode != FaultKernelIsolationMode || input.DaemonControl != FaultKernelDaemonControl {
		return fmt.Errorf("fault-kernel input has unsupported identity")
	}
	if err := ValidateFaultKernelPlan(FaultKernelPlan{input.Profile, input.PlanID, input.CaseID, input.Protocol}); err != nil {
		return err
	}
	wantModes := []FaultKernelMode{
		{"no-injection", "baseline", "none", "matched-attempt-control", "attempt1-normal-completion"},
		{"daemon-sigkill", "treatment", "daemon_sigkill", "attempt1-ready-and-populated", "restart-cleanup-requeue-attempt2"},
	}
	if len(input.Modes) != len(wantModes) || input.Modes[0] != wantModes[0] || input.Modes[1] != wantModes[1] {
		return fmt.Errorf("fault-kernel input modes differ from the fixed protocol")
	}
	for _, hash := range []string{input.WorkloadSHA256, input.ComponentManifestSHA256, input.RuntimeConfigSHA256} {
		if !validFaultKernelHash(hash) {
			return fmt.Errorf("fault-kernel input contains an invalid hash")
		}
	}
	return nil
}

func ValidateFaultKernelTrace(trace FaultKernelTrialTrace) error {
	planID, caseID, profileOK := faultKernelTraceIdentity(trace.Profile)
	if trace.SchemaVersion != FaultKernelTrialTraceSchemaV1 || trace.EvidenceScope != FaultKernelEvidenceScope || trace.PlanID != planID || trace.CaseID != caseID || !profileOK || trace.ClockMode != FaultKernelClockMode || validateRunID(trace.RunID) != nil || !validLabel(trace.PairID, false) || !validFaultKernelHash(trace.WorkloadSHA256) || !validFaultKernelHash(trace.InputSHA256) || trace.Repetition < 0 || trace.Repetition > 3 || trace.TrialOrder < 0 || trace.TrialOrder > 1 || !oneOf(trace.Phase, "warmup", "measurement") || trace.Profile == FaultKernelProfileSmoke && trace.Phase != "measurement" || trace.Profile == FaultKernelProfileFormal && (trace.Repetition != 0 || trace.Phase == "warmup" && !slices.Contains(faultKernelFormalWarmupSeeds, trace.Seed) || trace.Phase == "measurement" && !slices.Contains(faultKernelFormalMeasuredSeeds, trace.Seed)) || !oneOf(trace.Status, "SUCCEEDED", "FAILED", "TIMED_OUT", "INVALID") || !oneOf(trace.ReasonCode, "NONE", "HARNESS_FAILURE", "DAEMON_EXIT_MISMATCH", "ORPHAN_NOT_OBSERVED", "CHECKPOINT_NOT_LOADED", "RECOVERY_NOT_REQUEUED", "RETRY_NOT_READY", "CLEANUP_INCOMPLETE", "ISOLATION_ESCAPE", "DUPLICATE_ARTIFACTS", "TRIAL_TIMEOUT") || !validFaultKernelReceipt(trace.StartupReceipt) || !validFaultKernelObservationBounds(trace.Observations) || trace.TimeoutMS < faultKernelMinimumTrialTimeoutMS || trace.TimeoutMS > 300000 {
		return fmt.Errorf("fault-kernel trace has invalid identity")
	}
	if err := validateFaultKernelTraceMode(trace); err != nil {
		return err
	}
	if err := validateFaultKernelMilestones(trace.Milestones); err != nil {
		return err
	}
	if err := validateFaultKernelTarget(trace.Target); err != nil {
		return err
	}
	if !trace.Survivor.CompletedBeforeInjection || trace.Survivor.FinalStatus != "SUCCEEDED" ||
		trace.Survivor.ArtifactObservations < 1 || trace.Survivor.ArtifactObservations > 32 {
		return fmt.Errorf("fault-kernel survivor gate failed")
	}
	if len(trace.CgroupSampleSequences) < 2 || len(trace.CgroupSampleSequences) > 8 {
		return fmt.Errorf("fault-kernel trace has invalid cgroup sample count")
	}
	previous := 0
	for _, sequence := range trace.CgroupSampleSequences {
		if sequence <= previous || sequence > 1000000 {
			return fmt.Errorf("fault-kernel cgroup sample sequence is invalid")
		}
		previous = sequence
	}
	if trace.Status == "SUCCEEDED" {
		return validateFaultKernelSuccess(trace)
	}
	if trace.ReasonCode == "NONE" {
		return fmt.Errorf("unsuccessful fault-kernel trace requires a reason")
	}
	return validateFaultKernelFailure(trace)
}

func validateFaultKernelTraceMode(trace FaultKernelTrialTrace) error {
	want := FaultKernelTraceProtocol{IsolationMode: FaultKernelIsolationMode, DaemonControl: FaultKernelDaemonControl}
	switch trace.Role {
	case "baseline":
		want.FaultMode, want.InjectionBoundary, want.ExpectedRecovery = "none", "matched-attempt-control", "attempt1-normal-completion"
		if trace.Treatment != "no-injection" || trace.Protocol != want {
			return fmt.Errorf("fault-kernel baseline protocol is invalid")
		}
	case "treatment":
		want.FaultMode, want.InjectionBoundary, want.ExpectedRecovery = "daemon_sigkill", "attempt1-ready-and-populated", "restart-cleanup-requeue-attempt2"
		if trace.Treatment != "daemon-sigkill" || trace.Protocol != want {
			return fmt.Errorf("fault-kernel treatment protocol is invalid")
		}
	default:
		return fmt.Errorf("unsupported fault-kernel role %q", trace.Role)
	}
	return nil
}

func validateFaultKernelMilestones(m FaultKernelMilestones) error {
	values := []*int64{&m.StartedNS, &m.Attempt1ReadyNS, m.InjectedNS, m.DaemonExitedNS, m.CleanupCompleteNS, m.Attempt2ReadyNS, m.RecoveredNS, &m.FinishedNS}
	previous := int64(-1)
	for _, value := range values {
		if value == nil {
			continue
		}
		if *value < 0 || *value > faultKernelMaxMonotonicNS || *value < previous {
			return fmt.Errorf("fault-kernel milestones are outside the trial or non-monotonic")
		}
		previous = *value
	}
	return nil
}

func validateFaultKernelTarget(target FaultKernelTargetTrace) error {
	if target.AttemptBefore != 1 || target.AttemptAfter < 1 || target.AttemptAfter > 2 || len(target.Attempts) != target.AttemptAfter ||
		!oneOf(target.FinalStatus, "SUCCEEDED", "FAILED", "RUNNING") {
		return fmt.Errorf("fault-kernel target identity is invalid")
	}
	for index, attempt := range target.Attempts {
		if attempt.Number != index+1 || attempt.ArtifactObservations < 0 || attempt.ArtifactObservations > 32 ||
			!oneOf(attempt.FinalStatus, "SUCCEEDED", "FAILED", "LOST", "RUNNING") ||
			!oneOf(attempt.ReasonCode, "NONE", "RUNTIME_INTERRUPTED", "AGENT_EXIT", "AGENT_TIMEOUT") {
			return fmt.Errorf("fault-kernel attempt %d is invalid", index+1)
		}
	}
	return nil
}

func validateFaultKernelSuccess(trace FaultKernelTrialTrace) error {
	if trace.ReasonCode != "NONE" || trace.Milestones.CleanupCompleteNS == nil {
		return fmt.Errorf("successful fault-kernel trace lacks cleanup or has a reason")
	}
	if trace.Role == "baseline" {
		want := FaultKernelAttemptTrace{1, true, "SUCCEEDED", 1, "NONE"}
		wantObservations := FaultKernelObservations{DuplicateArtifactsObserved: true}
		if trace.StartupReceipt != nil || trace.Milestones.InjectedNS != nil || trace.Milestones.DaemonExitedNS != nil ||
			trace.Milestones.Attempt2ReadyNS != nil || trace.Milestones.RecoveredNS != nil || trace.Target.AttemptAfter != 1 ||
			len(trace.Target.Attempts) != 1 || trace.Target.Attempts[0] != want || trace.Target.FinalStatus != "SUCCEEDED" ||
			trace.Survivor != faultKernelSuccessfulSurvivor() || trace.Observations != wantObservations {
			return fmt.Errorf("successful fault-kernel baseline failed its semantic gate")
		}
		return nil
	}
	wantReceipt := FaultKernelStartupReceipt{1, true, true, 1, 0, 0}
	wantFirst := FaultKernelAttemptTrace{1, true, "LOST", 0, "RUNTIME_INTERRUPTED"}
	wantSecond := FaultKernelAttemptTrace{2, true, "SUCCEEDED", 1, "NONE"}
	wantObservations := FaultKernelObservations{
		DaemonSIGKILLObserved: true, OrphanObservedBeforeRestart: true,
		CheckpointLoaded: true, RecoveryRequeued: true, RestartReady: true,
		OldProcessesAfterCleanupObserved: true, OldCgroupsAfterCleanupObserved: true,
		DuplicateArtifactsObserved: true,
	}
	if trace.StartupReceipt == nil || *trace.StartupReceipt != wantReceipt || trace.Milestones.InjectedNS == nil ||
		trace.Milestones.DaemonExitedNS == nil || trace.Milestones.Attempt2ReadyNS == nil || trace.Milestones.RecoveredNS == nil ||
		trace.Target.AttemptAfter != 2 || len(trace.Target.Attempts) != 2 || trace.Target.Attempts[0] != wantFirst ||
		trace.Target.Attempts[1] != wantSecond || trace.Target.FinalStatus != "SUCCEEDED" ||
		trace.Survivor != faultKernelSuccessfulSurvivor() || trace.Observations != wantObservations {
		return fmt.Errorf("successful fault-kernel treatment failed its semantic gate")
	}
	return nil
}

func RecomputeFaultKernelMetrics(trace FaultKernelTrialTrace) (FaultKernelMetrics, error) {
	if err := ValidateFaultKernelTrace(trace); err != nil {
		return FaultKernelMetrics{}, err
	}
	m := trace.Milestones
	return FaultKernelMetrics{
		DaemonExitMS: faultKernelDeltaMS(m.InjectedNS, m.DaemonExitedNS),
		CleanupMS:    faultKernelDeltaMS(m.DaemonExitedNS, m.CleanupCompleteNS),
		RetryStartMS: faultKernelDeltaMS(m.InjectedNS, m.Attempt2ReadyNS),
		RecoveryMS:   faultKernelDeltaMS(m.InjectedNS, m.RecoveredNS),
		ElapsedMS:    float64(m.FinishedNS-m.StartedNS) / 1e6, Retries: trace.Target.AttemptAfter - trace.Target.AttemptBefore,
	}, nil
}

func ValidateFaultKernelMetrics(trace FaultKernelTrialTrace, metrics FaultKernelMetrics) error {
	want, err := RecomputeFaultKernelMetrics(trace)
	if err != nil {
		return err
	}
	if !equalFaultKernelFloat(metrics.DaemonExitMS, want.DaemonExitMS) || !equalFaultKernelFloat(metrics.CleanupMS, want.CleanupMS) ||
		!equalFaultKernelFloat(metrics.RetryStartMS, want.RetryStartMS) || !equalFaultKernelFloat(metrics.RecoveryMS, want.RecoveryMS) ||
		!closeMetric(metrics.ElapsedMS, want.ElapsedMS) || metrics.Retries != want.Retries {
		return fmt.Errorf("fault-kernel CSV metrics cannot be reproduced from trace")
	}
	return nil
}

func faultKernelDeltaMS(start, end *int64) *float64 {
	if start == nil || end == nil {
		return nil
	}
	value := float64(*end-*start) / 1e6
	return &value
}

func equalFaultKernelFloat(left, right *float64) bool {
	return left == nil && right == nil || left != nil && right != nil && !math.IsNaN(*left) && closeMetric(*left, *right)
}

func validCPUMax(value *string) bool {
	if value == nil {
		return false
	}
	fields := strings.Fields(*value)
	return len(fields) == 2 && (fields[0] == "max" || validPositiveInteger(fields[0])) && validPositiveInteger(fields[1])
}

func validControllerLimit(value *string) bool {
	return value != nil && (*value == "max" || validPositiveInteger(*value))
}

func validPositiveInteger(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0
}

func validFaultKernelReceipt(value *FaultKernelStartupReceipt) bool {
	return value == nil || *value == (FaultKernelStartupReceipt{1, true, true, 1, 0, 0})
}
func validFaultKernelObservationBounds(value FaultKernelObservations) bool {
	return value.OldProcessesAfterCleanup >= 0 && value.OldProcessesAfterCleanup <= 4096 &&
		(value.OldProcessesAfterCleanupObserved || value.OldProcessesAfterCleanup == 0) &&
		value.OldCgroupsAfterCleanup >= 0 && value.OldCgroupsAfterCleanup <= 2 &&
		(value.OldCgroupsAfterCleanupObserved || value.OldCgroupsAfterCleanup == 0) &&
		value.IsolationEscapeCount >= 0 && value.IsolationEscapeCount <= 4096 &&
		value.DuplicateArtifacts >= 0 && value.DuplicateArtifacts <= 62 &&
		(value.DuplicateArtifactsObserved || value.DuplicateArtifacts == 0)
}

func faultKernelSuccessfulSurvivor() FaultKernelSurvivorTrace {
	return FaultKernelSurvivorTrace{
		CompletedBeforeInjection: true, FinalStatus: "SUCCEEDED", ArtifactObservations: 1,
	}
}
