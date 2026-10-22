package benchmark

import (
	"context"
	"errors"
	"fmt"
	"time"

	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
)

func runContainedFaultTrial(
	ctx context.Context,
	workload faultCaseWorkload,
	mode faultTrialMode,
	command FaultAgentCommand,
) (trace FaultTrialTrace, resultErr error) {
	readyDir, err := newFaultReadyDirectory()
	if err != nil {
		return FaultTrialTrace{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, cleanupFaultReadyDirectory(readyDir, workload)) }()
	targetMode := "success"
	targetHold := time.Duration(0)
	targetTimeout := time.Duration(faultNormalTimeoutMS) * time.Millisecond
	switch mode.faultMode {
	case "none":
	case "agent_exit":
		targetMode = "exit"
	case "agent_timeout":
		targetHold = time.Duration(workload.TargetHoldMS) * time.Millisecond
		targetTimeout = time.Duration(workload.TargetTimeoutMS) * time.Millisecond
	default:
		return FaultTrialTrace{}, fmt.Errorf("unsupported contained fault mode %q", mode.faultMode)
	}
	runtime, err := newFaultRuntime()
	if err != nil {
		return FaultTrialTrace{}, err
	}
	recorder := newFaultArtifactRecorder()
	coordinator, err := newFaultCoordinator(runtime, workload, command, targetMode, targetHold, targetTimeout, readyDir, recorder)
	if err != nil {
		return FaultTrialTrace{}, err
	}
	origin := time.Now()
	if err := coordinator.ApplyPatch(faultTasks(workload), time.Now().UTC()); err != nil {
		return FaultTrialTrace{}, err
	}
	targetResult, targetErr := coordinator.Tick(ctx, time.Now().UTC())
	detectedNS := time.Since(origin).Nanoseconds()
	if err := validateContainedFaultOutcome(mode.faultMode, targetResult, targetErr); err != nil {
		return FaultTrialTrace{}, err
	}
	if err := waitFaultAgentReady(ctx, readyDir, workload.Target.TaskID, 1); err != nil {
		return FaultTrialTrace{}, fmt.Errorf("target process readiness: %w", err)
	}
	if targetResult.Decision.Candidate.TaskID != workload.Target.TaskID {
		return FaultTrialTrace{}, fmt.Errorf("fault target was not scheduled first")
	}
	survivorResult, err := coordinator.Tick(ctx, time.Now().UTC())
	if err != nil {
		return FaultTrialTrace{}, fmt.Errorf("run unaffected task: %w", err)
	}
	if survivorResult.Decision.Candidate.TaskID != workload.Unaffected.TaskID {
		return FaultTrialTrace{}, fmt.Errorf("unexpected unaffected task %q", survivorResult.Decision.Candidate.TaskID)
	}
	if err := waitFaultAgentReady(ctx, readyDir, workload.Unaffected.TaskID, 1); err != nil {
		return FaultTrialTrace{}, fmt.Errorf("unaffected process readiness: %w", err)
	}
	finishedNS := time.Since(origin).Nanoseconds()
	results := []controlplane.TickResult{targetResult, survivorResult}
	target, err := faultTaskTrace(runtime, workload.Target, results)
	if err != nil {
		return FaultTrialTrace{}, err
	}
	survivor, err := faultTaskTrace(runtime, workload.Unaffected, results)
	if err != nil {
		return FaultTrialTrace{}, err
	}
	publications, duplicates := recorder.snapshot()
	trace = FaultTrialTrace{
		ClockMode: faultClockMode, TimeoutMS: workload.TrialTimeoutMS,
		Protocol:   containedFaultProtocol(mode.faultMode),
		Milestones: FaultMilestonesTrace{CleanupNS: int64Pointer(finishedNS), FinishedNS: finishedNS},
		Target:     target, UnaffectedTasks: []FaultTaskTrace{survivor}, Publications: publications,
		DuplicateArtifacts: duplicates,
		Cleanup:            faultCleanup(results, runtime.Stats().Allocated.IsZero()),
		Recovery:           FaultRecoveryTrace{CheckpointBackend: "none", RetryCount: 0},
		ObservedReasonCode: "NONE", ReasonCode: "NONE",
	}
	if mode.faultMode != "none" {
		injectedAt := targetResult.Execution.Process.ProcessStartedAt
		if mode.faultMode == "agent_timeout" {
			injectedAt = targetResult.Execution.Process.DeadlineAt
		}
		injectedNS := injectedAt.Sub(origin).Nanoseconds()
		if injectedAt.IsZero() || injectedNS < 0 || detectedNS < injectedNS {
			return FaultTrialTrace{}, fmt.Errorf("fault injection timestamp is outside the trial")
		}
		trace.Milestones.InjectedNS = int64Pointer(injectedNS)
		trace.Milestones.DetectedNS = int64Pointer(detectedNS)
		trace.Milestones.RecoveredNS = int64Pointer(detectedNS)
		trace.ObservedReasonCode = faultModeReason(mode.faultMode)
	}
	return trace, nil
}

