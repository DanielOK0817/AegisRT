package benchmark

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/runservice"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/statefile"
)

type faultOutcomeObserver struct {
	mu      sync.Mutex
	results []controlplane.TickResult
}

func (o *faultOutcomeObserver) Observe(outcome runservice.Outcome) error {
	if outcome.Result.Execution.Attempt.ID == "" {
		return nil
	}
	o.mu.Lock()
	o.results = append(o.results, outcome.Result)
	o.mu.Unlock()
	return nil
}

func (o *faultOutcomeObserver) snapshot() []controlplane.TickResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]controlplane.TickResult(nil), o.results...)
}

func runServiceRestartFaultTrial(
	ctx context.Context,
	workload faultCaseWorkload,
	mode faultTrialMode,
	command FaultAgentCommand,
) (trace FaultTrialTrace, resultErr error) {
	cleanupSafe := true
	directory, statePath, err := newFaultStatePath()
	if err != nil {
		return FaultTrialTrace{}, err
	}
	defer func() {
		if cleanupSafe {
			resultErr = errors.Join(resultErr, cleanupFaultState(directory, statePath))
		}
	}()
	readyDir, err := newFaultReadyDirectory()
	if err != nil {
		return FaultTrialTrace{}, err
	}
	defer func() {
		if cleanupSafe {
			resultErr = errors.Join(resultErr, cleanupFaultReadyDirectory(readyDir, workload))
		}
	}()
	store, err := statefile.OpenExclusive(statePath)
	if err != nil {
		return FaultTrialTrace{}, err
	}
	defer func() {
		if cleanupSafe {
			resultErr = errors.Join(resultErr, store.Close())
		}
	}()
	runtime, report, err := openFaultPersistentRuntime(store)
	if err != nil {
		return FaultTrialTrace{}, err
	}
	if report.Loaded || report.Recovered {
		return FaultTrialTrace{}, fmt.Errorf("new fault state unexpectedly loaded")
	}
	recorder := newFaultArtifactRecorder()
	coordinator, err := newFaultCoordinator(
		runtime, workload, command, "success", time.Duration(workload.TargetHoldMS)*time.Millisecond,
		time.Duration(faultNormalTimeoutMS)*time.Millisecond, readyDir, recorder,
	)
	if err != nil {
		return FaultTrialTrace{}, err
	}
	if err := coordinator.ApplyPatch(faultTasks(workload), time.Now().UTC()); err != nil {
		return FaultTrialTrace{}, err
	}
	origin := time.Now()
	firstObserver := &faultOutcomeObserver{}
	firstService, err := newFaultRunService(coordinator, firstObserver)
	if err != nil {
		return FaultTrialTrace{}, err
	}
	firstCtx, firstCancel := context.WithCancel(ctx)
	firstDone := startFaultService(firstCtx, firstService)
	defer func() {
		if firstDone != nil {
			if stopErr := stopFaultService(firstCancel, firstDone); stopErr != nil {
				cleanupSafe = false
				resultErr = errors.Join(resultErr, stopErr)
			}
		}
	}()

	milestones := FaultMilestonesTrace{}
	recovery := FaultRecoveryTrace{CheckpointBackend: "statefile-v1"}
	var results []controlplane.TickResult
	if mode.faultMode == "none" {
		if err := waitFaultTasksTerminal(ctx, runtime, workload, domain.TaskStatusSucceeded); err != nil {
			return FaultTrialTrace{}, err
		}
		stopErr := stopFaultService(firstCancel, firstDone)
		if stopErr != nil {
			cleanupSafe = false
			return FaultTrialTrace{}, stopErr
		}
		firstDone = nil
		if err := waitFaultAgentReady(ctx, readyDir, workload.Target.TaskID, 1); err != nil {
			return FaultTrialTrace{}, err
		}
		if err := waitFaultAgentReady(ctx, readyDir, workload.Unaffected.TaskID, 1); err != nil {
			return FaultTrialTrace{}, err
		}
		results = firstObserver.snapshot()
	} else if mode.faultMode == "runservice_graceful_restart" {
		if err := waitFaultTaskStatus(ctx, runtime, workload.Target.TaskID, domain.TaskStatusRunning); err != nil {
			return FaultTrialTrace{}, err
		}
		if err := waitFaultAgentReady(ctx, readyDir, workload.Target.TaskID, 1); err != nil {
			return FaultTrialTrace{}, fmt.Errorf("wait first Agent readiness: %w", err)
		}
		readyNS := time.Since(origin).Nanoseconds()
		milestones.AgentReadyNS = int64Pointer(readyNS)
		injectedNS := time.Since(origin).Nanoseconds()
		milestones.InjectedNS = int64Pointer(injectedNS)
		firstCancel()
		if err := waitFaultTaskStatus(ctx, runtime, workload.Target.TaskID, domain.TaskStatusReady); err != nil {
			return FaultTrialTrace{}, err
		}
		if err := verifyFaultRequeued(runtime, workload.Target.TaskID); err != nil {
			return FaultTrialTrace{}, err
		}
		requeuedNS := time.Since(origin).Nanoseconds()
		milestones.DetectedNS = int64Pointer(requeuedNS)
		milestones.RequeuedNS = int64Pointer(requeuedNS)
		recovery.Requeued = true
		stopErr := stopFaultService(firstCancel, firstDone)
		if stopErr != nil {
			cleanupSafe = false
			return FaultTrialTrace{}, stopErr
		}
		firstDone = nil
		firstResults := firstObserver.snapshot()
		if err := store.Close(); err != nil {
			return FaultTrialTrace{}, err
		}
		store, err = statefile.OpenExclusive(statePath)
		if err != nil {
			return FaultTrialTrace{}, err
		}
		runtime, report, err = openFaultPersistentRuntime(store)
		if err != nil {
			return FaultTrialTrace{}, err
		}
		if !report.Loaded || report.Recovered || runtime.AttemptNumber(workload.Target.TaskID) != 1 {
			return FaultTrialTrace{}, fmt.Errorf("graceful restart did not load canonical ready state")
		}
		recovery.CheckpointLoaded = true
		recovery.RuntimeReopened = true
		recovery.RecoveryPerformedOnOpen = false
		recovery.RetryCount = 1
		coordinator, err = newFaultCoordinator(
			runtime, workload, command, "success", time.Duration(workload.TargetHoldMS)*time.Millisecond,
			time.Duration(faultNormalTimeoutMS)*time.Millisecond, readyDir, recorder,
		)
		if err != nil {
			return FaultTrialTrace{}, err
		}
		secondObserver := &faultOutcomeObserver{}
		secondService, err := newFaultRunService(coordinator, secondObserver)
		if err != nil {
			return FaultTrialTrace{}, err
		}
		secondCtx, secondCancel := context.WithCancel(ctx)
		secondDone := startFaultService(secondCtx, secondService)
		defer func() {
			if secondDone != nil {
				if stopErr := stopFaultService(secondCancel, secondDone); stopErr != nil {
					cleanupSafe = false
					resultErr = errors.Join(resultErr, stopErr)
				}
			}
		}()
		if err := waitFaultTaskAttemptStatus(ctx, runtime, workload.Target.TaskID, 2, domain.TaskStatusRunning); err != nil {
			return FaultTrialTrace{}, err
		}
		if err := waitFaultAgentReady(ctx, readyDir, workload.Target.TaskID, 2); err != nil {
			return FaultTrialTrace{}, fmt.Errorf("wait resumed Agent readiness: %w", err)
		}
		resumedNS := time.Since(origin).Nanoseconds()
		milestones.ResumedNS = int64Pointer(resumedNS)
		if err := waitFaultTaskStatus(ctx, runtime, workload.Target.TaskID, domain.TaskStatusSucceeded); err != nil {
			return FaultTrialTrace{}, err
		}
		recoveredNS := time.Since(origin).Nanoseconds()
		milestones.RecoveredNS = int64Pointer(recoveredNS)
		if err := waitFaultTasksTerminal(ctx, runtime, workload, domain.TaskStatusSucceeded); err != nil {
			return FaultTrialTrace{}, err
		}
		stopErr = stopFaultService(secondCancel, secondDone)
		if stopErr != nil {
			cleanupSafe = false
			return FaultTrialTrace{}, stopErr
		}
		secondDone = nil
		if err := waitFaultAgentReady(ctx, readyDir, workload.Unaffected.TaskID, 1); err != nil {
			return FaultTrialTrace{}, err
		}
		results = append(firstResults, secondObserver.snapshot()...)
	} else {
		return FaultTrialTrace{}, fmt.Errorf("unsupported service fault mode %q", mode.faultMode)
	}

	finishedNS := time.Since(origin).Nanoseconds()
	milestones.CleanupNS = int64Pointer(finishedNS)
	milestones.FinishedNS = finishedNS
	target, err := faultTaskTrace(runtime, workload.Target, results)
	if err != nil {
		return FaultTrialTrace{}, err
	}
	survivor, err := faultTaskTrace(runtime, workload.Unaffected, results)
	if err != nil {
		return FaultTrialTrace{}, err
	}
	publications, duplicates := recorder.snapshot()
	return FaultTrialTrace{
		ClockMode: faultClockMode, TimeoutMS: workload.TrialTimeoutMS,
		Protocol: serviceFaultProtocol(mode.faultMode), Milestones: milestones,
		Target: target, UnaffectedTasks: []FaultTaskTrace{survivor}, Publications: publications,
		DuplicateArtifacts: duplicates, Cleanup: faultCleanup(results, runtime.Stats().Allocated.IsZero()),
		Recovery: recovery, ObservedReasonCode: faultModeReason(mode.faultMode), ReasonCode: "NONE",
	}, nil
}

