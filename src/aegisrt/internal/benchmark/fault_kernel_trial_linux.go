//go:build linux

package benchmark

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
)

func (runner *faultKernelRunner) prepareTrialFiles(paths faultKernelTrialPaths) ([]byte, error) {
	registry, err := faultKernelRegistryJSON(runner.platform.benchmarkExecutable, paths.ready)
	if err != nil {
		return nil, err
	}
	if err := writeFaultKernelPrivateFile(paths.registry, registry); err != nil {
		return nil, err
	}
	if err := validateFaultKernelRegistry(paths.registry, runner.platform.benchmarkExecutable, paths.ready); err != nil {
		return nil, err
	}
	token, err := newFaultKernelToken()
	if err != nil {
		return nil, err
	}
	if err := writeFaultKernelPrivateFile(paths.token, append(append([]byte(nil), token...), '\n')); err != nil {
		clearFaultKernelToken(token)
		return nil, err
	}
	return token, nil
}

func (runner *faultKernelRunner) waitAttemptReady(
	ctx context.Context, api faultKernelRuntimeClient, ready string, cell faultKernelCell, attemptID string,
) error {
	return waitFaultKernelCondition(ctx, func() (bool, error) {
		marker, err := faultKernelMarkerReady(ready, attemptID)
		if err != nil || !marker {
			return false, err
		}
		statuses, err := api.TaskStatuses(ctx)
		if err != nil {
			return false, err
		}
		if len(statuses) != 2 || statuses[cell.Target.ID] != domain.TaskStatusRunning {
			return false, nil
		}
		if statuses[cell.Survivor.ID] != domain.TaskStatusSucceeded {
			return false, nil
		}
		artifacts, err := api.OutputArtifactCount(ctx, cell.Survivor.ID)
		if err != nil || artifacts != 1 {
			return false, err
		}
		observation, err := runner.observeAttempt(attemptID)
		return err == nil && observation.ready(), err
	})
}

func (runner *faultKernelRunner) finishBaseline(
	ctx context.Context, api faultKernelRuntimeClient, cell faultKernelCell, started time.Time,
	trace *FaultKernelTrialTrace, executed *faultKernelExecutedTrial,
) error {
	before, err := waitFaultKernelSuccess(ctx, api, cell)
	if err != nil {
		return err
	}
	trace.Target = faultKernelSuccessfulTarget(false)
	trace.Survivor = faultKernelSuccessfulSurvivor()
	recordFaultKernelArtifactCounts(trace, before)
	cleanupNS, err := runner.appendAbsentSample(ctx, executed, trace, "trial_terminal", 1, started)
	if err != nil {
		return newFaultKernelTrialFailure(err, "FAILED", "CLEANUP_INCOMPLETE")
	}
	trace.Milestones.CleanupCompleteNS = faultKernelNSPointer(cleanupNS)
	after, err := waitFaultKernelSuccess(ctx, api, cell)
	if err != nil || after.target < before.target || after.survivor < before.survivor {
		return errors.Join(faultKernelDaemonFailure("artifact-recheck"), errFaultKernelUnverifiedOutput)
	}
	recordFaultKernelArtifactCounts(trace, after)
	if after.duplicates() > 0 {
		return newFaultKernelTrialFailure(
			faultKernelDaemonFailure("duplicate-artifacts"), "FAILED", "DUPLICATE_ARTIFACTS")
	}
	return nil
}

func (runner *faultKernelRunner) finishTreatment(
	ctx context.Context, api faultKernelRuntimeClient, cell faultKernelCell, started time.Time,
	trace *FaultKernelTrialTrace, executed *faultKernelExecutedTrial,
) error {
	before, err := waitFaultKernelSuccess(ctx, api, cell)
	if err != nil {
		return err
	}
	trace.Milestones.RecoveredNS = faultKernelNSPointer(elapsedFaultKernelNS(started))
	trace.Target = faultKernelSuccessfulTarget(true)
	trace.Survivor = faultKernelSuccessfulSurvivor()
	recordFaultKernelArtifactCounts(trace, before)
	if _, err := runner.appendAbsentSample(ctx, executed, trace, "trial_terminal", 2, started); err != nil {
		return newFaultKernelTrialFailure(err, "FAILED", "CLEANUP_INCOMPLETE")
	}
	after, err := waitFaultKernelSuccess(ctx, api, cell)
	if err != nil || after.target < before.target || after.survivor < before.survivor {
		return errors.Join(faultKernelDaemonFailure("artifact-recheck"), errFaultKernelUnverifiedOutput)
	}
	recordFaultKernelArtifactCounts(trace, after)
	if after.duplicates() > 0 {
		return newFaultKernelTrialFailure(
			faultKernelDaemonFailure("duplicate-artifacts"), "FAILED", "DUPLICATE_ARTIFACTS")
	}
	return nil
}

