package benchmark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

type faultKernelExecutedTrial struct {
	Trace      FaultKernelTrialTrace
	Samples    []FaultKernelCgroupSample
	StartedAt  time.Time
	FinishedAt time.Time
}

type faultKernelEvidenceBundle struct {
	RunID        string
	Plan         FaultKernelPlan
	Input        FaultKernelInput
	WorkloadJSON []byte
	Components   FaultKernelComponentManifest
	Trials       []faultKernelExecutedTrial
}

func prepareFaultKernelTrials(runID string, trials []faultKernelExecutedTrial) ([]faultKernelExecutedTrial, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	prepared := make([]faultKernelExecutedTrial, len(trials))
	for index, executed := range trials {
		if executed.StartedAt.IsZero() || executed.FinishedAt.IsZero() || executed.FinishedAt.Before(executed.StartedAt) {
			return nil, fmt.Errorf("fault-kernel trial %d has invalid observation time", index)
		}
		trace := cloneFaultKernelOutputTrace(executed.Trace)
		if trace.RunID != "" && trace.RunID != runID {
			return nil, fmt.Errorf("fault-kernel trial %d has conflicting run id", index)
		}
		trace.RunID = runID
		samples := make([]FaultKernelCgroupSample, len(executed.Samples))
		for sampleIndex, sample := range executed.Samples {
			samples[sampleIndex] = cloneFaultKernelOutputSample(sample)
			if samples[sampleIndex].RunID != "" && samples[sampleIndex].RunID != runID {
				return nil, fmt.Errorf("fault-kernel trial %d sample %d has conflicting run id", index, sampleIndex)
			}
			samples[sampleIndex].RunID = runID
		}
		if err := ValidateFaultKernelEvidence(trace, samples); err != nil {
			return nil, fmt.Errorf("fault-kernel trial %d: %w", index, err)
		}
		prepared[index] = faultKernelExecutedTrial{Trace: trace, Samples: samples,
			StartedAt: executed.StartedAt, FinishedAt: executed.FinishedAt}
	}
	return prepared, nil
}

func marshalFaultKernelTraces(records []FaultKernelTrialTrace) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for index, record := range records {
		if err := ValidateFaultKernelTrace(record); err != nil {
			return nil, fmt.Errorf("fault-kernel trace %d: %w", index, err)
		}
		if err := encoder.Encode(record); err != nil {
			return nil, fmt.Errorf("encode fault-kernel trace %d: %w", index, err)
		}
	}
	return output.Bytes(), nil
}

func marshalFaultKernelSamples(records []FaultKernelCgroupSample) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for index, record := range records {
		if err := validateFaultKernelSample(record); err != nil {
			return nil, fmt.Errorf("fault-kernel cgroup sample %d: %w", index, err)
		}
		if err := encoder.Encode(record); err != nil {
			return nil, fmt.Errorf("encode fault-kernel cgroup sample %d: %w", index, err)
		}
	}
	return output.Bytes(), nil
}

func cloneFaultKernelOutputTrace(trace FaultKernelTrialTrace) FaultKernelTrialTrace {
	trace.CgroupSampleSequences = append([]int(nil), trace.CgroupSampleSequences...)
	trace.Target.Attempts = append([]FaultKernelAttemptTrace(nil), trace.Target.Attempts...)
	if trace.StartupReceipt != nil {
		value := *trace.StartupReceipt
		trace.StartupReceipt = &value
	}
	trace.Milestones.InjectedNS = cloneFaultKernelInt64(trace.Milestones.InjectedNS)
	trace.Milestones.DaemonExitedNS = cloneFaultKernelInt64(trace.Milestones.DaemonExitedNS)
	trace.Milestones.CleanupCompleteNS = cloneFaultKernelInt64(trace.Milestones.CleanupCompleteNS)
	trace.Milestones.Attempt2ReadyNS = cloneFaultKernelInt64(trace.Milestones.Attempt2ReadyNS)
	trace.Milestones.RecoveredNS = cloneFaultKernelInt64(trace.Milestones.RecoveredNS)
	return trace
}

func cloneFaultKernelOutputSample(sample FaultKernelCgroupSample) FaultKernelCgroupSample {
	sample.Populated = cloneFaultKernelBool(sample.Populated)
	sample.MemberCount = cloneFaultKernelInt(sample.MemberCount)
	sample.ControllerReadback.CPUMax = cloneFaultKernelString(sample.ControllerReadback.CPUMax)
	sample.ControllerReadback.MemoryMax = cloneFaultKernelString(sample.ControllerReadback.MemoryMax)
	sample.ControllerReadback.PIDsMax = cloneFaultKernelString(sample.ControllerReadback.PIDsMax)
	return sample
}

func cloneFaultKernelInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFaultKernelInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFaultKernelBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFaultKernelString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
