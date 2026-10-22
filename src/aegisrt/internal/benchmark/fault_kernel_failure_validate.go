package benchmark

import "fmt"

func validateFaultKernelFailure(trace FaultKernelTrialTrace) error {
	if !validFaultKernelFailureStatusReason(trace.Status, trace.ReasonCode) {
		return fmt.Errorf("fault-kernel failure status and reason do not match")
	}
	if !validFaultKernelRoleFailureReason(trace.Role, trace.Status, trace.ReasonCode) {
		return fmt.Errorf("fault-kernel failure reason is invalid for its role")
	}
	if (trace.ReasonCode == "ISOLATION_ESCAPE") != (trace.Observations.IsolationEscapeCount > 0) {
		return fmt.Errorf("fault-kernel isolation reason does not match its observation")
	}
	if !trace.Target.Attempts[0].ReadyObserved {
		return fmt.Errorf("fault-kernel failure lacks the observed first-attempt readiness gate")
	}
	if trace.Role == "baseline" {
		if trace.StartupReceipt != nil || trace.Observations.DaemonSIGKILLObserved ||
			trace.Observations.OrphanObservedBeforeRestart || trace.Observations.CheckpointLoaded ||
			trace.Observations.RecoveryRequeued || trace.Observations.RestartReady ||
			trace.Observations.OldProcessesAfterCleanupObserved || trace.Observations.OldCgroupsAfterCleanupObserved {
			return fmt.Errorf("fault-kernel baseline contains treatment-only evidence")
		}
	}
	if trace.Role == "treatment" && trace.Target.AttemptAfter == 1 {
		if trace.StartupReceipt != nil || trace.Observations.RecoveryRequeued ||
			trace.Observations.OldProcessesAfterCleanupObserved ||
			trace.Observations.OldCgroupsAfterCleanupObserved {
			return fmt.Errorf("fault-kernel first-attempt failure claims a completed recovery gate")
		}
	}
	if trace.Role == "treatment" && trace.Target.AttemptAfter == 2 {
		wantFirst := FaultKernelAttemptTrace{
			Number: 1, ReadyObserved: true, FinalStatus: "LOST", ReasonCode: "RUNTIME_INTERRUPTED",
		}
		wantReceipt := FaultKernelStartupReceipt{1, true, true, 1, 0, 0}
		if trace.Target.Attempts[0] != wantFirst || trace.StartupReceipt == nil ||
			*trace.StartupReceipt != wantReceipt || trace.Milestones.InjectedNS == nil ||
			trace.Milestones.DaemonExitedNS == nil || !trace.Observations.DaemonSIGKILLObserved ||
			!trace.Observations.OrphanObservedBeforeRestart || !trace.Observations.CheckpointLoaded ||
			!trace.Observations.RecoveryRequeued || !trace.Observations.RestartReady {
			return fmt.Errorf("fault-kernel recovery contradicts its interrupted first attempt")
		}
	}
	if trace.Role == "treatment" {
		if err := validateFaultKernelTreatmentFailureStage(trace); err != nil {
			return err
		}
	}
	if trace.Target.FinalStatus == "FAILED" &&
		(trace.Observations.DuplicateArtifactsObserved || trace.Observations.DuplicateArtifacts != 0 ||
			trace.Survivor.ArtifactObservations != 1) {
		return fmt.Errorf("failed fault-kernel target claims post-success artifact observations")
	}
	if trace.ReasonCode == "DUPLICATE_ARTIFACTS" && trace.Target.FinalStatus != "SUCCEEDED" {
		return fmt.Errorf("duplicate-artifact failure lacks a successful target observation")
	}
	if trace.Role == "treatment" && trace.Target.FinalStatus == "SUCCEEDED" &&
		trace.Target.AttemptAfter != 2 {
		return fmt.Errorf("successful treatment target lacks its recovery attempt")
	}
	current := trace.Target.Attempts[len(trace.Target.Attempts)-1]
	if trace.Target.FinalStatus == "SUCCEEDED" {
		cleanupFailure := trace.Status == "FAILED" && trace.ReasonCode == "CLEANUP_INCOMPLETE"
		cleanupTimeout := trace.Status == "TIMED_OUT" && trace.ReasonCode == "TRIAL_TIMEOUT"
		duplicateFailure := trace.Status == "FAILED" && trace.ReasonCode == "DUPLICATE_ARTIFACTS"
		if (!cleanupFailure && !cleanupTimeout && !duplicateFailure) || current.FinalStatus != "SUCCEEDED" ||
			current.ReasonCode != "NONE" || !validFaultKernelArtifactSummary(trace, current) ||
			duplicateFailure && trace.Observations.DuplicateArtifacts == 0 {
			return fmt.Errorf("fault-kernel failure contradicts a successful target")
		}
		return nil
	}
	if trace.Target.FinalStatus != "FAILED" || current.FinalStatus != "FAILED" ||
		current.ArtifactObservations != 0 {
		return fmt.Errorf("fault-kernel failed target is not terminal")
	}
	wantReason := "RUNTIME_INTERRUPTED"
	if trace.Status == "TIMED_OUT" {
		wantReason = "AGENT_TIMEOUT"
	}
	if current.ReasonCode != wantReason {
		return fmt.Errorf("fault-kernel failed attempt has an invalid reason")
	}
	return nil
}

