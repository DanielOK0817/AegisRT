package benchmark

import "fmt"

type faultKernelSampleContract struct {
	stage   string
	attempt int
	exists  bool
}

var faultKernelSuccessfulSampleContracts = map[string][]faultKernelSampleContract{
	"baseline": {
		{stage: "attempt1_ready", attempt: 1, exists: true},
		{stage: "trial_terminal", attempt: 1, exists: false},
	},
	"treatment": {
		{stage: "attempt1_ready", attempt: 1, exists: true},
		{stage: "post_daemon_exit", attempt: 1, exists: true},
		{stage: "post_runtime_restart", attempt: 1, exists: false},
		{stage: "attempt2_ready", attempt: 2, exists: true},
		{stage: "trial_terminal", attempt: 2, exists: false},
	},
}

func ValidateFaultKernelEvidence(trace FaultKernelTrialTrace, samples []FaultKernelCgroupSample) error {
	if err := ValidateFaultKernelTrace(trace); err != nil {
		return err
	}
	contracts := faultKernelSuccessfulSampleContracts[trace.Role]
	if len(samples) != len(trace.CgroupSampleSequences) || trace.Status == "SUCCEEDED" && len(samples) != len(contracts) {
		return fmt.Errorf("fault-kernel sample count does not match trace")
	}
	previousNS := int64(-1)
	maximumEscapes := 0
	for index, sample := range samples {
		if err := validateFaultKernelSample(sample); err != nil {
			return fmt.Errorf("cgroup sample %d: %w", index, err)
		}
		if sample.RunID != trace.RunID || sample.PairID != trace.PairID || sample.TrialOrder != trace.TrialOrder ||
			sample.Phase != trace.Phase || sample.SampleSequence != trace.CgroupSampleSequences[index] ||
			sample.MonotonicNS < previousNS || sample.MonotonicNS < trace.Milestones.StartedNS ||
			sample.MonotonicNS > trace.Milestones.FinishedNS {
			return fmt.Errorf("cgroup sample %d does not match trace identity or order", index)
		}
		if trace.Status == "SUCCEEDED" {
			if err := validateFaultKernelSuccessfulSample(trace, sample, contracts[index]); err != nil {
				return fmt.Errorf("cgroup sample %d: %w", index, err)
			}
		}
		if sample.EscapedDescendantCount > maximumEscapes {
			maximumEscapes = sample.EscapedDescendantCount
		}
		previousNS = sample.MonotonicNS
	}
	if trace.Observations.IsolationEscapeCount != maximumEscapes {
		return fmt.Errorf("fault-kernel isolation escape summary does not match samples")
	}
	if trace.Status != "SUCCEEDED" {
		return validateFaultKernelFailedSamples(trace, samples)
	}
	return nil
}

