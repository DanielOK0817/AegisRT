package benchmark

import "testing"

func TestFaultKernelFailedTargetRequiresTerminalAttemptSemantics(t *testing.T) {
	trace, _ := validFaultKernelBaselineEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "HARNESS_FAILURE"
	trace.Target.FinalStatus = "FAILED"
	trace.Target.Attempts[0].FinalStatus = "FAILED"
	trace.Target.Attempts[0].ArtifactObservations = 0
	trace.Target.Attempts[0].ReasonCode = "RUNTIME_INTERRUPTED"
	trace.Observations.DuplicateArtifactsObserved = false
	if err := ValidateFaultKernelTrace(trace); err != nil {
		t.Fatalf("terminal failed target rejected: %v", err)
	}

	invalid := cloneFaultKernelTrace(trace)
	invalid.Target.Attempts[0].ReasonCode = "AGENT_TIMEOUT"
	if err := ValidateFaultKernelTrace(invalid); err == nil {
		t.Fatal("non-timeout failure accepted AGENT_TIMEOUT")
	}
	invalid = cloneFaultKernelTrace(trace)
	invalid.Target.Attempts[0].FinalStatus = "RUNNING"
	if err := ValidateFaultKernelTrace(invalid); err == nil {
		t.Fatal("failure accepted a running current attempt")
	}

	timedOut := cloneFaultKernelTrace(trace)
	timedOut.Status, timedOut.ReasonCode = "TIMED_OUT", "TRIAL_TIMEOUT"
	timedOut.Target.Attempts[0].ReasonCode = "AGENT_TIMEOUT"
	if err := ValidateFaultKernelTrace(timedOut); err != nil {
		t.Fatalf("terminal timed-out target rejected: %v", err)
	}
	timedOut.Target.Attempts[0].ReasonCode = "RUNTIME_INTERRUPTED"
	if err := ValidateFaultKernelTrace(timedOut); err == nil {
		t.Fatal("timed-out failure accepted RUNTIME_INTERRUPTED")
	}
}

func TestFaultKernelSuccessfulTargetIsOnlyRetainedForCleanupFailureOrTimeout(t *testing.T) {
	trace, _ := validFaultKernelBaselineEvidence()
	for _, test := range []struct {
		name   string
		status string
		reason string
	}{
		{name: "cleanup failure", status: "FAILED", reason: "CLEANUP_INCOMPLETE"},
		{name: "cleanup timeout", status: "TIMED_OUT", reason: "TRIAL_TIMEOUT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneFaultKernelTrace(trace)
			candidate.Status, candidate.ReasonCode = test.status, test.reason
			if err := ValidateFaultKernelTrace(candidate); err != nil {
				t.Fatalf("successful target cleanup outcome rejected: %v", err)
			}
		})
	}

	forged := cloneFaultKernelTrace(trace)
	forged.Status, forged.ReasonCode = "FAILED", "HARNESS_FAILURE"
	if err := ValidateFaultKernelTrace(forged); err == nil {
		t.Fatal("successful target with FAILED/HARNESS_FAILURE was accepted")
	}
}

func TestFaultKernelEscapeSummaryEqualsMaximumSampleObservation(t *testing.T) {
	trace, samples := failedFaultKernelTreatmentBeforeRestartEvidence(2)
	trace.Observations.IsolationEscapeCount = 0

	if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
		t.Fatal("trace escape summary smaller than sample maximum was accepted")
	}
	trace.Observations.IsolationEscapeCount = 2
	if err := ValidateFaultKernelEvidence(trace, samples); err != nil {
		t.Fatalf("trace bound to maximum sample escape count rejected: %v", err)
	}
	trace.Observations.IsolationEscapeCount = 3
	if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
		t.Fatal("trace escape summary larger than sample maximum was accepted")
	}
}

func TestFaultKernelDuplicateArtifactObservationDistinguishesUnknownFromZero(t *testing.T) {
	trace, _ := validFaultKernelBaselineEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "HARNESS_FAILURE"
	trace.Target.FinalStatus = "FAILED"
	trace.Target.Attempts[0] = FaultKernelAttemptTrace{
		Number: 1, ReadyObserved: true, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
	}
	trace.Observations.DuplicateArtifactsObserved = false
	trace.Observations.DuplicateArtifacts = 0
	if err := ValidateFaultKernelTrace(trace); err != nil {
		t.Fatalf("unknown duplicate-artifact observation rejected: %v", err)
	}
	trace.Observations.DuplicateArtifacts = 1
	if err := ValidateFaultKernelTrace(trace); err == nil {
		t.Fatal("unobserved duplicate-artifact count was accepted as nonzero")
	}
}