func validateFaultKernelTreatmentFailureStage(trace FaultKernelTrialTrace) error {
	m, o := trace.Milestones, trace.Observations
	if trace.Status == "INVALID" {
		if trace.Target.AttemptAfter != 1 || m.InjectedNS == nil || m.DaemonExitedNS == nil ||
			!o.DaemonSIGKILLObserved || !o.OrphanObservedBeforeRestart || !o.RestartReady ||
			o.CheckpointLoaded || o.RecoveryRequeued || trace.StartupReceipt != nil {
			return fmt.Errorf("invalid fault-kernel trace contradicts the recovery namespace gate")
		}
		return nil
	}
	if trace.Status != "FAILED" {
		return nil
	}
	switch trace.ReasonCode {
	case "DAEMON_EXIT_MISMATCH":
		if trace.Target.AttemptAfter != 1 || m.InjectedNS == nil || m.DaemonExitedNS != nil ||
			o.DaemonSIGKILLObserved || o.OrphanObservedBeforeRestart || o.RestartReady ||
			o.CheckpointLoaded || o.RecoveryRequeued {
			return fmt.Errorf("daemon-exit failure contradicts its injection gate")
		}
	case "ORPHAN_NOT_OBSERVED":
		if !faultKernelTreatmentAttemptOneGate(trace) || o.OrphanObservedBeforeRestart ||
			o.RestartReady || o.CheckpointLoaded {
			return fmt.Errorf("orphan failure contradicts its post-exit gate")
		}
	case "CHECKPOINT_NOT_LOADED":
		if !faultKernelTreatmentAttemptOneGate(trace) || !o.OrphanObservedBeforeRestart ||
			!o.RestartReady || o.CheckpointLoaded {
			return fmt.Errorf("checkpoint failure contradicts its restart gate")
		}
	case "RECOVERY_NOT_REQUEUED":
		if !faultKernelTreatmentAttemptOneGate(trace) || !o.OrphanObservedBeforeRestart ||
			!o.RestartReady || !o.CheckpointLoaded {
			return fmt.Errorf("requeue failure contradicts its checkpoint gate")
		}
	case "RETRY_NOT_READY":
		if trace.Target.AttemptAfter == 1 &&
			(!faultKernelTreatmentAttemptOneGate(trace) || !o.OrphanObservedBeforeRestart ||
				o.RestartReady || o.CheckpointLoaded) {
			return fmt.Errorf("retry failure contradicts its orphan gate")
		}
		if trace.Target.AttemptAfter == 2 &&
			(trace.Target.Attempts[1].ReadyObserved || m.Attempt2ReadyNS != nil) {
			return fmt.Errorf("retry failure claims a ready second attempt")
		}
	case "CLEANUP_INCOMPLETE":
		if trace.Target.AttemptAfter != 2 {
			return fmt.Errorf("treatment cleanup failure precedes its recovery attempt")
		}
	case "ISOLATION_ESCAPE":
		if trace.Target.AttemptAfter == 1 &&
			(!faultKernelTreatmentAttemptOneGate(trace) || o.OrphanObservedBeforeRestart ||
				o.RestartReady || o.CheckpointLoaded) {
			return fmt.Errorf("isolation failure contradicts its post-exit gate")
		}
	}
	return nil
}

func faultKernelTreatmentAttemptOneGate(trace FaultKernelTrialTrace) bool {
	return trace.Target.AttemptAfter == 1 && trace.Milestones.InjectedNS != nil &&
		trace.Milestones.DaemonExitedNS != nil && trace.Observations.DaemonSIGKILLObserved &&
		!trace.Observations.RecoveryRequeued && trace.StartupReceipt == nil
}

func validFaultKernelRoleFailureReason(role, status, reason string) bool {
	if role == "treatment" {
		return true
	}
	if role != "baseline" || status == "INVALID" {
		return false
	}
	if status == "TIMED_OUT" {
		return reason == "TRIAL_TIMEOUT"
	}
	return status == "FAILED" && oneOf(
		reason, "HARNESS_FAILURE", "CLEANUP_INCOMPLETE", "DUPLICATE_ARTIFACTS",
	)
}

func validFaultKernelArtifactSummary(trace FaultKernelTrialTrace, current FaultKernelAttemptTrace) bool {
	if !trace.Observations.DuplicateArtifactsObserved || current.ArtifactObservations < 1 ||
		trace.Survivor.ArtifactObservations < 1 {
		return false
	}
	want := current.ArtifactObservations - 1 + trace.Survivor.ArtifactObservations - 1
	return trace.Observations.DuplicateArtifacts == want
}

func validFaultKernelFailureStatusReason(status, reason string) bool {
	switch status {
	case "TIMED_OUT":
		return reason == "TRIAL_TIMEOUT"
	case "INVALID":
		return reason == "HARNESS_FAILURE"
	case "FAILED":
		switch reason {
		case "HARNESS_FAILURE", "DAEMON_EXIT_MISMATCH", "ORPHAN_NOT_OBSERVED",
			"CHECKPOINT_NOT_LOADED", "RECOVERY_NOT_REQUEUED", "RETRY_NOT_READY",
			"CLEANUP_INCOMPLETE", "ISOLATION_ESCAPE", "DUPLICATE_ARTIFACTS":
			return true
		default:
			return false
		}
	default:
		return false
	}
}
