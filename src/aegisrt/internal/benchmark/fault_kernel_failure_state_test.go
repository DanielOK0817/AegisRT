package benchmark

import "testing"

func TestFaultKernelTreatmentFailureReasonStages(t *testing.T) {
	for _, test := range []struct {
		name, status, reason string
	}{
		{name: "daemon exit", status: "FAILED", reason: "DAEMON_EXIT_MISMATCH"},
		{name: "orphan", status: "FAILED", reason: "ORPHAN_NOT_OBSERVED"},
		{name: "checkpoint", status: "FAILED", reason: "CHECKPOINT_NOT_LOADED"},
		{name: "requeue", status: "FAILED", reason: "RECOVERY_NOT_REQUEUED"},
		{name: "namespace", status: "INVALID", reason: "HARNESS_FAILURE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			trace, samples := faultKernelReasonStageEvidence(test.status, test.reason)
			if err := ValidateFaultKernelEvidence(trace, samples); err != nil {
				t.Fatalf("valid stage evidence rejected: %v", err)
			}
		})
	}
	t.Run("attempt two retry", func(t *testing.T) {
		trace, samples := faultKernelAttemptTwoRetryEvidence()
		if err := ValidateFaultKernelEvidence(trace, samples); err != nil {
			t.Fatalf("valid attempt-two retry failure rejected: %v", err)
		}
	})
}

func TestFaultKernelTreatmentFailureReasonRejectsContradictoryGates(t *testing.T) {
	daemon, daemonSamples := faultKernelReasonStageEvidence("FAILED", "DAEMON_EXIT_MISMATCH")
	daemon.Observations.DaemonSIGKILLObserved = true
	if err := ValidateFaultKernelEvidence(daemon, daemonSamples); err == nil {
		t.Fatal("daemon-exit failure claiming a confirmed SIGKILL was accepted")
	}

	orphan, orphanSamples := faultKernelReasonStageEvidence("FAILED", "ORPHAN_NOT_OBSERVED")
	orphanSamples[1] = readyFaultKernelPostExitSample(orphan, orphanSamples[1])
	if err := ValidateFaultKernelEvidence(orphan, orphanSamples); err == nil {
		t.Fatal("orphan failure with a ready post-exit sample was accepted")
	}

	checkpoint, checkpointSamples := faultKernelReasonStageEvidence("FAILED", "CHECKPOINT_NOT_LOADED")
	checkpoint.Observations.RestartReady = false
	if err := ValidateFaultKernelEvidence(checkpoint, checkpointSamples); err == nil {
		t.Fatal("checkpoint failure before restart readiness was accepted")
	}

	requeue, requeueSamples := faultKernelReasonStageEvidence("FAILED", "RECOVERY_NOT_REQUEUED")
	requeue.Observations.CheckpointLoaded = false
	if err := ValidateFaultKernelEvidence(requeue, requeueSamples); err == nil {
		t.Fatal("requeue failure without a loaded checkpoint was accepted")
	}

	invalid, invalidSamples := faultKernelReasonStageEvidence("INVALID", "HARNESS_FAILURE")
	invalid.Target = faultKernelFailedAttemptTwoTarget()
	invalid.StartupReceipt = &FaultKernelStartupReceipt{1, true, true, 1, 0, 0}
	invalid.Observations.CheckpointLoaded = true
	invalid.Observations.RecoveryRequeued = true
	if err := ValidateFaultKernelEvidence(invalid, invalidSamples); err == nil {
		t.Fatal("invalid namespace failure claiming attempt two was accepted")
	}

	retry, retrySamples := faultKernelAttemptTwoRetryEvidence()
	retry.Target.Attempts[1].ReadyObserved = true
	readyNS := int64(60_000_000)
	retry.Milestones.Attempt2ReadyNS = &readyNS
	if err := ValidateFaultKernelEvidence(retry, retrySamples); err == nil {
		t.Fatal("retry-not-ready failure claiming a ready attempt was accepted")
	}
}

func TestFaultKernelAttemptOneRejectsPostRequeueSample(t *testing.T) {
	trace, samples := faultKernelReasonStageEvidence("FAILED", "CHECKPOINT_NOT_LOADED")
	trace.ReasonCode = "HARNESS_FAILURE"
	_, treatmentSamples := validFaultKernelTreatmentEvidence()
	postRestart := treatmentSamples[2]
	postRestart.MonotonicNS = *trace.Milestones.CleanupCompleteNS
	postRestart.SampleSequence = 3
	terminal := samples[len(samples)-1]
	terminal.SampleSequence = 4
	samples = []FaultKernelCgroupSample{samples[0], samples[1], postRestart, terminal}
	trace.CgroupSampleSequences = []int{1, 2, 3, 4}
	if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
		t.Fatal("first-attempt failure with a post-requeue sample was accepted")
	}
}

