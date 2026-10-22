//go:build linux

package benchmark

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/resourcecontrol"
)

const (
	resourceKernelNamespace      = "resource-smoke-v1"
	resourceKernelSampleInterval = 100 * time.Millisecond
)

type resourceKernelCase struct {
	caseID, controller, limitKind, fixture, unit string
	baseline, treatment                          resourcecontrol.Limits
	baselineLimit, treatmentLimit                int64
}

type resourceKernelWorkloadCase struct {
	CaseID           string `json:"case_id"`
	Controller       string `json:"controller"`
	LimitKind        string `json:"limit_kind"`
	Fixture          string `json:"fixture"`
	BaselineLimit    int64  `json:"baseline_limit"`
	TreatmentLimit   int64  `json:"treatment_limit"`
	TreatmentSwap    string `json:"treatment_swap"`
	FixtureValue     int64  `json:"fixture_value"`
	FixtureUnit      string `json:"fixture_unit"`
	SampleIntervalMS int64  `json:"sample_interval_ms"`
	Unit             string `json:"unit"`
}

func collectResourceKernelPlatform(ctx context.Context, options ResourceKernelCollectOptions) (RunSummary, error) {
	if _, err := validateOutputParent(options.OutputDir); err != nil {
		return RunSummary{}, err
	}
	root, err := resourcecontrol.ResolveSelfCgroupV2Root("/proc/self/cgroup", "/sys/fs/cgroup")
	if err != nil || !validFaultKernelInvocationID(os.Getenv("INVOCATION_ID")) {
		return RunSummary{}, ErrResourceKernelUnsupported
	}
	driver, err := resourcecontrol.PrepareDelegatedCgroupV2(root, resourceKernelNamespace, os.Getpid())
	if err != nil {
		return RunSummary{}, err
	}
	if err := driver.EnsureNamespaceEmpty(); err != nil {
		return RunSummary{}, err
	}
	environment, err := collectHostOpenEulerEnvironment(root)
	if err != nil {
		return RunSummary{}, err
	}
	deps := defaultCollectorDeps()
	deps.environment = func() (Environment, error) { return environment, nil }
	return collectExperiment(ctx, options.OutputDir, deps, func(ctx context.Context, runID string, now func() time.Time) (experimentEvidence, error) {
		evidence, buildErr := buildResourceKernelEvidence(ctx, runID, options.Plan, driver, now)
		cleanupErr := driver.EnsureNamespaceEmpty()
		return evidence, errors.Join(buildErr, cleanupErr)
	})
}

