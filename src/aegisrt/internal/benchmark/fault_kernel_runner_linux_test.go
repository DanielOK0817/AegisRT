//go:build linux

package benchmark

import (
	"context"
	"errors"
	"testing"
	"time"

	"aegisrt/internal/resourcecontrol"
)

func TestFaultKernelCellModesHonorCounterbalancedOrder(t *testing.T) {
	for seedIndex := 0; seedIndex < 2; seedIndex++ {
		modes := faultKernelCellModes(seedIndex)
		if len(modes) != 2 || modes[0].order != 0 || modes[1].order != 1 {
			t.Fatalf("seed %d modes=%+v", seedIndex, modes)
		}
		wantFirstRole := "baseline"
		if seedIndex%2 == 1 {
			wantFirstRole = "treatment"
		}
		if modes[0].role != wantFirstRole {
			t.Fatalf("seed %d first role=%q", seedIndex, modes[0].role)
		}
	}
}

func TestFaultKernelAttemptObservationRequiresRealTree(t *testing.T) {
	expected := expectedFaultKernelControllerReadback()
	valid := faultKernelAttemptObservation{
		exists: true, populated: true, members: 2, escapes: 0,
		kernel: resourcecontrol.CgroupObservation{ControllerReadback: resourcecontrol.CgroupControllerReadback{
			CPUMax: &expected.cpuMax, MemoryMax: &expected.memoryMax, PIDsMax: &expected.pidsMax,
		}},
	}
	if !valid.ready() {
		t.Fatal("valid deterministic process tree rejected")
	}
	for _, invalid := range []faultKernelAttemptObservation{
		{},
		{exists: true, populated: false, members: 2},
		{exists: true, populated: true, members: 1},
		{exists: true, populated: true, members: 3},
		{exists: true, populated: true, members: 2, escapes: 1},
	} {
		if invalid.ready() {
			t.Fatalf("invalid process tree accepted: %+v", invalid)
		}
	}
}

func TestFaultKernelControllerReadbackMatchesFixedRuntimeContract(t *testing.T) {
	got := expectedFaultKernelControllerReadback()
	want := faultKernelControllerReadbackContract{
		cpuMax: "10000 100000", memoryMax: "67108864", pidsMax: "64",
	}
	if got != want {
		t.Fatalf("controller readback=%+v, want %+v", got, want)
	}
}

func TestFaultKernelHarnessSoftwareIdentityMustMatch(t *testing.T) {
	manifest := FaultKernelComponentManifest{Components: []FaultKernelComponent{
		{Role: FaultKernelComponentHarness, SHA256: "harness-hash"},
	}}
	if !faultKernelHarnessMatchesSoftware(manifest, Software{BinarySHA256: "harness-hash"}) {
		t.Fatal("matching harness and software identities rejected")
	}
	if faultKernelHarnessMatchesSoftware(manifest, Software{BinarySHA256: "other-hash"}) ||
		faultKernelHarnessMatchesSoftware(FaultKernelComponentManifest{}, Software{BinarySHA256: "harness-hash"}) {
		t.Fatal("mismatched or missing harness identity accepted")
	}
}

func TestFaultKernelFailureClassificationIsAllowlisted(t *testing.T) {
	status, reason := classifyFaultKernelTrialFailure(context.DeadlineExceeded)
	if status != "TIMED_OUT" || reason != "TRIAL_TIMEOUT" {
		t.Fatalf("deadline classification=%q/%q", status, reason)
	}
	status, reason = classifyFaultKernelTrialFailure(newFaultKernelTrialFailure(
		faultKernelDaemonFailure("fixture"), "INVALID", "HARNESS_FAILURE",
	))
	if status != "INVALID" || reason != "HARNESS_FAILURE" {
		t.Fatalf("invalid classification=%q/%q", status, reason)
	}
}

