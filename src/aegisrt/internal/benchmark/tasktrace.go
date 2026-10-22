package benchmark

import (
	"fmt"
	"math"
	"sort"
)

const schedulerTaskTraceSchemaV1 = "aegisrt.experiment.scheduler-task-trace/v1"

// SchedulerEvidence keeps the exact canonical workload and the allowlisted
// task-level records used to derive Result. Accessors return defensive copies.
type SchedulerEvidence struct {
	Result       SchedulerResult
	workloadJSON []byte
	inputJSON    []byte
	taskTraces   []SchedulerTaskTrace
	warmupTrials []trialObservation
}

func (e SchedulerEvidence) CanonicalWorkloadJSON() []byte {
	return append([]byte(nil), e.workloadJSON...)
}

func (e SchedulerEvidence) CanonicalInputJSON() []byte {
	return append([]byte(nil), e.inputJSON...)
}

func (e SchedulerEvidence) TaskTraceRecords() []SchedulerTaskTrace {
	return append([]SchedulerTaskTrace(nil), e.taskTraces...)
}

// SchedulerTaskTrace is a fixed allowlist. It deliberately excludes prompts,
// environment data, scheduler reason strings, and arbitrary payloads.
type SchedulerTaskTrace struct {
	SchemaVersion        string  `json:"schema_version"`
	RunID                string  `json:"run_id"`
	EvidenceScope        string  `json:"evidence_scope"`
	PlanID               string  `json:"plan_id"`
	CaseID               string  `json:"case_id"`
	PairID               string  `json:"pair_id"`
	Seed                 uint32  `json:"seed"`
	Repetition           int     `json:"repetition"`
	TrialOrder           int     `json:"trial_order"`
	Treatment            string  `json:"treatment"`
	Role                 string  `json:"role"`
	Policy               string  `json:"policy"`
	WorkflowShape        string  `json:"workflow_shape"`
	WorkloadSHA256       string  `json:"workload_sha256"`
	InputSHA256          string  `json:"input_sha256"`
	TrialTimeoutMS       int     `json:"trial_timeout_ms"`
	StarvationLimitMS    int64   `json:"starvation_limit_ms"`
	CriticalPathMetric   string  `json:"critical_path_metric_semantic"`
	TaskID               string  `json:"task_id"`
	WorkflowID           string  `json:"workflow_id"`
	Status               string  `json:"status"`
	ReadyMS              int64   `json:"ready_ms"`
	DispatchMS           int64   `json:"dispatch_ms"`
	TerminalMS           int64   `json:"terminal_ms"`
	ServiceMS            int64   `json:"service_ms"`
	NormalizedService    float64 `json:"normalized_service"`
	DispatchSequence     int     `json:"dispatch_sequence"`
	CriticalPathOrder    int     `json:"critical_path_order"`
	CriticalSegmentOrder int     `json:"critical_segment_order"`
	UnlockTarget         bool    `json:"unlock_target"`
}

type SchedulerTraceMetrics struct {
	MakespanMS          float64
	QueueP50MS          float64
	QueueP95MS          float64
	CriticalPathMS      float64
	DependencyUnlockMS  float64
	ThroughputTasksS    float64
	JainFairness        float64
	AdmissionRejections int
	StarvationCount     int
	DecisionCount       int
}

func makeSchedulerTaskTraces(workload schedulerWorkload, timings map[string]*taskTiming, row SchedulerRow, inputHash string) ([]SchedulerTaskTrace, error) {
	pathOrder := orderedTaskIndex(workload.CriticalPath)
	segmentOrder := orderedTaskIndex(workload.CriticalSegment)
	records := make([]SchedulerTaskTrace, 0, len(workload.Tasks))
	for _, item := range workload.Tasks {
		timing, exists := timings[item.Spec.ID]
		if !exists {
			return nil, fmt.Errorf("timing for task %q is missing", item.Spec.ID)
		}
		criticalOrder := -1
		if order, exists := pathOrder[item.Spec.ID]; exists {
			criticalOrder = order
		}
		criticalSegmentOrder := -1
		if order, exists := segmentOrder[item.Spec.ID]; exists {
			criticalSegmentOrder = order
		}
		records = append(records, SchedulerTaskTrace{
			SchemaVersion: schedulerTaskTraceSchemaV1, EvidenceScope: row.EvidenceScope,
			PlanID: workload.PlanID, CaseID: row.CaseID, PairID: row.PairID, Seed: row.Seed,
			Repetition: row.Repetition, TrialOrder: row.TrialOrder, Treatment: row.Treatment,
			Role: row.Role, Policy: row.Policy, WorkflowShape: row.WorkflowShape,
			WorkloadSHA256: row.WorkloadSHA256, InputSHA256: inputHash, TrialTimeoutMS: row.TimeoutMS,
			StarvationLimitMS: workload.StarvationLimitMS, CriticalPathMetric: workload.CriticalMetric,
			TaskID: item.Spec.ID, WorkflowID: item.Spec.WorkflowID, Status: "SUCCEEDED",
			ReadyMS: timing.readyMS, DispatchMS: timing.dispatchMS, TerminalMS: timing.terminalMS,
			ServiceMS:         item.ServiceMS,
			NormalizedService: item.Spec.Resources.DominantShare(workload.Capacity) * float64(item.ServiceMS),
			DispatchSequence:  timing.dispatchSequence, CriticalPathOrder: criticalOrder,
			CriticalSegmentOrder: criticalSegmentOrder, UnlockTarget: item.Spec.ID == workload.UnlockTarget,
		})
	}
	return sortedTaskTraces(records), nil
}

