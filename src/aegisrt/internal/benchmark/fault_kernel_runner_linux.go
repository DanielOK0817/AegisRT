//go:build linux

package benchmark

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"aegisrt/internal/controlplane"
	"aegisrt/internal/resourcecontrol"
)

type faultKernelRunner struct {
	platform        *faultKernelPlatform
	runID           string
	plan            FaultKernelPlan
	workloadHash    string
	inputHash       string
	session         string
	trialIndex      int
	activeRoot      string
	activeNamespace string
	activeDriver    *resourcecontrol.CgroupV2Driver
}

type faultKernelRunMode struct {
	role, treatment string
	order           int
	protocol        FaultKernelTraceProtocol
}

type faultKernelAttemptObservation struct {
	kernel            resourcecontrol.CgroupObservation
	exists, populated bool
	members, escapes  int
}

func (value faultKernelAttemptObservation) ready() bool {
	expected := expectedFaultKernelControllerReadback()
	return value.exists && value.populated && value.members == 2 && value.escapes == 0 &&
		value.kernel.ControllerReadback.CPUMax != nil && *value.kernel.ControllerReadback.CPUMax == expected.cpuMax &&
		value.kernel.ControllerReadback.MemoryMax != nil && *value.kernel.ControllerReadback.MemoryMax == expected.memoryMax &&
		value.kernel.ControllerReadback.PIDsMax != nil && *value.kernel.ControllerReadback.PIDsMax == expected.pidsMax
}

func (runner *faultKernelRunner) run(ctx context.Context, cells []faultKernelCell) ([]faultKernelExecutedTrial, error) {
	result := make([]faultKernelExecutedTrial, 0, len(cells)*2)
	for _, cell := range cells {
		seedIndex := faultKernelSeedIndex(runner.plan, cell.Phase, cell.Seed)
		if seedIndex < 0 {
			return nil, faultKernelDaemonFailure("cell-seed")
		}
		for _, mode := range faultKernelCellModes(seedIndex) {
			trial, err := runner.runTrial(ctx, cell, mode)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				trial, err = finalizeFaultKernelFailedTrial(trial, err)
				if err != nil {
					return nil, err
				}
			} else if err := ValidateFaultKernelEvidence(trial.Trace, trial.Samples); err != nil {
				return nil, faultKernelDaemonFailure("successful-trial-evidence")
			}
			result = append(result, trial)
		}
	}
	return result, nil
}

