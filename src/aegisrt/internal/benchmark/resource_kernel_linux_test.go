//go:build linux

package benchmark

import (
	"errors"
	"testing"
	"time"
)

func TestResourceKernelPressureCasesKeepBaselineUnrestricted(t *testing.T) {
	cases := resourceKernelCases()
	if len(cases) != 3 {
		t.Fatalf("case count = %d", len(cases))
	}
	pids := cases[2]
	if pids.baseline.PIDsMax != 0 || pids.baselineLimit != 0 || pids.treatment.PIDsMax != 12 {
		t.Fatalf("unexpected PID limits: %+v", pids)
	}
	if resourceMemoryFixtureBytes < 4*(64<<20) {
		t.Fatalf("memory fixture is too small for the fixed ceiling: %d", resourceMemoryFixtureBytes)
	}
	if !cases[1].treatment.DisableSwap {
		t.Fatal("memory treatment must disable swap")
	}
	if resourceCPUFixtureDuration*5 >= resourceKernelTrialTimeoutMS*1e6 {
		t.Fatal("CPU fixture leaves no timeout headroom at the fixed quota")
	}
	workloads := resourceKernelWorkloadCases()
	if workloads[1].TreatmentSwap != "disabled" || workloads[0].FixtureValue <= 0 {
		t.Fatalf("workload omits fixed controls: %+v", workloads)
	}
	if resourceKernelSampleInterval != 100*1e6 || workloads[0].SampleIntervalMS != 100 {
		t.Fatal("CPU sample interval must match the fixed cpu.max period")
	}
	if workloads[0].FixtureUnit != "cpu_milliseconds" {
		t.Fatalf("CPU workload unit = %q", workloads[0].FixtureUnit)
	}
}

func TestResourceKernelPIDGateAcceptsExpectedFixtureFailure(t *testing.T) {
	exitErr := errors.New("fixture reached process ceiling")
	if err := validateResourceKernelTrialResult("pids", 12, resourceKernelStats{pidMax: 1}, exitErr); err != nil {
		t.Fatalf("PID ceiling event was rejected: %v", err)
	}
	if err := validateResourceKernelTrialResult("pids", 12, resourceKernelStats{}, nil); err == nil {
		t.Fatal("missing PID ceiling event was accepted")
	}
	if err := validateResourceKernelTrialResult("cpu", 250, resourceKernelStats{throttled: 1}, exitErr); err == nil {
		t.Fatal("unexpected CPU fixture failure was accepted")
	}
}

func TestResourceKernelCPUAverageUsesWholeTrial(t *testing.T) {
	if got := resourceKernelCPUAverageMilli(750_000, 3*time.Second); got != 250 {
		t.Fatalf("CPU average = %d, want 250", got)
	}
	if got := resourceKernelCPUAverageMilli(1, 0); got != 0 {
		t.Fatalf("zero-duration CPU average = %d", got)
	}
}

func TestResourceProcessCPUClockIsAvailable(t *testing.T) {
	usage, ok := resourceProcessCPUTime()
	if !ok || usage < 0 {
		t.Fatalf("process CPU clock = (%s, %t)", usage, ok)
	}
}