func orderedTaskIndex(taskIDs []string) map[string]int {
	result := make(map[string]int, len(taskIDs))
	for order, taskID := range taskIDs {
		result[taskID] = order
	}
	return result
}

func AggregateSchedulerTaskTraces(records []SchedulerTaskTrace) (SchedulerTraceMetrics, error) {
	if len(records) == 0 {
		return SchedulerTraceMetrics{}, fmt.Errorf("scheduler task traces are empty")
	}
	first := records[0]
	queue := make([]float64, 0, len(records))
	firstDispatch := int64(math.MaxInt64)
	lastTerminal := int64(-1)
	dispatches := make(map[int]struct{}, len(records))
	taskIDs := make(map[string]struct{}, len(records))
	path := make(map[int]SchedulerTaskTrace)
	segment := make(map[int]SchedulerTaskTrace)
	workflowService := make(map[string]float64)
	workflowTerminal := make(map[string]int64)
	var unlock *SchedulerTaskTrace
	result := SchedulerTraceMetrics{}
	for index := range records {
		record := records[index]
		if !sameTraceTrial(first, record) {
			return SchedulerTraceMetrics{}, fmt.Errorf("task trace %d belongs to a different trial", index)
		}
		if record.SchemaVersion != schedulerTaskTraceSchemaV1 || record.Status != "SUCCEEDED" ||
			record.EvidenceScope != "windows-functional" || !validSchedulerTraceIdentity(record) ||
			record.TrialTimeoutMS != trialTimeoutMS || record.StarvationLimitMS <= 0 ||
			!validTracePolicyRole(record.CaseID, record.Treatment, record.Policy, record.Role) ||
			len(record.WorkloadSHA256) != 64 || !isLowerHex(record.WorkloadSHA256) ||
			len(record.InputSHA256) != 64 || !isLowerHex(record.InputSHA256) {
			return SchedulerTraceMetrics{}, fmt.Errorf("task trace %d has unsupported schema or status", index)
		}
		if record.TaskID == "" || record.WorkflowID == "" || record.ServiceMS <= 0 ||
			record.ReadyMS < 0 || record.DispatchMS < record.ReadyMS ||
			record.TerminalMS-record.DispatchMS != record.ServiceMS ||
			math.IsNaN(record.NormalizedService) || math.IsInf(record.NormalizedService, 0) || record.NormalizedService <= 0 {
			return SchedulerTraceMetrics{}, fmt.Errorf("task trace %d has invalid timing or service", index)
		}
		if _, exists := taskIDs[record.TaskID]; exists {
			return SchedulerTraceMetrics{}, fmt.Errorf("duplicate task trace %q", record.TaskID)
		}
		taskIDs[record.TaskID] = struct{}{}
		if record.DispatchSequence < 0 {
			return SchedulerTraceMetrics{}, fmt.Errorf("task trace %q has negative dispatch sequence", record.TaskID)
		}
		if _, exists := dispatches[record.DispatchSequence]; exists {
			return SchedulerTraceMetrics{}, fmt.Errorf("duplicate dispatch sequence %d", record.DispatchSequence)
		}
		dispatches[record.DispatchSequence] = struct{}{}
		if err := addOrderedTrace(path, record.CriticalPathOrder, record); err != nil {
			return SchedulerTraceMetrics{}, fmt.Errorf("critical path: %w", err)
		}
		if err := addOrderedTrace(segment, record.CriticalSegmentOrder, record); err != nil {
			return SchedulerTraceMetrics{}, fmt.Errorf("critical segment: %w", err)
		}
		if record.UnlockTarget {
			if unlock != nil {
				return SchedulerTraceMetrics{}, fmt.Errorf("multiple dependency unlock targets")
			}
			copy := record
			unlock = &copy
		}
		wait := record.DispatchMS - record.ReadyMS
		queue = append(queue, float64(wait))
		if wait > record.StarvationLimitMS {
			result.StarvationCount++
		}
		firstDispatch = minInt64(firstDispatch, record.DispatchMS)
		lastTerminal = maxInt64(lastTerminal, record.TerminalMS)
		workflowService[record.WorkflowID] += record.NormalizedService
		workflowTerminal[record.WorkflowID] = maxInt64(workflowTerminal[record.WorkflowID], record.TerminalMS)
	}
	if err := contiguousOrders(dispatches, len(records), "dispatch sequence"); err != nil {
		return SchedulerTraceMetrics{}, err
	}
	if err := contiguousTraceOrders(path, "critical path"); err != nil {
		return SchedulerTraceMetrics{}, err
	}
	if err := contiguousTraceOrders(segment, "critical segment"); err != nil {
		return SchedulerTraceMetrics{}, err
	}
	if unlock == nil {
		return SchedulerTraceMetrics{}, fmt.Errorf("dependency unlock target is missing")
	}
	segmentStart := segment[0]
	segmentEnd := segment[len(segment)-1]
	result.MakespanMS = float64(lastTerminal - firstDispatch)
	result.QueueP50MS = nearestRank(queue, 0.50)
	result.QueueP95MS = nearestRank(queue, 0.95)
	result.CriticalPathMS = float64(segmentEnd.TerminalMS - segmentStart.ReadyMS)
	result.DependencyUnlockMS = float64(unlock.DispatchMS - unlock.ReadyMS)
	result.DecisionCount = len(records)
	if result.MakespanMS <= 0 || result.CriticalPathMS <= 0 {
		return SchedulerTraceMetrics{}, fmt.Errorf("non-positive derived duration")
	}
	result.ThroughputTasksS = float64(len(records)) * 1000 / result.MakespanMS
	serviceRates := make(map[string]float64, len(workflowService))
	for workflowID, service := range workflowService {
		completionMS := workflowTerminal[workflowID] - firstDispatch
		if completionMS <= 0 {
			return SchedulerTraceMetrics{}, fmt.Errorf("non-positive workflow completion for %s", workflowID)
		}
		serviceRates[workflowID] = service / float64(completionMS)
	}
	result.JainFairness = jainFairness(serviceRates)
	return result, nil
}