func (runner *faultKernelRunner) runTrial(
	parent context.Context, cell faultKernelCell, mode faultKernelRunMode,
) (executed faultKernelExecutedTrial, resultErr error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(runner.plan.Protocol.TrialTimeoutMS)*time.Millisecond)
	defer cancel()
	started := time.Now()
	startedAt := started.UTC()
	trace := runner.baseTrace(cell, mode)
	traceable := false
	defer func() {
		trace.Milestones.FinishedNS = elapsedFaultKernelNS(started)
		executed.Trace, executed.StartedAt, executed.FinishedAt = trace, startedAt, time.Now().UTC()
	}()
	paths, err := createFaultKernelTrialPaths(runner.session, runner.trialIndex)
	outerID := fmt.Sprintf("f4-daemon-%03d", runner.trialIndex)
	runner.trialIndex++
	if err != nil {
		return executed, err
	}
	token, err := runner.prepareTrialFiles(paths)
	if err != nil {
		return executed, err
	}
	defer clearFaultKernelToken(token)
	attempt1, attempt2 := controlplane.AttemptID(cell.Target.ID, 1), controlplane.AttemptID(cell.Target.ID, 2)
	runtimeNamespace, err := resourcecontrol.RuntimeNamespaceForStateFile(paths.state)
	if err != nil {
		return executed, faultKernelDaemonFailure("runtime-namespace")
	}
	config := fixedFaultKernelRuntimeConfig()
	if _, err := runner.platform.supervisorDriver.Create(outerID, resourcecontrol.Limits{
		Resources: config.Capacity.Clone(), PIDsMax: config.PIDsMax, CPUPeriodMicros: config.CPUPeriodMicros,
	}); err != nil {
		if cleanupErr := runner.platform.supervisorDriver.Destroy(outerID); cleanupErr != nil {
			return executed, errors.Join(
				faultKernelDaemonFailure("daemon-cgroup"), errFaultKernelUnverifiedCleanup,
			)
		}
		return executed, faultKernelDaemonFailure("daemon-cgroup")
	}
	outerRoot := filepath.Join(runner.platform.cgroupRoot, "aegisrt", faultKernelSupervisorNamespace, outerID)
	var daemon *faultKernelDaemon
	defer func() {
		var stopErr, attemptCleanupErr, reclaimErr error
		if daemon != nil {
			stopCtx, stop := context.WithTimeout(context.Background(), faultKernelStopLimit)
			stopErr = daemon.Stop(stopCtx)
			stop()
		}
		if runner.activeDriver != nil {
			attemptCleanupErr = errors.Join(runner.activeDriver.Destroy(attempt1),
				runner.activeDriver.Destroy(attempt2))
		}
		if resultErr != nil && traceable && attemptCleanupErr == nil {
			attemptCleanupErr = runner.appendFailureTerminalSample(&executed, &trace, started)
		}
		if runtimeNamespace == "" {
			reclaimErr = runner.platform.supervisorDriver.Destroy(outerID)
		} else {
			reclaimErr = runner.platform.supervisorDriver.ReclaimAttemptHierarchy(outerID, runtimeNamespace)
		}
		runner.activeDriver, runner.activeRoot, runner.activeNamespace = nil, "", ""
		if errors.Join(stopErr, attemptCleanupErr, reclaimErr) != nil {
			resultErr = errors.Join(resultErr, errFaultKernelUnverifiedCleanup)
		}
	}()

	client, receipt, startErr := runner.startDaemon(ctx, paths, token, outerID, "auto", false)
	if startErr != nil {
		return executed, startErr
	}
	daemon = client.daemon
	defer client.Close()
	if !validFaultKernelInitialReceipt(receipt) || receipt.RuntimeNamespace != runtimeNamespace {
		return executed, faultKernelDaemonFailure("initial-receipt")
	}
	runner.activeRoot, runner.activeNamespace = outerRoot, runtimeNamespace
	runner.activeDriver, err = resourcecontrol.NewCgroupV2Driver(outerRoot, runtimeNamespace)
	if err != nil || runner.activeDriver.EnsureNamespaceEmpty() != nil {
		return executed, faultKernelDaemonFailure("runtime-driver")
	}
	if err := client.api.SubmitTargetAndSurvivor(ctx, cell.Target, cell.Survivor); err != nil {
		return executed, faultKernelDaemonFailure("task-submit")
	}
	if err := runner.waitAttemptReady(ctx, client.api, paths.ready, cell, attempt1); err != nil {
		return executed, err
	}
	trace.Milestones.Attempt1ReadyNS = elapsedFaultKernelNS(started)
	if err := runner.appendReadySample(&executed, &trace, "attempt1_ready", 1, started); err != nil {
		return executed, err
	}
	markFaultKernelAttempt1Ready(&trace)
	traceable = true
	if mode.role == "baseline" {
		if err := runner.finishBaseline(ctx, client.api, cell, started, &trace, &executed); err != nil {
			return executed, err
		}
	} else {
		client.Close()
		trace.Milestones.InjectedNS = faultKernelNSPointer(elapsedFaultKernelNS(started))
		if err := daemon.KillAndWait(ctx); err != nil {
			return executed, newFaultKernelTrialFailure(err, "FAILED", "DAEMON_EXIT_MISMATCH")
		}
		trace.Observations.DaemonSIGKILLObserved = true
		trace.Milestones.DaemonExitedNS = faultKernelNSPointer(elapsedFaultKernelNS(started))
		if err := runner.appendReadySample(&executed, &trace, "post_daemon_exit", 1, started); err != nil {
			return executed, newFaultKernelTrialFailure(err, "FAILED", "ORPHAN_NOT_OBSERVED")
		}
		trace.Observations.OrphanObservedBeforeRestart = true
		oldIdentities, err := captureFaultKernelIdentities(
			faultKernelProcRoot, runner.attemptGroupPath(attempt1),
		)
		if err != nil {
			return executed, err
		}
		client, receipt, err = runner.startDaemon(ctx, paths, token, outerID, outerRoot, true)
		if err != nil {
			return executed, newFaultKernelTrialFailure(err, "FAILED", "RETRY_NOT_READY")
		}
		daemon = client.daemon
		defer client.Close()
		trace.Observations.RestartReady = true
		if receipt.RuntimeNamespace != runtimeNamespace {
			return executed, newFaultKernelTrialFailure(
				faultKernelDaemonFailure("recovery-namespace"), "INVALID", "HARNESS_FAILURE")
		}
		if !receipt.StateLoaded {
			return executed, newFaultKernelTrialFailure(
				faultKernelDaemonFailure("checkpoint-not-loaded"), "FAILED", "CHECKPOINT_NOT_LOADED")
		}
		trace.Observations.CheckpointLoaded = true
		if !validFaultKernelRecoveryReceipt(receipt) {
			return executed, newFaultKernelTrialFailure(
				faultKernelDaemonFailure("recovery-not-requeued"), "FAILED", "RECOVERY_NOT_REQUEUED")
		}
		trace.Observations.RecoveryRequeued = true
		trace.StartupReceipt = faultKernelReceiptPointer(receipt.Evidence())
		markFaultKernelAttempt2Requeued(&trace)
		cleanupNS, err := runner.appendAbsentSample(ctx, &executed, &trace, "post_runtime_restart", 1, started)
		if err != nil {
			return executed, err
		}
		trace.Milestones.CleanupCompleteNS = faultKernelNSPointer(cleanupNS)
		if err := waitFaultKernelCondition(ctx, func() (bool, error) {
			live, err := countLiveFaultKernelIdentities(faultKernelProcRoot, oldIdentities)
			if err == nil {
				trace.Observations.OldProcessesAfterCleanupObserved = true
				trace.Observations.OldProcessesAfterCleanup = live
			}
			return live == 0, err
		}); err != nil {
			return executed, newFaultKernelTrialFailure(err, "FAILED", "CLEANUP_INCOMPLETE")
		}
		if err := runner.waitAttemptReady(ctx, client.api, paths.ready, cell, attempt2); err != nil {
			return executed, newFaultKernelTrialFailure(err, "FAILED", "RETRY_NOT_READY")
		}
		trace.Milestones.Attempt2ReadyNS = faultKernelNSPointer(elapsedFaultKernelNS(started))
		if err := runner.appendReadySample(&executed, &trace, "attempt2_ready", 2, started); err != nil {
			trace.Milestones.Attempt2ReadyNS = nil
			return executed, newFaultKernelTrialFailure(err, "FAILED", "RETRY_NOT_READY")
		}
		markFaultKernelAttempt2Ready(&trace)
		if err := runner.finishTreatment(ctx, client.api, cell, started, &trace, &executed); err != nil {
			return executed, err
		}
	}
	return executed, nil
}

