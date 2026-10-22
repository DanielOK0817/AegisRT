//go:build linux

package benchmark

import (
	"context"
	"errors"
	"time"

	"aegisrt/internal/domain"
)

func collectFaultKernelPlatform(
	ctx context.Context, options FaultKernelCollectOptions,
) (summary RunSummary, resultErr error) {
	if _, err := validateOutputParent(options.OutputDir); err != nil {
		return RunSummary{}, err
	}
	if err := ValidateFaultKernelPlan(options.Plan); err != nil {
		return RunSummary{}, err
	}
	platform, err := prepareFaultKernelPlatform(options.Plan.Profile == FaultKernelProfileFormal)
	if err != nil {
		return RunSummary{}, err
	}
	cleanupPending := true
	cleanup := func() error {
		if !cleanupPending {
			return nil
		}
		cleanupPending = false
		if err := platform.cleanup(); err != nil {
			return faultKernelDaemonFailure("supervisor-cleanup")
		}
		return nil
	}
	defer func() {
		if err := cleanup(); err != nil {
			summary = RunSummary{}
			resultErr = errors.Join(resultErr, err)
		}
	}()
	deps := defaultCollectorDeps()
	environment := platform.environment
	environment.Cgroup.Controllers = append([]string(nil), environment.Cgroup.Controllers...)
	deps.environment = func() (Environment, error) { return environment, nil }
	software := platform.software
	deps.software = func(string) (Software, error) { return software, nil }
	return collectExperiment(ctx, options.OutputDir, deps, func(
		ctx context.Context, runID string, _ func() time.Time,
	) (experimentEvidence, error) {
		evidence, buildErr := collectFaultKernelEvidence(ctx, runID, options.Plan, platform)
		cleanupErr := cleanup()
		if buildErr != nil || cleanupErr != nil {
			return experimentEvidence{}, errors.Join(buildErr, cleanupErr)
		}
		return evidence, nil
	})
}

func collectFaultKernelEvidence(
	ctx context.Context, runID string, plan FaultKernelPlan, platform *faultKernelPlatform,
) (experimentEvidence, error) {
	workload, cells, err := newFaultKernelWorkload(plan)
	if err != nil {
		return experimentEvidence{}, err
	}
	workloadJSON, err := marshalFaultKernelWorkload(workload)
	if err != nil {
		return experimentEvidence{}, err
	}
	components, _, _, err := buildFaultKernelComponentManifest(
		platform.benchmarkExecutable, platform.daemonExecutable, platform.software.RuntimeCommit,
	)
	if err != nil {
		return experimentEvidence{}, err
	}
	if !faultKernelHarnessMatchesSoftware(components, platform.software) {
		return experimentEvidence{}, faultKernelDaemonFailure("component-software-identity")
	}
	componentJSON, err := canonicalJSON(components)
	if err != nil {
		return experimentEvidence{}, err
	}
	configHash, err := faultKernelRuntimeConfigHash(workload.RuntimeConfig)
	if err != nil {
		return experimentEvidence{}, err
	}
	input, err := NewFaultKernelInput(
		plan, hashBytes(workloadJSON), hashBytes(componentJSON), configHash,
	)
	if err != nil {
		return experimentEvidence{}, err
	}
	inputJSON, err := canonicalJSON(input)
	if err != nil {
		return experimentEvidence{}, err
	}
	session, err := createFaultKernelSession(platform.runtimeDirectory, runID)
	if err != nil {
		return experimentEvidence{}, err
	}
	runner := faultKernelRunner{platform: platform, runID: runID, plan: plan,
		workloadHash: input.WorkloadSHA256, inputHash: hashBytes(inputJSON), session: session}
	trials, err := runner.run(ctx, cells)
	if err != nil {
		return experimentEvidence{}, err
	}
	return buildFaultKernelExperimentEvidence(faultKernelEvidenceBundle{
		RunID: runID, Plan: plan, Input: input, WorkloadJSON: workloadJSON,
		Components: components, Trials: trials,
	})
}

func faultKernelHarnessMatchesSoftware(components FaultKernelComponentManifest, software Software) bool {
	for _, component := range components.Components {
		if component.Role == FaultKernelComponentHarness {
			return component.SHA256 == software.BinarySHA256
		}
	}
	return false
}

func faultKernelCellModes(seedIndex int) []faultKernelRunMode {
	baseline := faultKernelRunMode{role: "baseline", treatment: "no-injection",
		protocol: FaultKernelTraceProtocol{FaultMode: "none", InjectionBoundary: "matched-attempt-control",
			IsolationMode: FaultKernelIsolationMode, DaemonControl: FaultKernelDaemonControl,
			ExpectedRecovery: "attempt1-normal-completion"}}
	treatment := faultKernelRunMode{role: "treatment", treatment: "daemon-sigkill",
		protocol: FaultKernelTraceProtocol{FaultMode: "daemon_sigkill", InjectionBoundary: "attempt1-ready-and-populated",
			IsolationMode: FaultKernelIsolationMode, DaemonControl: FaultKernelDaemonControl,
			ExpectedRecovery: "restart-cleanup-requeue-attempt2"}}
	if seedIndex%2 == 0 {
		baseline.order, treatment.order = 0, 1
		return []faultKernelRunMode{baseline, treatment}
	}
	treatment.order, baseline.order = 0, 1
	return []faultKernelRunMode{treatment, baseline}
}

