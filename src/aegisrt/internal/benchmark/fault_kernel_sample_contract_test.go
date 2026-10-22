package benchmark

import "testing"

func TestFaultKernelSuccessfulSamplesRejectFalseCgroupProof(t *testing.T) {
	tests := []struct {
		name   string
		build  func() (FaultKernelTrialTrace, []FaultKernelCgroupSample)
		mutate func(FaultKernelTrialTrace, []FaultKernelCgroupSample)
	}{
		{
			name: "baseline terminal wrong attempt", build: validFaultKernelBaselineEvidence,
			mutate: func(_ FaultKernelTrialTrace, samples []FaultKernelCgroupSample) { samples[1].AttemptNumber = 2 },
		},
		{
			name: "baseline terminal still exists", build: validFaultKernelBaselineEvidence,
			mutate: func(trace FaultKernelTrialTrace, samples []FaultKernelCgroupSample) {
				samples[1] = cgroupSample(trace, 2, "trial_terminal", 40_000_000, 1, true, true, 2)
			},
		},
		{
			name: "restart cleanup still exists", build: validFaultKernelTreatmentEvidence,
			mutate: func(trace FaultKernelTrialTrace, samples []FaultKernelCgroupSample) {
				samples[2] = cgroupSample(trace, 3, "post_runtime_restart", 50_000_000, 1, true, true, 2)
			},
		},
		{
			name: "treatment terminal still exists", build: validFaultKernelTreatmentEvidence,
			mutate: func(trace FaultKernelTrialTrace, samples []FaultKernelCgroupSample) {
				samples[4] = cgroupSample(trace, 5, "trial_terminal", 80_000_000, 2, true, true, 2)
			},
		},
		{
			name: "wrong member count", build: validFaultKernelTreatmentEvidence,
			mutate: func(_ FaultKernelTrialTrace, samples []FaultKernelCgroupSample) { *samples[3].MemberCount = 1 },
		},
		{
			name: "wrong controller readback", build: validFaultKernelTreatmentEvidence,
			mutate: func(_ FaultKernelTrialTrace, samples []FaultKernelCgroupSample) {
				value := "max"
				samples[1].ControllerReadback.MemoryMax = &value
			},
		},
		{
			name: "wrong ready attempt", build: validFaultKernelTreatmentEvidence,
			mutate: func(_ FaultKernelTrialTrace, samples []FaultKernelCgroupSample) { samples[3].AttemptNumber = 1 },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trace, samples := test.build()
			test.mutate(trace, samples)
			if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
				t.Fatal("false successful cgroup proof was accepted")
			}
		})
	}
}

func TestFaultKernelSuccessfulSamplesBindMilestones(t *testing.T) {
	trace, samples := validFaultKernelBaselineEvidence()
	samples[1].MonotonicNS--
	if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
		t.Fatal("baseline terminal sample outside cleanup milestone was accepted")
	}

	trace, samples = validFaultKernelTreatmentEvidence()
	samples[2].MonotonicNS++
	if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
		t.Fatal("restart cleanup sample outside cleanup milestone was accepted")
	}
}

func TestFaultKernelFailedSamplesRequireReadyAndTerminalProof(t *testing.T) {
	trace, samples := validFaultKernelBaselineEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "HARNESS_FAILURE"
	*samples[0].MemberCount = 1
	if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
		t.Fatal("failed trial without a real readiness proof was accepted")
	}

	trace, samples = validFaultKernelBaselineEvidence()
	trace.Status, trace.ReasonCode = "TIMED_OUT", "TRIAL_TIMEOUT"
	samples[1] = cgroupSample(trace, 2, "trial_terminal", 40_000_000, 1, true, true, 2)
	if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
		t.Fatal("failed trial without terminal cgroup absence was accepted")
	}

	trace, samples = validFaultKernelTreatmentEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "RETRY_NOT_READY"
	samples[1].Stage = "attempt2_ready"
	if err := ValidateFaultKernelEvidence(trace, samples); err == nil {
		t.Fatal("failed treatment with a non-prefix stage order was accepted")
	}
}