type faultKernelRunningDaemon struct {
	daemon *faultKernelDaemon
	api    *faultKernelAPIClient
}

func (running *faultKernelRunningDaemon) Close() {
	if running != nil && running.api != nil {
		running.api.Close()
	}
}

func (runner *faultKernelRunner) startDaemon(
	ctx context.Context, paths faultKernelTrialPaths, token []byte,
	outerID, cgroupRoot string, useManager bool,
) (*faultKernelRunningDaemon, faultKernelDaemonReceipt, error) {
	daemon, receipt, err := startFaultKernelDaemon(ctx, faultKernelDaemonConfig{
		BenchmarkExecutable: runner.platform.benchmarkExecutable,
		Arguments:           faultKernelDaemonArguments(paths, cgroupRoot),
		Driver:              runner.platform.supervisorDriver, AttemptID: outerID, UseAttemptManager: useManager,
	})
	if err != nil {
		return nil, faultKernelDaemonReceipt{}, err
	}
	api, err := newFaultKernelAPIClient(receipt.ListenAddress, token)
	if err != nil {
		abortFaultKernelDaemon(daemon)
		return nil, faultKernelDaemonReceipt{}, err
	}
	if err := waitFaultKernelCondition(ctx, func() (bool, error) {
		err := api.Ready(ctx)
		if errors.Is(err, errFaultKernelAPIRequest) {
			return false, nil
		}
		return err == nil, err
	}); err != nil {
		api.Close()
		abortFaultKernelDaemon(daemon)
		return nil, faultKernelDaemonReceipt{}, errors.Join(
			faultKernelDaemonFailure("api-ready"), err,
		)
	}
	return &faultKernelRunningDaemon{daemon: daemon, api: api}, receipt, nil
}