func faultKernelSeedIndex(plan FaultKernelPlan, phase string, seed uint32) int {
	seeds := plan.Protocol.MeasuredSeeds
	if phase == "warmup" {
		seeds = plan.Protocol.WarmupSeeds
	}
	for index, candidate := range seeds {
		if candidate == seed {
			return index
		}
	}
	return -1
}

func (runner *faultKernelRunner) baseTrace(cell faultKernelCell, mode faultKernelRunMode) FaultKernelTrialTrace {
	return FaultKernelTrialTrace{
		SchemaVersion: FaultKernelTrialTraceSchemaV1, RunID: runner.runID,
		EvidenceScope: FaultKernelEvidenceScope, Profile: runner.plan.Profile,
		PlanID: runner.plan.PlanID, CaseID: runner.plan.CaseID, PairID: cell.PairID,
		Seed: cell.Seed, Repetition: cell.Repetition, TrialOrder: mode.order, Phase: cell.Phase,
		Treatment: mode.treatment, Role: mode.role, WorkloadSHA256: runner.workloadHash,
		InputSHA256: runner.inputHash, Status: "SUCCEEDED", TimeoutMS: runner.plan.Protocol.TrialTimeoutMS,
		ClockMode: FaultKernelClockMode, Protocol: mode.protocol,
		Milestones: FaultKernelMilestones{StartedNS: 0}, ReasonCode: "NONE",
	}
}

func validFaultKernelInitialReceipt(receipt faultKernelDaemonReceipt) bool {
	return receipt.RuntimeNamespace != "" && !receipt.StateLoaded && !receipt.RecoveryPerformed &&
		receipt.RequeuedCount == 0 && receipt.FailedCount == 0 && receipt.CanceledCount == 0
}

func validFaultKernelRecoveryReceipt(receipt faultKernelDaemonReceipt) bool {
	return receipt.StateLoaded && receipt.RecoveryPerformed && receipt.RequeuedCount == 1 &&
		receipt.FailedCount == 0 && receipt.CanceledCount == 0
}

func waitFaultKernelCondition(ctx context.Context, condition func() (bool, error)) error {
	ticker := time.NewTicker(faultKernelPollInterval)
	defer ticker.Stop()
	for {
		ready, err := condition()
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type faultKernelArtifactCounts struct {
	target   int
	survivor int
}

func (counts faultKernelArtifactCounts) duplicates() int {
	return counts.target - 1 + counts.survivor - 1
}

func waitFaultKernelSuccess(
	ctx context.Context, api faultKernelRuntimeClient, cell faultKernelCell,
) (faultKernelArtifactCounts, error) {
	var previous faultKernelArtifactCounts
	havePrevious := false
	err := waitFaultKernelCondition(ctx, func() (bool, error) {
		statuses, err := api.TaskStatuses(ctx)
		if err != nil {
			return false, err
		}
		if len(statuses) != 2 || statuses[cell.Target.ID] != domain.TaskStatusSucceeded ||
			statuses[cell.Survivor.ID] != domain.TaskStatusSucceeded {
			return false, nil
		}
		current, err := readFaultKernelArtifactCounts(ctx, api, cell)
		if err != nil {
			return false, err
		}
		if current.target < 1 || current.survivor < 1 {
			return false, nil
		}
		stable := havePrevious && current == previous
		previous, havePrevious = current, true
		return stable, nil
	})
	return previous, err
}

func readFaultKernelArtifactCounts(
	ctx context.Context, api faultKernelRuntimeClient, cell faultKernelCell,
) (faultKernelArtifactCounts, error) {
	target, err := api.OutputArtifactCount(ctx, cell.Target.ID)
	if err != nil {
		return faultKernelArtifactCounts{}, err
	}
	survivor, err := api.OutputArtifactCount(ctx, cell.Survivor.ID)
	return faultKernelArtifactCounts{target: target, survivor: survivor}, err
}

func faultKernelSuccessfulTarget(retried bool) FaultKernelTargetTrace {
	attempts := []FaultKernelAttemptTrace{{1, true, "SUCCEEDED", 1, "NONE"}}
	if retried {
		attempts = []FaultKernelAttemptTrace{{1, true, "LOST", 0, "RUNTIME_INTERRUPTED"},
			{2, true, "SUCCEEDED", 1, "NONE"}}
	}
	return FaultKernelTargetTrace{AttemptBefore: 1, AttemptAfter: len(attempts), Attempts: attempts, FinalStatus: "SUCCEEDED"}
}

func elapsedFaultKernelNS(started time.Time) int64 { return time.Since(started).Nanoseconds() }
func faultKernelNSPointer(value int64) *int64      { return &value }
func faultKernelReceiptPointer(value FaultKernelStartupReceipt) *FaultKernelStartupReceipt {
	return &value
}
func clearFaultKernelToken(token []byte) {
	for index := range token {
		token[index] = 0
	}
}