func buildResourceKernelEvidence(ctx context.Context, runID string, plan ResourceKernelPlan, driver *resourcecontrol.CgroupV2Driver, now func() time.Time) (experimentEvidence, error) {
	workload := struct {
		Schema string                       `json:"schema"`
		PlanID string                       `json:"plan_id"`
		Cases  []resourceKernelWorkloadCase `json:"cases"`
	}{
		Schema: "aegisrt.experiment.resource-workload/v1", PlanID: plan.PlanID, Cases: resourceKernelWorkloadCases(),
	}
	workloadJSON, err := marshalJSON(workload)
	if err != nil {
		return experimentEvidence{}, err
	}
	workloadHash := hashBytes(workloadJSON)
	executable, err := os.Executable()
	if err != nil {
		return experimentEvidence{}, err
	}
	psi, err := resourcecontrol.NewHostPSICollector()
	if err != nil {
		return experimentEvidence{}, err
	}
	rows := make([]ResourceKernelRow, 0, 6)
	trials := make([]trialObservation, 0, 6)
	for caseIndex, testCase := range resourceKernelCases() {
		for roleIndex, role := range []string{"baseline", "treatment"} {
			limits, requested, treatment := testCase.baseline, testCase.baselineLimit, "unrestricted"
			if role == "treatment" {
				limits, requested, treatment = testCase.treatment, testCase.treatmentLimit, testCase.limitKind
			}
			attemptID := fmt.Sprintf("case-%d-%s", caseIndex, role)
			started := now()
			row, runErr := runResourceKernelTrial(ctx, driver, executable, psi, attemptID, testCase, limits, requested)
			finished := now()
			row.SchemaVersion, row.RunID, row.EvidenceScope = ResourceKernelCSVSchemaV2, runID, ResourceKernelEvidenceScope
			row.CaseID, row.PairID, row.Seed, row.Repetition = testCase.caseID, "pair-"+strings.ToLower(testCase.caseID)+"-104729-000", resourceKernelSeed, 0
			row.TrialOrder, row.Treatment, row.Role, row.WorkloadSHA256 = roleIndex, treatment, role, workloadHash
			row.Backend, row.Controller, row.LimitKind, row.RequestedLimit, row.Unit = "cgroup-v2", testCase.controller, testCase.limitKind, requested, testCase.unit
			row.TimeoutMS, row.LedgerInvariant, row.Admitted = resourceKernelTrialTimeoutMS, true, 1
			if runErr != nil {
				row.Status, row.ContainmentResult, row.ReasonCode = "FAILED", "FAILED", "KERNEL_GATE_FAILED"
			} else {
				row.Status, row.ContainmentResult, row.ReasonCode = "SUCCEEDED", "PASSED", "NONE"
			}
			rows = append(rows, row)
			trials = append(trials, trialObservation{Status: row.Status, CaseID: row.CaseID, PairID: row.PairID, Seed: row.Seed, Repetition: 0, TrialOrder: roleIndex, Treatment: treatment, StartedAt: started, FinishedAt: finished})
		}
	}
	var csvData bytes.Buffer
	if err := writeResourceKernelCSV(&csvData, rows); err != nil {
		return experimentEvidence{}, err
	}
	return experimentEvidence{Kind: "resource", PlanID: plan.PlanID, EvidenceScope: ResourceKernelEvidenceScope,
		CaseIDs: append([]string(nil), plan.CaseIDs...), WorkloadSHA256: workloadHash, InputSHA256: workloadHash,
		Protocol:  ManifestProtocol{MeasuredPairs: 3, Seeds: append([]uint32(nil), plan.Seeds...), Pairing: "seed-and-repetition-v1", Counterbalance: "alternating-seed-index-v1", TrialTimeoutMS: plan.TrialTimeoutMS, BootstrapSeed: 424242, BootstrapResamples: 10000},
		Artifacts: []artifactPayload{{Name: "resource.csv", Kind: "resource-csv", MediaType: "text/csv", Records: len(rows), Data: csvData.Bytes()}, {Name: "workload.json", Kind: "workload-json", MediaType: "application/json", Records: 1, Data: workloadJSON}},
		Trials:    trials, RowCount: len(rows)}, nil
}

func resourceKernelCases() []resourceKernelCase {
	return []resourceKernelCase{
		{caseID: "R2", controller: "cpu", limitKind: "cpu.max", fixture: "cpu", unit: "millicpu", treatment: resourcecontrol.Limits{Resources: domain.ResourceVector{CPUMilli: 250}}, treatmentLimit: 250},
		{caseID: "R3-memory", controller: "memory", limitKind: "memory.max", fixture: "memory", unit: "bytes", treatment: resourcecontrol.Limits{Resources: domain.ResourceVector{MemoryMiB: 64}, DisableSwap: true}, treatmentLimit: 64 << 20},
		{caseID: "R3-pids", controller: "pids", limitKind: "pids.max", fixture: "pids", unit: "processes", treatment: resourcecontrol.Limits{PIDsMax: 12}, treatmentLimit: 12},
	}
}

func resourceKernelWorkloadCases() []resourceKernelWorkloadCase {
	cases := resourceKernelCases()
	result := make([]resourceKernelWorkloadCase, 0, len(cases))
	for _, item := range cases {
		var fixtureValue int64
		var fixtureUnit string
		switch item.fixture {
		case "cpu":
			fixtureValue, fixtureUnit = resourceCPUFixtureDuration.Milliseconds(), "cpu_milliseconds"
		case "memory":
			fixtureValue, fixtureUnit = resourceMemoryFixtureBytes, "bytes"
		case "pids":
			fixtureValue, fixtureUnit = resourcePIDFixtureChildren, "children"
		}
		swapPolicy := "unchanged"
		if item.treatment.DisableSwap {
			swapPolicy = "disabled"
		}
		result = append(result, resourceKernelWorkloadCase{CaseID: item.caseID, Controller: item.controller,
			LimitKind: item.limitKind, Fixture: item.fixture, BaselineLimit: item.baselineLimit,
			TreatmentLimit: item.treatmentLimit, TreatmentSwap: swapPolicy,
			FixtureValue: fixtureValue, FixtureUnit: fixtureUnit,
			SampleIntervalMS: resourceKernelSampleInterval.Milliseconds(), Unit: item.unit})
	}
	return result
}

