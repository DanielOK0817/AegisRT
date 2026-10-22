package benchmark

import (
	"fmt"
	"math"
	"sort"
)

func calculateMetrics(workload schedulerWorkload, timings map[string]*taskTiming, metrics trialMetrics) (trialMetrics, error) {
	queue := make([]float64, 0, len(timings))
	firstDispatch := int64(math.MaxInt64)
	lastTerminal := int64(0)
	workflowService := make(map[string]float64)
	workflowTerminal := make(map[string]int64)
	for taskID, timing := range timings {
		if timing.dispatchMS < 0 || timing.terminalMS < timing.dispatchMS || timing.dispatchMS < timing.readyMS {
			return trialMetrics{}, fmt.Errorf("invalid timing for task %s", taskID)
		}
		wait := timing.dispatchMS - timing.readyMS
		queue = append(queue, float64(wait))
		if wait > workload.StarvationLimitMS {
			metrics.starvationCount++
		}
		if timing.dispatchMS < firstDispatch {
			firstDispatch = timing.dispatchMS
		}
		if timing.terminalMS > lastTerminal {
			lastTerminal = timing.terminalMS
		}
	}
	for _, task := range workload.Tasks {
		timing := timings[task.Spec.ID]
		workflowService[task.Spec.WorkflowID] += task.Spec.Resources.DominantShare(workload.Capacity) * float64(task.ServiceMS)
		if timing.terminalMS > workflowTerminal[task.Spec.WorkflowID] {
			workflowTerminal[task.Spec.WorkflowID] = timing.terminalMS
		}
	}
	if len(workload.CriticalSegment) == 0 {
		return trialMetrics{}, fmt.Errorf("critical path metric segment is empty")
	}
	criticalStart, exists := timings[workload.CriticalSegment[0]]
	if !exists {
		return trialMetrics{}, fmt.Errorf("critical path metric segment start is missing")
	}
	criticalEnd, exists := timings[workload.CriticalSegment[len(workload.CriticalSegment)-1]]
	if !exists {
		return trialMetrics{}, fmt.Errorf("critical path metric segment end is missing")
	}
	unlock, exists := timings[workload.UnlockTarget]
	if !exists {
		return trialMetrics{}, fmt.Errorf("unlock target is missing")
	}
	metrics.makespanMS = float64(lastTerminal - firstDispatch)
	metrics.queueP50MS = nearestRank(queue, 0.50)
	metrics.queueP95MS = nearestRank(queue, 0.95)
	metrics.criticalPathMS = float64(criticalEnd.terminalMS - criticalStart.readyMS)
	metrics.dependencyUnlockMS = float64(unlock.dispatchMS - unlock.readyMS)
	if metrics.makespanMS <= 0 {
		return trialMetrics{}, fmt.Errorf("non-positive makespan")
	}
	metrics.throughputTasksS = float64(len(timings)) * 1000 / metrics.makespanMS
	serviceRates := make(map[string]float64, len(workflowService))
	for workflowID, service := range workflowService {
		completionMS := workflowTerminal[workflowID] - firstDispatch
		if completionMS <= 0 {
			return trialMetrics{}, fmt.Errorf("non-positive workflow completion for %s", workflowID)
		}
		serviceRates[workflowID] = service / float64(completionMS)
	}
	metrics.jainFairness = jainFairness(serviceRates)
	return metrics, nil
}

func nearestRank(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	index := int(math.Ceil(percentile*float64(len(ordered)))) - 1
	if index < 0 {
		index = 0
	}
	return ordered[index]
}

func makeSchedulerRow(workload schedulerWorkload, metrics trialMetrics, workloadHash, pairID string, repetition, order int, treatment, policy, role string) SchedulerRow {
	return SchedulerRow{
		SchemaVersion: schedulerSchemaV1, Status: "SUCCEEDED", EvidenceScope: "windows-functional",
		CaseID: workload.CaseID, PairID: pairID, Seed: workload.Seed, Repetition: repetition, TrialOrder: order,
		Treatment: treatment, Role: role, Policy: policy, WorkflowShape: workload.WorkflowShape, WorkloadSHA256: workloadHash,
		WorkflowCount: workflowCount(workload), TaskCount: len(workload.Tasks), CapacityCPUMillis: workload.Capacity.CPUMilli,
		CapacityMemoryBytes: workload.Capacity.MemoryMiB * 1024 * 1024, CapacityTokens: workload.Capacity.TokenBudget,
		CapacityToolSlots: sumToolSlots(workload.Capacity.ToolSlots), MakespanMS: metrics.makespanMS,
		QueueP50MS: metrics.queueP50MS, QueueP95MS: metrics.queueP95MS, CriticalPathMS: metrics.criticalPathMS,
		DependencyUnlockMS: metrics.dependencyUnlockMS, ThroughputTasksS: metrics.throughputTasksS,
		JainFairness: metrics.jainFairness, AdmissionRejections: metrics.admissionRejections, StarvationCount: metrics.starvationCount,
		DecisionCount: metrics.decisionCount, TimeoutMS: trialTimeoutMS, ReasonCode: "NONE",
	}
}

func workflowCount(workload schedulerWorkload) int {
	workflows := make(map[string]struct{})
	for _, task := range workload.Tasks {
		workflows[task.Spec.WorkflowID] = struct{}{}
	}
	return len(workflows)
}

func sumToolSlots(slots map[string]int64) int64 {
	var total int64
	for _, value := range slots {
		total += value
	}
	return total
}
