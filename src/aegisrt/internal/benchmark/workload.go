package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"aegisrt/internal/domain"
)

const (
	s1SmokeShape          = "diamond-single-slot-sim-v2"
	schedulerPlanID       = schedulerSmokePlanID
	workflowShape         = s1SmokeShape
	formalS1Shape         = "diamond-single-slot-sim-v3"
	formalS2Shape         = "multi-workflow-multi-resource-sim-v1"
	formalS3Shape         = "dynamic-critical-unlock-sim-v1"
	starvationLimitMS     = int64(50)
	trialTimeoutMS        = 60000
	policyAgingMS         = int64(1000)
	maxSchedulerPlanCells = 128
	schedulerSchemaV1     = "aegisrt.experiment.scheduler/v1"
	manifestSchemaV1      = "aegisrt.experiment.manifest/v1"
	eventSchemaV1         = "aegisrt.experiment.event/v1"
)

type workloadBundle struct {
	Version   string              `json:"version"`
	Workloads []schedulerWorkload `json:"workloads"`
}

type schedulerWorkload struct {
	PlanID            string                `json:"plan_id"`
	CaseID            string                `json:"case_id"`
	Phase             string                `json:"phase"`
	WorkflowShape     string                `json:"workflow_shape"`
	Seed              uint32                `json:"seed"`
	Capacity          domain.ResourceVector `json:"capacity"`
	CriticalPath      []string              `json:"critical_path"`
	CriticalSegment   []string              `json:"critical_path_metric_segment"`
	CriticalMetric    string                `json:"critical_path_metric_semantic"`
	UnlockTarget      string                `json:"unlock_target"`
	StarvationLimitMS int64                 `json:"starvation_limit_ms"`
	Tasks             []workloadTask        `json:"tasks"`
}

type workloadTask struct {
	Spec              domain.TaskSpec `json:"spec"`
	ServiceMS         int64           `json:"service_ms"`
	InheritedPriority int             `json:"inherited_priority"`
	CriticalPath      int             `json:"critical_path"`
	UnlockCount       int             `json:"unlock_count"`
}

func buildS1SmokeWorkload(seed uint32) schedulerWorkload {
	return buildS1Workload(seed, schedulerSmokePlanID, "S1-smoke", s1SmokeShape)
}

func buildS1FormalWorkload(seed uint32) schedulerWorkload {
	return buildS1Workload(seed, schedulerFormalPlanID, "S1", formalS1Shape)
}

func buildS1Workload(seed uint32, planID, caseID, shape string) schedulerWorkload {
	capacity := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 512, TokenBudget: 1000, ToolSlots: map[string]int64{"worker": 1}}
	resource := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 128, TokenBudget: 100, ToolSlots: map[string]int64{"worker": 1}}
	makeTask := taskFactory(resource)
	rootMS := int64(5 + seed%3)
	auxiliaryMS := int64(8 + seed%5)
	criticalMS := int64(30 + seed%8)
	joinMS := int64(5 + (seed/3)%3)
	return schedulerWorkload{
		PlanID: planID, CaseID: caseID, WorkflowShape: shape, Seed: seed, Capacity: capacity,
		CriticalPath: []string{"root", "beta-critical", "join"}, CriticalSegment: []string{"beta-critical"},
		CriticalMetric: "predeclared-segment-response", UnlockTarget: "beta-critical", StarvationLimitMS: starvationLimitMS,
		Tasks: []workloadTask{
			makeTask("root", "workflow-s1", nil, 1, rootMS, 8, 3, 2),
			makeTask("alpha-aux", "workflow-s1", []string{"root"}, 0, auxiliaryMS, 0, 1, 1),
			makeTask("beta-critical", "workflow-s1", []string{"root"}, 8, criticalMS, 8, 2, 1),
			makeTask("join", "workflow-s1", []string{"alpha-aux", "beta-critical"}, 8, joinMS, 8, 1, 0),
		},
	}
}