func TestFaultKernelDuplicateArtifactFailureBindsTargetAndSurvivorCounts(t *testing.T) {
	for _, test := range []struct {
		name              string
		targetArtifacts   int
		survivorArtifacts int
	}{
		{name: "target duplicate", targetArtifacts: 2, survivorArtifacts: 1},
		{name: "survivor duplicate", targetArtifacts: 1, survivorArtifacts: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			trace, samples := validFaultKernelBaselineEvidence()
			trace.Status, trace.ReasonCode = "FAILED", "DUPLICATE_ARTIFACTS"
			trace.Target.Attempts[0].ArtifactObservations = test.targetArtifacts
			trace.Survivor.ArtifactObservations = test.survivorArtifacts
			trace.Observations.DuplicateArtifacts = 1
			if err := ValidateFaultKernelEvidence(trace, samples); err != nil {
				t.Fatalf("valid duplicate observation rejected: %v", err)
			}
			trace.Observations.DuplicateArtifacts = 0
			if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
				t.Fatal("duplicate reason without a duplicate count was accepted")
			}
		})
	}
}

func TestFaultKernelCleanupObservationDistinguishesUnknownFromZero(t *testing.T) {
	trace, _ := validFaultKernelTreatmentEvidence()
	trace.Observations.OldProcessesAfterCleanupObserved = false
	if err := ValidateFaultKernelTrace(trace); err == nil {
		t.Fatal("successful treatment accepted an unobserved old-process count")
	}
	trace, _ = validFaultKernelTreatmentEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "CLEANUP_INCOMPLETE"
	trace.Target.FinalStatus = "FAILED"
	trace.Target.Attempts[1].FinalStatus = "FAILED"
	trace.Target.Attempts[1].ArtifactObservations = 0
	trace.Target.Attempts[1].ReasonCode = "RUNTIME_INTERRUPTED"
	trace.Observations.DuplicateArtifactsObserved = false
	trace.Observations.OldProcessesAfterCleanup = 2
	if err := ValidateFaultKernelTrace(trace); err != nil {
		t.Fatalf("observed nonzero cleanup failure rejected: %v", err)
	}
	trace.Observations.OldProcessesAfterCleanupObserved = false
	if err := ValidateFaultKernelTrace(trace); err == nil {
		t.Fatal("unobserved nonzero old-process count was accepted")
	}
}

func TestFaultKernelFailureReasonMatchesRoleAndIsolationEvidence(t *testing.T) {
	baseline, _ := validFaultKernelBaselineEvidence()
	baseline.Status, baseline.ReasonCode = "FAILED", "CHECKPOINT_NOT_LOADED"
	baseline.Target.FinalStatus = "FAILED"
	baseline.Target.Attempts[0] = FaultKernelAttemptTrace{
		Number: 1, ReadyObserved: true, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
	}
	baseline.Observations.DuplicateArtifactsObserved = false
	if err := ValidateFaultKernelTrace(baseline); err == nil {
		t.Fatal("baseline accepted a treatment-only recovery reason")
	}

	treatment, samples := failedFaultKernelTreatmentBeforeRestartEvidence(0)
	if err := ValidateFaultKernelEvidence(treatment, samples); err == nil {
		t.Fatal("isolation reason without an escaped descendant was accepted")
	}
	samples[1].EscapedDescendantCount = 1
	treatment.Observations.IsolationEscapeCount = 1
	if err := ValidateFaultKernelEvidence(treatment, samples); err != nil {
		t.Fatalf("isolation failure bound to escaped descendant rejected: %v", err)
	}
	treatment.ReasonCode = "HARNESS_FAILURE"
	if err := ValidateFaultKernelEvidence(treatment, samples); err == nil {
		t.Fatal("escaped descendant with a non-isolation reason was accepted")
	}
}