func validateContainedFaultOutcome(
	faultMode string,
	result controlplane.TickResult,
	err error,
) error {
	switch faultMode {
	case "none":
		if err != nil || result.Execution.Attempt.Status != domain.AttemptStatusSucceeded {
			return fmt.Errorf("baseline execution failed: %w", err)
		}
	case "agent_exit":
		if !errors.Is(err, executor.ErrExit) || result.Execution.Attempt.Status != domain.AttemptStatusFailed ||
			result.Execution.Process.ExitCode != 7 || result.Execution.Process.TimedOut {
			return fmt.Errorf("non-zero exit was not contained as declared")
		}
	case "agent_timeout":
		if (!errors.Is(err, executor.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded)) ||
			result.Execution.Attempt.Status != domain.AttemptStatusFailed || !result.Execution.Process.TimedOut ||
			result.Execution.Process.Canceled {
			return fmt.Errorf("deadline overrun was not classified as timeout")
		}
	default:
		return fmt.Errorf("unsupported contained fault mode %q", faultMode)
	}
	return nil
}

func containedFaultProtocol(faultMode string) FaultProtocolTrace {
	protocol := FaultProtocolTrace{
		FaultMode: faultMode, IsolationMode: "tracked-child-process",
		InjectionBoundary: "matched-dispatch-control", ExpectedRecovery: "normal-completion",
	}
	switch faultMode {
	case "agent_exit":
		protocol.InjectionBoundary = "agent-process-start"
		protocol.ExpectedRecovery = "contained-terminal-failure"
	case "agent_timeout":
		protocol.InjectionBoundary = "configured-agent-deadline"
		protocol.ExpectedRecovery = "contained-terminal-failure"
	}
	return protocol
}

func faultCleanup(results []controlplane.TickResult, resourcesReleased bool) FaultCleanupTrace {
	started, reaped := 0, 0
	for _, result := range results {
		if result.Execution.Process.ProcessStarted {
			started++
		}
		if result.Execution.Process.ProcessStarted && result.Execution.Process.WaitCompleted {
			reaped++
		}
	}
	orphans := started - reaped
	return FaultCleanupTrace{
		CleanupObserved: true, ResourcesReleased: resourcesReleased,
		ChildProcessesStarted: started, ChildProcessesReaped: reaped, OrphanProcesses: intPointer(orphans),
		OrphanCgroups: nil,
	}
}

func faultModeReason(mode string) string {
	switch mode {
	case "agent_exit":
		return "AGENT_EXIT"
	case "agent_timeout":
		return "AGENT_TIMEOUT"
	case "runservice_graceful_restart":
		return "RUNSERVICE_GRACEFUL_RESTART"
	default:
		return "NONE"
	}
}