func validateFaultKernelFailedSamples(
	trace FaultKernelTrialTrace, samples []FaultKernelCgroupSample,
) error {
	if len(samples) < 2 {
		return fmt.Errorf("failed trial lacks readiness and terminal cleanup proofs")
	}
	ready := samples[0]
	if err := validateFaultKernelSuccessfulSample(trace, ready,
		faultKernelSampleContract{stage: "attempt1_ready", attempt: 1, exists: true}); err != nil {
		return fmt.Errorf("failed trial readiness proof: %w", err)
	}
	terminal := samples[len(samples)-1]
	if terminal.Stage != "trial_terminal" || terminal.AttemptNumber != trace.Target.AttemptAfter ||
		terminal.Exists || terminal.EscapedDescendantCount != 0 {
		return fmt.Errorf("failed trial lacks terminal cgroup absence proof")
	}
	if trace.Milestones.CleanupCompleteNS != nil && terminal.MonotonicNS < *trace.Milestones.CleanupCompleteNS {
		return fmt.Errorf("failed trial terminal proof predates cleanup")
	}
	if trace.Role == "baseline" {
		if len(samples) != 2 || trace.Target.AttemptAfter != 1 || trace.StartupReceipt != nil ||
			trace.Milestones.InjectedNS != nil || trace.Milestones.DaemonExitedNS != nil ||
			trace.Milestones.Attempt2ReadyNS != nil || trace.Milestones.RecoveredNS != nil {
			return fmt.Errorf("failed baseline contains treatment-only evidence")
		}
		return nil
	}
	if trace.Target.AttemptAfter == 1 && len(samples) > 3 {
		return fmt.Errorf("first-attempt treatment contains post-requeue samples")
	}
	switch {
	case trace.ReasonCode == "DAEMON_EXIT_MISMATCH" && len(samples) != 2:
		return fmt.Errorf("daemon-exit failure contains a post-exit sample")
	case trace.ReasonCode == "ORPHAN_NOT_OBSERVED" && (len(samples) < 2 || len(samples) > 3):
		return fmt.Errorf("orphan failure has an invalid sample boundary")
	case oneOf(trace.ReasonCode, "CHECKPOINT_NOT_LOADED", "RECOVERY_NOT_REQUEUED") && len(samples) != 3:
		return fmt.Errorf("restart-gate failure lacks its exact sample prefix")
	case trace.Status == "INVALID" && len(samples) != 3:
		return fmt.Errorf("invalid recovery namespace trace lacks its exact sample prefix")
	}
	if trace.Observations.OrphanObservedBeforeRestart {
		if len(samples) < 3 || samples[1].Stage != "post_daemon_exit" {
			return fmt.Errorf("failed treatment lacks its observed orphan cgroup proof")
		}
		contract := faultKernelSampleContract{stage: "post_daemon_exit", attempt: 1, exists: true}
		if err := validateFaultKernelSuccessfulSample(trace, samples[1], contract); err != nil {
			return fmt.Errorf("failed treatment orphan proof: %w", err)
		}
	}
	if trace.ReasonCode == "ORPHAN_NOT_OBSERVED" && len(samples) == 3 {
		contract := faultKernelSampleContract{stage: "post_daemon_exit", attempt: 1, exists: true}
		if validateFaultKernelSuccessfulSample(trace, samples[1], contract) == nil {
			return fmt.Errorf("orphan failure contains a ready post-daemon sample")
		}
	}
	if trace.Milestones.InjectedNS == nil || len(samples) > len(faultKernelSuccessfulSampleContracts["treatment"]) {
		return fmt.Errorf("failed treatment has an invalid evidence prefix")
	}
	prefix := faultKernelSuccessfulSampleContracts["treatment"]
	for index := 1; index < len(samples)-1; index++ {
		sample, contract := samples[index], prefix[index]
		if sample.Stage != contract.stage || sample.AttemptNumber != contract.attempt {
			return fmt.Errorf("failed treatment samples are not a valid stage prefix")
		}
		if err := validateFaultKernelFailedStageTime(trace, sample); err != nil {
			return err
		}
	}
	return nil
}

func validateFaultKernelFailedStageTime(trace FaultKernelTrialTrace, sample FaultKernelCgroupSample) error {
	m := trace.Milestones
	switch sample.Stage {
	case "post_daemon_exit":
		if m.DaemonExitedNS == nil || sample.MonotonicNS < *m.DaemonExitedNS ||
			m.CleanupCompleteNS != nil && sample.MonotonicNS >= *m.CleanupCompleteNS {
			return fmt.Errorf("failed treatment post-exit sample is outside its milestone boundary")
		}
	case "post_runtime_restart":
		if m.CleanupCompleteNS == nil || sample.MonotonicNS != *m.CleanupCompleteNS || sample.Exists {
			return fmt.Errorf("failed treatment restart sample lacks cleanup proof")
		}
	case "attempt2_ready":
		if trace.Target.AttemptAfter != 2 {
			return fmt.Errorf("failed treatment attempt2 sample lacks a retry")
		}
		if len(trace.Target.Attempts) == 2 && trace.Target.Attempts[1].ReadyObserved {
			contract := faultKernelSampleContract{stage: "attempt2_ready", attempt: 2, exists: true}
			if err := validateFaultKernelSuccessfulSample(trace, sample, contract); err != nil {
				return fmt.Errorf("failed treatment attempt2 readiness proof: %w", err)
			}
		}
	}
	return nil
}