func recordFaultKernelArtifactCounts(trace *FaultKernelTrialTrace, counts faultKernelArtifactCounts) {
	current := len(trace.Target.Attempts) - 1
	if current >= 0 {
		trace.Target.Attempts[current].ArtifactObservations = counts.target
	}
	trace.Survivor.ArtifactObservations = counts.survivor
	trace.Observations.DuplicateArtifactsObserved = true
	trace.Observations.DuplicateArtifacts = counts.duplicates()
}

func (runner *faultKernelRunner) appendReadySample(
	executed *faultKernelExecutedTrial, trace *FaultKernelTrialTrace, stage string, attempt int, started time.Time,
) error {
	if err := runner.appendSample(executed, trace, stage, attempt, started); err != nil {
		return err
	}
	observation := faultKernelObservationFromSample(executed.Samples[len(executed.Samples)-1])
	if observation.escapes > 0 {
		return newFaultKernelTrialFailure(
			faultKernelDaemonFailure("isolation-escape"), "FAILED", "ISOLATION_ESCAPE")
	}
	if !observation.ready() {
		return faultKernelDaemonFailure("attempt-tree")
	}
	return nil
}

func (runner *faultKernelRunner) appendAbsentSample(
	ctx context.Context, executed *faultKernelExecutedTrial, trace *FaultKernelTrialTrace,
	stage string, attempt int, started time.Time,
) (int64, error) {
	attemptID := controlplane.AttemptID(trace.PairID+"-target", attempt)
	var observation faultKernelAttemptObservation
	if err := waitFaultKernelCondition(ctx, func() (bool, error) {
		current, err := runner.observeAttempt(attemptID)
		if err != nil {
			return false, err
		}
		observation = current
		if stage == "post_runtime_restart" {
			trace.Observations.OldCgroupsAfterCleanupObserved = true
			trace.Observations.OldCgroupsAfterCleanup = 0
			if current.exists {
				trace.Observations.OldCgroupsAfterCleanup = 1
			}
		}
		return !current.exists, nil
	}); err != nil {
		return 0, err
	}
	observedNS := elapsedFaultKernelNS(started)
	runner.appendObservedSample(executed, trace, stage, attempt, observedNS, observation)
	return observedNS, nil
}

func (runner *faultKernelRunner) appendSample(
	executed *faultKernelExecutedTrial, trace *FaultKernelTrialTrace, stage string, attempt int, started time.Time,
) error {
	attemptID := controlplane.AttemptID(trace.PairID+"-target", attempt)
	observation, err := runner.observeAttempt(attemptID)
	if err != nil {
		return err
	}
	runner.appendObservedSample(executed, trace, stage, attempt, elapsedFaultKernelNS(started), observation)
	return nil
}

func (runner *faultKernelRunner) appendObservedSample(
	executed *faultKernelExecutedTrial, trace *FaultKernelTrialTrace,
	stage string, attempt int, observedNS int64, observation faultKernelAttemptObservation,
) {
	sequence := len(executed.Samples) + 1
	executed.Samples = append(executed.Samples, newFaultKernelCgroupSample(
		runner.runID, trace.PairID, trace.Phase, sequence, trace.TrialOrder,
		stage, observedNS, attempt, observation.kernel, observation.escapes,
	))
	trace.CgroupSampleSequences = append(trace.CgroupSampleSequences, sequence)
}

func (runner *faultKernelRunner) observeAttempt(attemptID string) (faultKernelAttemptObservation, error) {
	if runner.activeDriver == nil || runner.activeRoot == "" {
		return faultKernelAttemptObservation{}, faultKernelDaemonFailure("runtime-driver")
	}
	observation, err := runner.activeDriver.Observe(attemptID)
	if err != nil {
		return faultKernelAttemptObservation{}, err
	}
	result := faultKernelAttemptObservation{kernel: observation, exists: observation.Exists}
	if !observation.Exists {
		return result, nil
	}
	if observation.Populated == nil || observation.MemberCount == nil {
		return faultKernelAttemptObservation{}, faultKernelDaemonFailure("cgroup-observation")
	}
	result.populated, result.members = *observation.Populated, *observation.MemberCount
	group := runner.attemptGroupPath(attemptID)
	result.escapes, err = countFaultKernelEscapes(faultKernelProcRoot, group)
	if err != nil {
		return faultKernelAttemptObservation{}, err
	}
	return result, nil
}

func (runner *faultKernelRunner) attemptGroupPath(attemptID string) string {
	return filepath.Join(runner.activeRoot, "aegisrt", runner.activeNamespace, attemptID)
}