func TestFaultKernelDuplicateReasonRequiresSuccessfulTargetObservation(t *testing.T) {
	trace, _ := validFaultKernelBaselineEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "DUPLICATE_ARTIFACTS"
	trace.Target.FinalStatus = "FAILED"
	trace.Target.Attempts[0] = FaultKernelAttemptTrace{
		Number: 1, ReadyObserved: true, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
	}
	trace.Observations.DuplicateArtifactsObserved = false
	if err := ValidateFaultKernelTrace(trace); err == nil {
		t.Fatal("duplicate-artifact reason without a successful target observation was accepted")
	}
}

func faultKernelReasonStageEvidence(status, reason string) (
	FaultKernelTrialTrace, []FaultKernelCgroupSample,
) {
	trace, original := validFaultKernelTreatmentEvidence()
	trace.Status, trace.ReasonCode = status, reason
	trace.StartupReceipt = nil
	trace.Target = FaultKernelTargetTrace{
		AttemptBefore: 1, AttemptAfter: 1, FinalStatus: "FAILED",
		Attempts: []FaultKernelAttemptTrace{{
			Number: 1, ReadyObserved: true, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
		}},
	}
	trace.Milestones.Attempt2ReadyNS = nil
	trace.Milestones.RecoveredNS = nil
	cleanup := trace.Milestones.FinishedNS
	trace.Milestones.CleanupCompleteNS = &cleanup
	trace.Observations = FaultKernelObservations{}

	terminal := original[len(original)-1]
	terminal.AttemptNumber = 1
	terminal.SampleSequence = 2
	samples := []FaultKernelCgroupSample{original[0], terminal}

	switch reason {
	case "DAEMON_EXIT_MISMATCH":
		trace.Milestones.DaemonExitedNS = nil
	case "ORPHAN_NOT_OBSERVED":
		trace.Observations.DaemonSIGKILLObserved = true
		notReady := original[1]
		notReady.Populated = boolPointer(false)
		terminal.SampleSequence = 3
		samples = []FaultKernelCgroupSample{original[0], notReady, terminal}
	case "CHECKPOINT_NOT_LOADED":
		trace.Observations = FaultKernelObservations{
			DaemonSIGKILLObserved: true, OrphanObservedBeforeRestart: true, RestartReady: true,
		}
		terminal.SampleSequence = 3
		samples = []FaultKernelCgroupSample{original[0], original[1], terminal}
	case "RECOVERY_NOT_REQUEUED":
		trace.Observations = FaultKernelObservations{
			DaemonSIGKILLObserved: true, OrphanObservedBeforeRestart: true,
			CheckpointLoaded: true, RestartReady: true,
		}
		terminal.SampleSequence = 3
		samples = []FaultKernelCgroupSample{original[0], original[1], terminal}
	default:
		trace.Observations = FaultKernelObservations{
			DaemonSIGKILLObserved: true, OrphanObservedBeforeRestart: true, RestartReady: true,
		}
		terminal.SampleSequence = 3
		samples = []FaultKernelCgroupSample{original[0], original[1], terminal}
	}
	trace.CgroupSampleSequences = make([]int, len(samples))
	for index := range samples {
		trace.CgroupSampleSequences[index] = samples[index].SampleSequence
	}
	return trace, samples
}

func faultKernelAttemptTwoRetryEvidence() (
	FaultKernelTrialTrace, []FaultKernelCgroupSample,
) {
	trace, samples := validFaultKernelTreatmentEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "RETRY_NOT_READY"
	trace.Target.FinalStatus = "FAILED"
	trace.Target.Attempts[1] = FaultKernelAttemptTrace{
		Number: 2, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
	}
	trace.Milestones.Attempt2ReadyNS = nil
	trace.Milestones.RecoveredNS = nil
	trace.Observations.DuplicateArtifactsObserved = false
	samples = []FaultKernelCgroupSample{samples[0], samples[1], samples[2], samples[4]}
	trace.CgroupSampleSequences = []int{
		samples[0].SampleSequence, samples[1].SampleSequence,
		samples[2].SampleSequence, samples[3].SampleSequence,
	}
	return trace, samples
}

func readyFaultKernelPostExitSample(
	trace FaultKernelTrialTrace, sample FaultKernelCgroupSample,
) FaultKernelCgroupSample {
	value := cgroupSample(trace, sample.SampleSequence, "post_daemon_exit",
		sample.MonotonicNS, 1, true, true, 2)
	return value
}

func faultKernelFailedAttemptTwoTarget() FaultKernelTargetTrace {
	return FaultKernelTargetTrace{
		AttemptBefore: 1, AttemptAfter: 2, FinalStatus: "FAILED",
		Attempts: []FaultKernelAttemptTrace{
			{Number: 1, ReadyObserved: true, FinalStatus: "LOST", ReasonCode: "RUNTIME_INTERRUPTED"},
			{Number: 2, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED"},
		},
	}
}