func validateFaultKernelSuccessfulSample(
	trace FaultKernelTrialTrace, sample FaultKernelCgroupSample, contract faultKernelSampleContract,
) error {
	if sample.Stage != contract.stage || sample.AttemptNumber != contract.attempt || sample.Exists != contract.exists ||
		sample.EscapedDescendantCount != 0 {
		return fmt.Errorf("successful sample violates its stage contract")
	}
	if contract.exists {
		if sample.Populated == nil || !*sample.Populated || sample.MemberCount == nil || *sample.MemberCount != 2 ||
			!matchesFaultKernelControllerReadback(sample.ControllerReadback) {
			return fmt.Errorf("successful ready sample lacks the fixed cgroup proof")
		}
	}
	return validateFaultKernelSuccessfulSampleTime(trace, sample)
}

func validateFaultKernelSuccessfulSampleTime(trace FaultKernelTrialTrace, sample FaultKernelCgroupSample) error {
	m := trace.Milestones
	valid := false
	switch sample.Stage {
	case "attempt1_ready":
		valid = sample.MonotonicNS >= m.Attempt1ReadyNS
	case "post_daemon_exit":
		valid = m.DaemonExitedNS != nil && m.CleanupCompleteNS != nil &&
			sample.MonotonicNS >= *m.DaemonExitedNS && sample.MonotonicNS < *m.CleanupCompleteNS
	case "post_runtime_restart":
		valid = m.CleanupCompleteNS != nil && sample.MonotonicNS == *m.CleanupCompleteNS
	case "attempt2_ready":
		valid = m.Attempt2ReadyNS != nil && sample.MonotonicNS >= *m.Attempt2ReadyNS
	case "trial_terminal":
		if trace.Role == "baseline" {
			valid = m.CleanupCompleteNS != nil && sample.MonotonicNS == *m.CleanupCompleteNS
		} else {
			valid = m.RecoveredNS != nil && sample.MonotonicNS >= *m.RecoveredNS
		}
	}
	if !valid {
		return fmt.Errorf("successful sample is outside its milestone boundary")
	}
	return nil
}

func validateFaultKernelSample(sample FaultKernelCgroupSample) error {
	if sample.SchemaVersion != FaultKernelCgroupSampleSchemaV1 || validateRunID(sample.RunID) != nil ||
		!validLabel(sample.PairID, false) || sample.TrialOrder < 0 || sample.TrialOrder > 1 ||
		!oneOf(sample.Phase, "warmup", "measurement") || sample.SampleSequence < 1 || sample.SampleSequence > 1000000 ||
		!oneOf(sample.Stage, "attempt1_ready", "post_daemon_exit", "post_runtime_restart", "attempt2_ready", "trial_terminal") ||
		sample.MonotonicNS < 0 || sample.MonotonicNS > faultKernelMaxMonotonicNS ||
		sample.AttemptNumber < 1 || sample.AttemptNumber > 2 ||
		sample.EscapedDescendantCount < 0 || sample.EscapedDescendantCount > 4096 {
		return fmt.Errorf("invalid cgroup sample identity")
	}
	r := sample.ControllerReadback
	if sample.Exists {
		if sample.Populated == nil || sample.MemberCount == nil || *sample.MemberCount < 0 || *sample.MemberCount > 4096 ||
			!validCPUMax(r.CPUMax) || !validControllerLimit(r.MemoryMax) || !validControllerLimit(r.PIDsMax) {
			return fmt.Errorf("existing cgroup sample lacks valid readback")
		}
	} else if sample.Populated != nil || sample.MemberCount != nil || r.CPUMax != nil || r.MemoryMax != nil || r.PIDsMax != nil {
		return fmt.Errorf("absent cgroup sample contains readback")
	}
	return nil
}