func openFaultPersistentRuntime(store *statefile.Store) (*runtimecore.Runtime, runtimecore.RecoveryReport, error) {
	policy, err := scheduler.NewDAGDRCFS(faultCapacity, time.Millisecond)
	if err != nil {
		return nil, runtimecore.RecoveryReport{}, err
	}
	return runtimecore.NewPersistent(policy, faultCapacity, "fault-smoke-config-v1", store, time.Now().UTC())
}

func newFaultRunService(coordinator *controlplane.Coordinator, observer *faultOutcomeObserver) (*runservice.Service, error) {
	return runservice.New(coordinator, observer, runservice.Config{PollInterval: 2 * time.Millisecond, MaxConcurrent: 1})
}

func startFaultService(ctx context.Context, service *runservice.Service) <-chan error {
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	return done
}

func stopFaultService(cancel context.CancelFunc, done <-chan error) error {
	return stopFaultServiceWithin(cancel, done, 10*time.Second)
}

func stopFaultServiceWithin(cancel context.CancelFunc, done <-chan error, timeout time.Duration) error {
	cancel()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("fault service did not stop within cleanup deadline")
	}
}

func waitFaultTasksTerminal(ctx context.Context, runtime *runtimecore.Runtime, workload faultCaseWorkload, status domain.TaskStatus) error {
	return waitFaultCondition(ctx, func() bool {
		target, targetExists := runtime.Task(workload.Target.TaskID)
		survivor, survivorExists := runtime.Task(workload.Unaffected.TaskID)
		return targetExists && survivorExists && target.Status == status && survivor.Status == status
	})
}