func runResourceKernelTrial(parent context.Context, driver *resourcecontrol.CgroupV2Driver, executable string, psi *resourcecontrol.PSICollector, attemptID string, testCase resourceKernelCase, limits resourcecontrol.Limits, requested int64) (row ResourceKernelRow, resultErr error) {
	if _, err := driver.Create(attemptID, limits); err != nil {
		return row, err
	}
	defer func() { resultErr = errors.Join(resultErr, driver.Destroy(attemptID)) }()
	ctx, cancel := context.WithTimeout(parent, resourceKernelTrialTimeoutMS*time.Millisecond)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "resource-fixture", testCase.fixture)
	placement, err := driver.PlaceCommandInExistingGroup(attemptID, command)
	if err != nil {
		return row, err
	}
	started := time.Now()
	if err := command.Start(); err != nil {
		_ = placement.Close()
		return row, err
	}
	_ = placement.Close()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	peakMemory, peakPIDs, waitErr := sampleResourceProcess(ctx, driver, attemptID, waitDone)
	finished := time.Now()
	stats, err := readResourceKernelStats(driver, attemptID)
	if err != nil {
		return row, err
	}
	pressure, err := psi.Collect()
	if err != nil {
		return row, err
	}
	elapsed := finished.Sub(started)
	row.ElapsedMS = float64(elapsed.Microseconds()) / 1000
	row.ThrottleEvents, row.OOMEvents, row.PIDLimitEvents = stats.throttled, stats.oomKills, stats.pidMax
	row.PressureSomeAvg10, row.PressureFullAvg10 = pressureForController(pressure, testCase.controller)
	switch testCase.controller {
	case "cpu":
		row.ObservedValue = resourceKernelCPUAverageMilli(stats.cpuUsage, elapsed)
		row.ObservationStatistic = "mean_over_trial"
	case "memory":
		row.ObservedValue = resourceMaxInt64(peakMemory, stats.memoryPeak)
		row.ObservationStatistic = "peak"
	case "pids":
		row.ObservedValue = peakPIDs
		row.ObservationStatistic = "peak"
	}
	return row, validateResourceKernelTrialResult(testCase.controller, requested, stats, waitErr)
}

func sampleResourceProcess(ctx context.Context, driver *resourcecontrol.CgroupV2Driver, attemptID string, waitDone <-chan error) (int64, int64, error) {
	var peakMemory, peakPIDs int64
	ticker := time.NewTicker(resourceKernelSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return peakMemory, peakPIDs, ctx.Err()
		case waitErr := <-waitDone:
			return peakMemory, peakPIDs, waitErr
		case <-ticker.C:
			stats, err := readResourceKernelStats(driver, attemptID)
			if err != nil {
				continue
			}
			peakMemory = resourceMaxInt64(peakMemory, stats.memoryCurrent)
			peakPIDs = resourceMaxInt64(peakPIDs, stats.pidsCurrent)
		}
	}
}

func resourceKernelCPUAverageMilli(usageMicros int64, elapsed time.Duration) int64 {
	elapsedMicros := elapsed.Microseconds()
	if usageMicros <= 0 || elapsedMicros <= 0 {
		return 0
	}
	return usageMicros/elapsedMicros*1000 + usageMicros%elapsedMicros*1000/elapsedMicros
}

type resourceKernelStats struct{ cpuUsage, throttled, memoryCurrent, memoryPeak, oomKills, pidsCurrent, pidMax int64 }

func readResourceKernelStats(driver *resourcecontrol.CgroupV2Driver, attemptID string) (resourceKernelStats, error) {
	observation, err := driver.Observe(attemptID)
	if err != nil || !observation.Exists {
		return resourceKernelStats{}, errors.New("attempt cgroup is unavailable")
	}
	counters, err := driver.Counters(attemptID)
	if err != nil {
		return resourceKernelStats{}, err
	}
	return resourceKernelStats{cpuUsage: counters.CPUUsageMicros, memoryCurrent: counters.MemoryCurrentBytes,
		throttled: counters.CPUThrottledEvents, memoryPeak: counters.MemoryPeakBytes,
		oomKills: counters.OOMKillEvents, pidsCurrent: counters.PIDsCurrent, pidMax: counters.PIDLimitEvents}, nil
}

func pressureForController(pressure resourcecontrol.SystemPressure, controller string) (float64, float64) {
	sample := pressure.CPU
	if controller == "memory" {
		sample = pressure.Memory
	}
	var some, full float64
	if sample.Some != nil {
		some = sample.Some.Avg10
	}
	if sample.Full != nil {
		full = sample.Full.Avg10
	}
	return some, full
}
func resourceMaxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
