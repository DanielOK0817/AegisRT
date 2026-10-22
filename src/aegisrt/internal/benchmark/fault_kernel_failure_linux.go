//go:build linux

package benchmark

import (
	"context"
	"errors"
	"time"

	"aegisrt/internal/controlplane"
)

var (
	errFaultKernelUnverifiedCleanup = errors.New("fault-kernel trial cleanup is unverified")
	errFaultKernelUnverifiedOutput  = errors.New("fault-kernel trial output observation is unverified")
)

type faultKernelTrialFailure struct {
	cause  error
	status string
	reason string
}

func (failure *faultKernelTrialFailure) Error() string {
	return "fault-kernel trial failed: " + failure.reason
}

func (failure *faultKernelTrialFailure) Unwrap() error { return failure.cause }

func newFaultKernelTrialFailure(err error, status, reason string) error {
	var existing *faultKernelTrialFailure
	if errors.As(err, &existing) {
		return err
	}
	return &faultKernelTrialFailure{cause: err, status: status, reason: reason}
}

func classifyFaultKernelTrialFailure(err error) (string, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMED_OUT", "TRIAL_TIMEOUT"
	}
	var failure *faultKernelTrialFailure
	if errors.As(err, &failure) && validFaultKernelFailureClass(failure.status, failure.reason) {
		return failure.status, failure.reason
	}
	return "FAILED", "HARNESS_FAILURE"
}

func validFaultKernelFailureClass(status, reason string) bool {
	return validFaultKernelFailureStatusReason(status, reason)
}

func finalizeFaultKernelFailedTrial(
	executed faultKernelExecutedTrial, failure error,
) (faultKernelExecutedTrial, error) {
	if errors.Is(failure, errFaultKernelUnverifiedCleanup) || errors.Is(failure, errFaultKernelUnverifiedOutput) {
		return faultKernelExecutedTrial{}, faultKernelDaemonFailure("unpublishable-trial-cleanup")
	}
	if !faultKernelFailureIsPublishable(executed) {
		return faultKernelExecutedTrial{}, faultKernelDaemonFailure("unpublishable-trial-failure")
	}
	status, reason := classifyFaultKernelTrialFailure(failure)
	executed.Trace.Status, executed.Trace.ReasonCode = status, reason
	finalizeFaultKernelFailedTarget(&executed.Trace, status)
	executed.Trace.Observations.IsolationEscapeCount = maximumFaultKernelEscapeCount(executed.Samples)
	if err := ValidateFaultKernelEvidence(executed.Trace, executed.Samples); err != nil {
		return faultKernelExecutedTrial{}, faultKernelDaemonFailure("invalid-trial-failure-evidence")
	}
	return executed, nil
}

func finalizeFaultKernelFailedTarget(trace *FaultKernelTrialTrace, status string) {
	if trace.Target.FinalStatus == "SUCCEEDED" {
		return
	}
	trace.Target.FinalStatus = "FAILED"
	index := trace.Target.AttemptAfter - 1
	if index < 0 || index >= len(trace.Target.Attempts) {
		return
	}
	reason := "RUNTIME_INTERRUPTED"
	if status == "TIMED_OUT" {
		reason = "AGENT_TIMEOUT"
	}
	trace.Target.Attempts[index].FinalStatus = "FAILED"
	trace.Target.Attempts[index].ReasonCode = reason
}

func maximumFaultKernelEscapeCount(samples []FaultKernelCgroupSample) int {
	maximum := 0
	for _, sample := range samples {
		if sample.EscapedDescendantCount > maximum {
			maximum = sample.EscapedDescendantCount
		}
	}
	return maximum
}

func faultKernelFailureIsPublishable(executed faultKernelExecutedTrial) bool {
	trace := executed.Trace
	if executed.StartedAt.IsZero() || executed.FinishedAt.Before(executed.StartedAt) ||
		!faultKernelPublishableSurvivor(trace.Survivor) || trace.Target.AttemptAfter < 1 ||
		len(trace.Target.Attempts) < 1 || !trace.Target.Attempts[0].ReadyObserved || len(executed.Samples) < 2 {
		return false
	}
	ready := false
	for _, sample := range executed.Samples {
		if sample.Stage == "attempt1_ready" && sample.AttemptNumber == 1 &&
			faultKernelObservationFromSample(sample).ready() {
			ready = true
			break
		}
	}
	terminal := executed.Samples[len(executed.Samples)-1]
	return ready && terminal.Stage == "trial_terminal" && !terminal.Exists
}

func faultKernelPublishableSurvivor(survivor FaultKernelSurvivorTrace) bool {
	return survivor.CompletedBeforeInjection && survivor.FinalStatus == "SUCCEEDED" &&
		survivor.ArtifactObservations >= 1 && survivor.ArtifactObservations <= 32
}

func markFaultKernelAttempt1Ready(trace *FaultKernelTrialTrace) {
	trace.Target = FaultKernelTargetTrace{AttemptBefore: 1, AttemptAfter: 1,
		Attempts: []FaultKernelAttemptTrace{{Number: 1, ReadyObserved: true,
			FinalStatus: "RUNNING", ReasonCode: "NONE"}}, FinalStatus: "RUNNING"}
	trace.Survivor = faultKernelSuccessfulSurvivor()
}

func markFaultKernelAttempt2Requeued(trace *FaultKernelTrialTrace) {
	trace.Target = FaultKernelTargetTrace{AttemptBefore: 1, AttemptAfter: 2,
		Attempts: []FaultKernelAttemptTrace{
			{Number: 1, ReadyObserved: true, FinalStatus: "LOST", ReasonCode: "RUNTIME_INTERRUPTED"},
			{Number: 2, FinalStatus: "RUNNING", ReasonCode: "NONE"},
		}, FinalStatus: "RUNNING"}
}

func markFaultKernelAttempt2Ready(trace *FaultKernelTrialTrace) {
	if len(trace.Target.Attempts) == 2 {
		trace.Target.Attempts[1].ReadyObserved = true
	}
}

func (runner *faultKernelRunner) appendFailureTerminalSample(
	executed *faultKernelExecutedTrial, trace *FaultKernelTrialTrace, started time.Time,
) error {
	if len(executed.Samples) > 0 {
		last := executed.Samples[len(executed.Samples)-1]
		if last.Stage == "trial_terminal" && !last.Exists {
			return nil
		}
	}
	attempt := trace.Target.AttemptAfter
	if attempt < 1 || attempt > 2 {
		return faultKernelDaemonFailure("failure-terminal-attempt")
	}
	attemptID := controlplane.AttemptID(trace.PairID+"-target", attempt)
	observation, err := runner.observeAttempt(attemptID)
	if err != nil || observation.exists {
		return faultKernelDaemonFailure("failure-terminal-observation")
	}
	observedNS := elapsedFaultKernelNS(started)
	runner.appendObservedSample(executed, trace, "trial_terminal", attempt, observedNS, observation)
	if trace.Milestones.CleanupCompleteNS == nil && trace.Milestones.Attempt2ReadyNS == nil {
		trace.Milestones.CleanupCompleteNS = faultKernelNSPointer(observedNS)
	}
	return nil
}