func waitFaultTaskStatus(ctx context.Context, runtime *runtimecore.Runtime, taskID string, status domain.TaskStatus) error {
	return waitFaultCondition(ctx, func() bool {
		snapshot, exists := runtime.Task(taskID)
		return exists && snapshot.Status == status
	})
}

func waitFaultTaskAttemptStatus(
	ctx context.Context,
	runtime *runtimecore.Runtime,
	taskID string,
	attempt int,
	status domain.TaskStatus,
) error {
	return waitFaultCondition(ctx, func() bool {
		snapshot, exists := runtime.Task(taskID)
		return exists && snapshot.Status == status && runtime.AttemptNumber(taskID) == attempt
	})
}

func waitFaultCondition(ctx context.Context, condition func() bool) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func verifyFaultRequeued(runtime *runtimecore.Runtime, taskID string) error {
	snapshot, exists := runtime.Task(taskID)
	if !exists || snapshot.Status != domain.TaskStatusReady || runtime.AttemptNumber(taskID) != 1 || !runtime.Stats().Allocated.IsZero() {
		return fmt.Errorf("graceful stop did not requeue and release the target")
	}
	return nil
}

func serviceFaultProtocol(mode string) FaultProtocolTrace {
	if mode == "none" {
		return FaultProtocolTrace{
			FaultMode: "none", InjectionBoundary: "matched-service-control",
			IsolationMode: "tracked-child-process", ExpectedRecovery: "normal-completion",
		}
	}
	return FaultProtocolTrace{
		FaultMode: "runservice_graceful_restart", InjectionBoundary: "active-attempt-root-cancel",
		IsolationMode: "tracked-child-process", ExpectedRecovery: "requeue-then-complete",
	}
}

func newFaultStatePath() (string, string, error) {
	directory, err := os.MkdirTemp("", "aegisrt-fault-")
	if err != nil {
		return "", "", err
	}
	return directory, filepath.Join(directory, "runtime.state"), nil
}

func cleanupFaultState(directory, statePath string) error {
	return errors.Join(
		removeIfPresent(statePath),
		removeIfPresent(statePath+".lock"),
		os.Remove(directory),
	)
}