func validTracePolicyRole(caseID, treatment, policy, role string) bool {
	return role == "baseline" && policy == baselinePolicy(caseID) ||
		role == "treatment" && policy == treatment && (policy == "priority" || policy == "dag-drcfs")
}

func validSchedulerTraceIdentity(record SchedulerTaskTrace) bool {
	switch record.CaseID {
	case "S1-smoke":
		return record.PlanID == schedulerSmokePlanID && record.WorkflowShape == s1SmokeShape && record.CriticalPathMetric == "predeclared-segment-response"
	case "S1":
		return record.PlanID == schedulerFormalPlanID && record.WorkflowShape == formalS1Shape && record.CriticalPathMetric == "predeclared-segment-response"
	case "S2":
		return record.PlanID == schedulerFormalPlanID && record.WorkflowShape == formalS2Shape && record.CriticalPathMetric == "diagnostic-task-response"
	case "S3":
		return record.PlanID == schedulerFormalPlanID && record.WorkflowShape == formalS3Shape && record.CriticalPathMetric == "critical-workflow-response"
	default:
		return false
	}
}

func sameTraceTrial(left, right SchedulerTaskTrace) bool {
	return left.RunID == right.RunID && left.EvidenceScope == right.EvidenceScope && left.PlanID == right.PlanID &&
		left.CaseID == right.CaseID && left.PairID == right.PairID && left.Seed == right.Seed &&
		left.Repetition == right.Repetition && left.TrialOrder == right.TrialOrder &&
		left.Treatment == right.Treatment && left.Role == right.Role && left.Policy == right.Policy &&
		left.WorkflowShape == right.WorkflowShape && left.WorkloadSHA256 == right.WorkloadSHA256 && left.InputSHA256 == right.InputSHA256 &&
		left.TrialTimeoutMS == right.TrialTimeoutMS && left.StarvationLimitMS == right.StarvationLimitMS &&
		left.CriticalPathMetric == right.CriticalPathMetric
}

func addOrderedTrace(target map[int]SchedulerTaskTrace, order int, record SchedulerTaskTrace) error {
	if order < -1 {
		return fmt.Errorf("task %q has invalid order %d", record.TaskID, order)
	}
	if order == -1 {
		return nil
	}
	if _, exists := target[order]; exists {
		return fmt.Errorf("duplicate order %d", order)
	}
	target[order] = record
	return nil
}

func contiguousTraceOrders(records map[int]SchedulerTaskTrace, label string) error {
	if len(records) == 0 {
		return fmt.Errorf("%s is empty", label)
	}
	orders := make(map[int]struct{}, len(records))
	for order := range records {
		orders[order] = struct{}{}
	}
	return contiguousOrders(orders, len(records), label)
}

func contiguousOrders(orders map[int]struct{}, count int, label string) error {
	for order := 0; order < count; order++ {
		if _, exists := orders[order]; !exists {
			return fmt.Errorf("%s is not contiguous at %d", label, order)
		}
	}
	return nil
}

func jainFairness(service map[string]float64) float64 {
	if len(service) == 0 {
		return 0
	}
	var sum, squares float64
	for _, value := range service {
		sum += value
		squares += value * value
	}
	if squares == 0 {
		return 0
	}
	return sum * sum / (float64(len(service)) * squares)
}

func sortedTaskTraces(records []SchedulerTaskTrace) []SchedulerTaskTrace {
	result := append([]SchedulerTaskTrace(nil), records...)
	sort.Slice(result, func(i, j int) bool { return result[i].DispatchSequence < result[j].DispatchSequence })
	return result
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