func buildS2FormalWorkload(seed uint32) schedulerWorkload {
	capacity := domain.ResourceVector{CPUMilli: 2000, MemoryMiB: 2048, TokenBudget: 2400, ToolSlots: map[string]int64{"worker": 2}}
	resources := []domain.ResourceVector{
		{CPUMilli: 1000, MemoryMiB: 256, TokenBudget: 300, ToolSlots: map[string]int64{"worker": 1}},
		{CPUMilli: 300, MemoryMiB: 1024, TokenBudget: 300, ToolSlots: map[string]int64{"worker": 1}},
		{CPUMilli: 400, MemoryMiB: 256, TokenBudget: 1200, ToolSlots: map[string]int64{"worker": 1}},
		{CPUMilli: 750, MemoryMiB: 512, TokenBudget: 600, ToolSlots: map[string]int64{"worker": 1}},
	}
	durations := []int64{28 + int64(seed%3), 24 + int64(seed%5), 20 + int64(seed%7), 26 + int64(seed%4), 29 + int64(seed%3), 23 + int64(seed%5)}
	tasks := make([]workloadTask, 0, 18)
	for workflowIndex, suffix := range []string{"a", "b", "c"} {
		workflowID := "workflow-s2-" + suffix
		for taskIndex := 0; taskIndex < 6; taskIndex++ {
			id := fmt.Sprintf("%s-%02d", suffix, taskIndex)
			makeTask := taskFactory(resources[(taskIndex+workflowIndex)%len(resources)])
			tasks = append(tasks, makeTask(id, workflowID, nil, 1, durations[taskIndex], 1, 1, 0))
		}
	}
	return schedulerWorkload{
		PlanID: schedulerFormalPlanID, CaseID: "S2", WorkflowShape: formalS2Shape, Seed: seed, Capacity: capacity,
		CriticalPath: []string{"c-05"}, CriticalSegment: []string{"c-05"}, CriticalMetric: "diagnostic-task-response",
		UnlockTarget: "c-05", StarvationLimitMS: 75, Tasks: tasks,
	}
}

func buildS3FormalWorkload(seed uint32) schedulerWorkload {
	capacity := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 512, TokenBudget: 1000, ToolSlots: map[string]int64{"worker": 1}}
	resource := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 128, TokenBudget: 100, ToolSlots: map[string]int64{"worker": 1}}
	makeTask := taskFactory(resource)
	loadMS := int64(28 + seed%5)
	tasks := []workloadTask{
		makeTask("critical-root", "workflow-s3-critical", nil, 10, 5+int64(seed%3), 0, 1, 1),
		makeTask("critical-target", "workflow-s3-critical", []string{"critical-root"}, 3, 20+int64(seed%4), 1, 2, 0),
	}
	dependency := []string(nil)
	for index := 0; index < 5; index++ {
		id := fmt.Sprintf("load-%02d", index)
		tasks = append(tasks, makeTask(id, "workflow-s3-load", dependency, 9, loadMS+int64(index%2), 0, 1, 1))
		dependency = []string{id}
	}
	return schedulerWorkload{
		PlanID: schedulerFormalPlanID, CaseID: "S3", WorkflowShape: formalS3Shape, Seed: seed, Capacity: capacity,
		CriticalPath: []string{"critical-root", "critical-target"}, CriticalSegment: []string{"critical-root", "critical-target"},
		CriticalMetric: "critical-workflow-response", UnlockTarget: "critical-target", StarvationLimitMS: 50, Tasks: tasks,
	}
}

func taskFactory(resource domain.ResourceVector) func(string, string, []string, int, int64, int, int, int) workloadTask {
	return func(id, workflowID string, dependencies []string, priority int, duration int64, inherited, critical, unlock int) workloadTask {
		return workloadTask{Spec: domain.TaskSpec{
			ID: id, WorkflowID: workflowID, AgentID: "benchmark-agent", Goal: "execute deterministic benchmark step",
			Dependencies: dependencies, Priority: priority, Resources: resource.Clone(), MaxAttempts: 1,
			Timeout: time.Duration(trialTimeoutMS) * time.Millisecond, IdempotencyKey: "idem-" + id,
		}, ServiceMS: duration, InheritedPriority: inherited, CriticalPath: critical, UnlockCount: unlock}
	}
}

type schedulerPolicyInput struct {
	Name            string `json:"name"`
	AgingIntervalMS int64  `json:"aging_interval_ms"`
}

type schedulerInputContract struct {
	Kind           string                 `json:"kind"`
	PlanID         string                 `json:"plan_id"`
	Profile        string                 `json:"profile"`
	BundleVersion  string                 `json:"bundle_version"`
	TrialTimeoutMS int                    `json:"trial_timeout_ms"`
	Plan           SchedulerPlan          `json:"plan"`
	WarmupSeeds    []uint32               `json:"warmup_seeds"`
	Policies       []schedulerPolicyInput `json:"policies"`
	WorkloadSHA256 string                 `json:"workload_sha256"`
}

func buildSchedulerInput(plan SchedulerPlan, profile schedulerProfile, workloadHash string) schedulerInputContract {
	return schedulerInputContract{
		Kind: "scheduler", PlanID: profile.PlanID, Profile: profile.Name, BundleVersion: profile.BundleVersion,
		TrialTimeoutMS: trialTimeoutMS, Plan: plan, WarmupSeeds: append([]uint32(nil), profile.WarmupSeeds...),
		WorkloadSHA256: workloadHash,
		Policies: []schedulerPolicyInput{
			{Name: "fifo", AgingIntervalMS: 0},
			{Name: "priority", AgingIntervalMS: policyAgingMS},
			{Name: "dag-drcfs", AgingIntervalMS: policyAgingMS},
		},
	}
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode canonical input: %w", err)
	}
	return data, nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalHash(value any) (string, error) {
	data, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}