func TestFaultKernelFailureRequiresFirstReadinessAndRecoveryGates(t *testing.T) {
	baseline, _ := validFaultKernelBaselineEvidence()
	baseline.Status, baseline.ReasonCode = "FAILED", "HARNESS_FAILURE"
	baseline.Target.FinalStatus = "FAILED"
	baseline.Target.Attempts[0] = FaultKernelAttemptTrace{
		Number: 1, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
	}
	baseline.Observations.DuplicateArtifactsObserved = false
	if err := ValidateFaultKernelTrace(baseline); err == nil {
		t.Fatal("failure without first-attempt readiness was accepted")
	}

	valid, _ := validFaultKernelTreatmentEvidence()
	valid.Status, valid.ReasonCode = "FAILED", "HARNESS_FAILURE"
	valid.Target.FinalStatus = "FAILED"
	valid.Target.Attempts[1] = FaultKernelAttemptTrace{
		Number: 2, ReadyObserved: true, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
	}
	valid.Observations.DuplicateArtifactsObserved = false
	if err := ValidateFaultKernelTrace(valid); err != nil {
		t.Fatalf("valid failed recovery trace rejected: %v", err)
	}

	for _, mutate := range []func(*FaultKernelTrialTrace){
		func(trace *FaultKernelTrialTrace) { trace.StartupReceipt = nil },
		func(trace *FaultKernelTrialTrace) { trace.Milestones.InjectedNS = nil },
		func(trace *FaultKernelTrialTrace) { trace.Milestones.DaemonExitedNS = nil },
		func(trace *FaultKernelTrialTrace) { trace.Observations.DaemonSIGKILLObserved = false },
		func(trace *FaultKernelTrialTrace) { trace.Observations.OrphanObservedBeforeRestart = false },
		func(trace *FaultKernelTrialTrace) { trace.Observations.CheckpointLoaded = false },
		func(trace *FaultKernelTrialTrace) { trace.Observations.RecoveryRequeued = false },
		func(trace *FaultKernelTrialTrace) { trace.Observations.RestartReady = false },
	} {
		trace := cloneFaultKernelTrace(valid)
		mutate(&trace)
		if err := ValidateFaultKernelTrace(trace); err == nil {
			t.Fatal("attempt-two failure without an exact recovery gate was accepted")
		}
	}

	attemptOne := cloneFaultKernelTrace(valid)
	attemptOne.Target = FaultKernelTargetTrace{
		AttemptBefore: 1, AttemptAfter: 1, FinalStatus: "FAILED",
		Attempts: []FaultKernelAttemptTrace{{
			Number: 1, ReadyObserved: true, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
		}},
	}
	attemptOne.StartupReceipt = nil
	attemptOne.Milestones.Attempt2ReadyNS = nil
	attemptOne.Milestones.RecoveredNS = nil
	attemptOne.Observations.CheckpointLoaded = false
	attemptOne.Observations.RecoveryRequeued = false
	attemptOne.Observations.RestartReady = false
	attemptOne.Observations.OldProcessesAfterCleanupObserved = false
	attemptOne.Observations.OldCgroupsAfterCleanupObserved = false
	attemptOne.CgroupSampleSequences = []int{1, 2}
	if err := ValidateFaultKernelTrace(attemptOne); err != nil {
		t.Fatalf("valid first-attempt treatment failure rejected: %v", err)
	}
	wantReceipt := FaultKernelStartupReceipt{1, true, true, 1, 0, 0}
	attemptOne.StartupReceipt = &wantReceipt
	if err := ValidateFaultKernelTrace(attemptOne); err == nil {
		t.Fatal("first-attempt failure with a recovery receipt was accepted")
	}
	attemptOne.StartupReceipt = nil
	attemptOne.Observations.RecoveryRequeued = true
	if err := ValidateFaultKernelTrace(attemptOne); err == nil {
		t.Fatal("first-attempt failure claiming requeue was accepted")
	}
}

func TestFaultKernelFailedRecoveryRequiresObservedOrphanProof(t *testing.T) {
	trace, samples := validFaultKernelTreatmentEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "HARNESS_FAILURE"
	trace.Target.FinalStatus = "FAILED"
	trace.Target.Attempts[1] = FaultKernelAttemptTrace{
		Number: 2, ReadyObserved: true, FinalStatus: "FAILED", ReasonCode: "RUNTIME_INTERRUPTED",
	}
	trace.Observations.DuplicateArtifactsObserved = false
	if err := ValidateFaultKernelEvidence(trace, samples); err != nil {
		t.Fatalf("valid failed recovery evidence rejected: %v", err)
	}

	withoutOrphan := []FaultKernelCgroupSample{samples[0], samples[len(samples)-1]}
	withoutOrphanTrace := cloneFaultKernelTrace(trace)
	withoutOrphanTrace.CgroupSampleSequences = []int{
		withoutOrphan[0].SampleSequence, withoutOrphan[1].SampleSequence,
	}
	if err := ValidateFaultKernelEvidence(withoutOrphanTrace, withoutOrphan); err == nil {
		t.Fatal("recovered failure without a post-daemon orphan proof was accepted")
	}

	notReady := append([]FaultKernelCgroupSample(nil), samples...)
	notReady[1].Populated = boolPointer(false)
	if err := ValidateFaultKernelEvidence(trace, notReady); err == nil {
		t.Fatal("recovered failure with a non-ready orphan sample was accepted")
	}
}