func TestFinalizeFaultKernelFailedTrialRequiresReadyAndTerminalProof(t *testing.T) {
	trace, samples := validFaultKernelBaselineEvidence()
	markFaultKernelAttempt1Ready(&trace)
	trace.Observations = FaultKernelObservations{}
	started := time.Unix(100, 0)
	executed := faultKernelExecutedTrial{
		Trace: trace, Samples: samples, StartedAt: started, FinishedAt: started.Add(time.Second),
	}
	failed, err := finalizeFaultKernelFailedTrial(executed, newFaultKernelTrialFailure(
		faultKernelDaemonFailure("fixture"), "FAILED", "HARNESS_FAILURE",
	))
	if err != nil || failed.Trace.Status != "FAILED" || failed.Trace.ReasonCode != "HARNESS_FAILURE" {
		t.Fatalf("failed trace=%+v err=%v", failed.Trace, err)
	}
	current := failed.Trace.Target.Attempts[failed.Trace.Target.AttemptAfter-1]
	if failed.Trace.Target.FinalStatus != "FAILED" || current.FinalStatus != "FAILED" ||
		current.ReasonCode != "RUNTIME_INTERRUPTED" || current.ArtifactObservations != 0 {
		t.Fatalf("failed target was not finalized: target=%+v current=%+v", failed.Trace.Target, current)
	}

	timeoutTrace, timeoutSamples := validFaultKernelBaselineEvidence()
	markFaultKernelAttempt1Ready(&timeoutTrace)
	timeoutTrace.Observations = FaultKernelObservations{}
	timeoutExecuted := faultKernelExecutedTrial{
		Trace: timeoutTrace, Samples: timeoutSamples, StartedAt: started, FinishedAt: started.Add(time.Second),
	}
	timedOut, err := finalizeFaultKernelFailedTrial(timeoutExecuted, context.DeadlineExceeded)
	if err != nil {
		t.Fatalf("timed-out trial was not publishable: %v", err)
	}
	timeoutCurrent := timedOut.Trace.Target.Attempts[timedOut.Trace.Target.AttemptAfter-1]
	if timedOut.Trace.Status != "TIMED_OUT" || timedOut.Trace.ReasonCode != "TRIAL_TIMEOUT" ||
		timedOut.Trace.Target.FinalStatus != "FAILED" || timeoutCurrent.FinalStatus != "FAILED" ||
		timeoutCurrent.ReasonCode != "AGENT_TIMEOUT" {
		t.Fatalf("timed-out target was not finalized: trace=%+v", timedOut.Trace)
	}

	executed.Samples = executed.Samples[:1]
	if _, err := finalizeFaultKernelFailedTrial(executed, faultKernelDaemonFailure("fixture")); err == nil {
		t.Fatal("failure without terminal cleanup proof was publishable")
	}
	_, samples = validFaultKernelBaselineEvidence()
	executed.Samples = samples
	if _, err := finalizeFaultKernelFailedTrial(executed, errors.Join(
		newFaultKernelTrialFailure(faultKernelDaemonFailure("fixture"), "FAILED", "HARNESS_FAILURE"),
		errFaultKernelUnverifiedCleanup,
	)); err == nil {
		t.Fatal("failure with unverified outer cleanup was publishable")
	}
}

func TestFinalizeFaultKernelFailedTrialRetainsTargetAndSurvivorDuplicates(t *testing.T) {
	for _, survivorArtifacts := range []int{1, 2} {
		trace, samples := validFaultKernelBaselineEvidence()
		trace.Target.Attempts[0].ArtifactObservations = 2
		trace.Survivor.ArtifactObservations = survivorArtifacts
		if survivorArtifacts == 2 {
			trace.Target.Attempts[0].ArtifactObservations = 1
		}
		trace.Observations.DuplicateArtifacts = 1
		started := time.Unix(100, 0)
		executed := faultKernelExecutedTrial{
			Trace: trace, Samples: samples, StartedAt: started, FinishedAt: started.Add(time.Second),
		}
		failed, err := finalizeFaultKernelFailedTrial(executed, newFaultKernelTrialFailure(
			faultKernelDaemonFailure("fixture"), "FAILED", "DUPLICATE_ARTIFACTS",
		))
		if err != nil || failed.Trace.ReasonCode != "DUPLICATE_ARTIFACTS" ||
			failed.Trace.Observations.DuplicateArtifacts != 1 {
			t.Fatalf("duplicate artifacts=%d trace=%+v err=%v", survivorArtifacts, failed.Trace, err)
		}
	}
}

func TestFinalizeFaultKernelFailedTrialPreservesSuccessfulTargetOnlyForCleanupOutcomes(t *testing.T) {
	trace, samples := validFaultKernelBaselineEvidence()
	started := time.Unix(100, 0)
	executed := faultKernelExecutedTrial{
		Trace: trace, Samples: samples, StartedAt: started, FinishedAt: started.Add(time.Second),
	}

	cleanupFailed, err := finalizeFaultKernelFailedTrial(executed, newFaultKernelTrialFailure(
		faultKernelDaemonFailure("fixture"), "FAILED", "CLEANUP_INCOMPLETE",
	))
	if err != nil {
		t.Fatalf("cleanup failure after target success was not publishable: %v", err)
	}
	cleanupCurrent := cleanupFailed.Trace.Target.Attempts[cleanupFailed.Trace.Target.AttemptAfter-1]
	if cleanupFailed.Trace.Target.FinalStatus != "SUCCEEDED" || cleanupCurrent.FinalStatus != "SUCCEEDED" ||
		cleanupCurrent.ArtifactObservations != 1 || cleanupCurrent.ReasonCode != "NONE" {
		t.Fatalf("cleanup failure changed successful target semantics: %+v", cleanupFailed.Trace.Target)
	}

	cleanupTimedOut, err := finalizeFaultKernelFailedTrial(executed, context.DeadlineExceeded)
	if err != nil || cleanupTimedOut.Trace.Target.FinalStatus != "SUCCEEDED" ||
		cleanupTimedOut.Trace.Target.Attempts[0].FinalStatus != "SUCCEEDED" {
		t.Fatalf("cleanup timeout changed successful target semantics: trace=%+v err=%v", cleanupTimedOut.Trace, err)
	}

	if _, err := finalizeFaultKernelFailedTrial(executed, newFaultKernelTrialFailure(
		faultKernelDaemonFailure("fixture"), "FAILED", "HARNESS_FAILURE",
	)); err == nil {
		t.Fatal("successful target with a non-cleanup harness failure was publishable")
	}
}
