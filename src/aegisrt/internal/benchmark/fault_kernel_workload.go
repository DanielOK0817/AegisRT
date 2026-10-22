package benchmark

import (
	"encoding/json"
	"fmt"
	"time"

	"aegisrt/internal/domain"
)

const (
	faultKernelWorkloadSchema  = "aegisrt.experiment.fault-kernel-workload/v1"
	faultKernelTargetAgentID   = "f4-target-agent"
	faultKernelSurvivorAgentID = "f4-survivor-agent"
	faultKernelFixtureCommand  = "fault-agent-fixture"
	faultKernelTargetHold      = 5 * time.Second
	faultKernelAgentTimeout    = 15 * time.Second
	faultKernelTaskTimeout     = 20 * time.Second
	faultKernelPollInterval    = 10 * time.Millisecond
	faultKernelShutdownTimeout = 5 * time.Second
	faultKernelEventCapacity   = 128
)

var faultKernelTaskResources = domain.ResourceVector{
	CPUMilli: 100, MemoryMiB: 64, TokenBudget: 256,
}

type faultKernelWorkload struct {
	SchemaVersion   string                     `json:"schema_version"`
	WorkloadVersion string                     `json:"workload_version"`
	FixtureProtocol string                     `json:"fixture_protocol"`
	Profile         string                     `json:"profile"`
	PlanID          string                     `json:"plan_id"`
	CaseID          string                     `json:"case_id"`
	RuntimeConfig   faultKernelRuntimeConfig   `json:"runtime_config"`
	Agents          []faultKernelWorkloadAgent `json:"agents"`
	Cells           []faultKernelWorkloadCell  `json:"cells"`
}

type faultKernelRuntimeConfig struct {
	SchedulerPolicy string                `json:"scheduler_policy"`
	Capacity        domain.ResourceVector `json:"capacity"`
	MaxWorkers      int                   `json:"max_workers"`
	PollIntervalMS  int                   `json:"poll_interval_ms"`
	EventCapacity   int                   `json:"event_capacity"`
	PIDsMax         int64                 `json:"pids_max"`
	CPUPeriodMicros int64                 `json:"cpu_period_micros"`
	ShutdownMS      int64                 `json:"shutdown_timeout_ms"`
	AgentTimeoutMS  int64                 `json:"agent_timeout_ms"`
	TaskTimeoutMS   int64                 `json:"task_timeout_ms"`
	TargetHoldMS    int64                 `json:"target_hold_ms"`
	SpawnChild      bool                  `json:"spawn_child"`
}

type faultKernelWorkloadAgent struct {
	ID                 string                `json:"id"`
	FixtureRole        string                `json:"fixture_role"`
	Resources          domain.ResourceVector `json:"resources"`
	MaxConcurrency     int                   `json:"max_concurrency"`
	ExecutionTimeoutMS int64                 `json:"execution_timeout_ms"`
}

type faultKernelWorkloadCell struct {
	Phase      string                  `json:"phase"`
	Seed       uint32                  `json:"seed"`
	Repetition int                     `json:"repetition"`
	PairID     string                  `json:"pair_id"`
	Target     faultKernelWorkloadTask `json:"target"`
	Survivor   faultKernelWorkloadTask `json:"survivor"`
}

type faultKernelWorkloadTask struct {
	ID             string                `json:"id"`
	WorkflowID     string                `json:"workflow_id"`
	AgentID        string                `json:"agent_id"`
	Goal           string                `json:"goal"`
	Dependencies   []string              `json:"dependencies"`
	Priority       int                   `json:"priority"`
	Resources      domain.ResourceVector `json:"resources"`
	MaxAttempts    int                   `json:"max_attempts"`
	TimeoutMS      int64                 `json:"timeout_ms"`
	IdempotencyKey string                `json:"idempotency_key"`
}

type faultKernelCell struct {
	Phase      string
	Seed       uint32
	Repetition int
	PairID     string
	WorkflowID string
	Target     domain.TaskSpec
	Survivor   domain.TaskSpec
}

func newFaultKernelWorkload(plan FaultKernelPlan) (faultKernelWorkload, []faultKernelCell, error) {
	if err := ValidateFaultKernelPlan(plan); err != nil {
		return faultKernelWorkload{}, nil, err
	}
	config := fixedFaultKernelRuntimeConfig()
	workload := faultKernelWorkload{
		SchemaVersion: faultKernelWorkloadSchema, WorkloadVersion: FaultKernelWorkloadVersion,
		FixtureProtocol: FaultKernelFixtureProtocol, Profile: plan.Profile,
		PlanID: plan.PlanID, CaseID: plan.CaseID, RuntimeConfig: config,
		Agents: fixedFaultKernelWorkloadAgents(),
	}
	cells := make([]faultKernelCell, 0, len(plan.Protocol.WarmupSeeds)+len(plan.Protocol.MeasuredSeeds)*plan.Protocol.Repetitions)
	appendPhase := func(phase string, seeds []uint32) error {
		for _, seed := range seeds {
			for repetition := 0; repetition < plan.Protocol.Repetitions; repetition++ {
				cell, err := newFaultKernelCell(phase, seed, repetition)
				if err != nil {
					return err
				}
				cells = append(cells, cell)
				workload.Cells = append(workload.Cells, faultKernelWorkloadCell{
					Phase: phase, Seed: seed, Repetition: repetition, PairID: cell.PairID,
					Target: faultKernelPublishedTask(cell.Target), Survivor: faultKernelPublishedTask(cell.Survivor),
				})
			}
		}
		return nil
	}
	if err := appendPhase("warmup", plan.Protocol.WarmupSeeds); err != nil {
		return faultKernelWorkload{}, nil, err
	}
	if err := appendPhase("measurement", plan.Protocol.MeasuredSeeds); err != nil {
		return faultKernelWorkload{}, nil, err
	}
	return workload, cells, nil
}

func fixedFaultKernelWorkloadAgents() []faultKernelWorkloadAgent {
	return []faultKernelWorkloadAgent{
		{ID: faultKernelTargetAgentID, FixtureRole: "target-agent-tree", Resources: faultKernelTaskResources.Clone(),
			MaxConcurrency: 1, ExecutionTimeoutMS: faultKernelAgentTimeout.Milliseconds()},
		{ID: faultKernelSurvivorAgentID, FixtureRole: "survivor", Resources: faultKernelTaskResources.Clone(),
			MaxConcurrency: 1, ExecutionTimeoutMS: faultKernelAgentTimeout.Milliseconds()},
	}
}

func faultKernelPublishedTask(task domain.TaskSpec) faultKernelWorkloadTask {
	return faultKernelWorkloadTask{
		ID: task.ID, WorkflowID: task.WorkflowID, AgentID: task.AgentID, Goal: task.Goal,
		Dependencies: append([]string{}, task.Dependencies...), Priority: task.Priority,
		Resources: task.Resources.Clone(), MaxAttempts: task.MaxAttempts,
		TimeoutMS: task.Timeout.Milliseconds(), IdempotencyKey: task.IdempotencyKey,
	}
}

func newFaultKernelCell(phase string, seed uint32, repetition int) (faultKernelCell, error) {
	phaseCode := "m"
	if phase == "warmup" {
		phaseCode = "w"
	} else if phase != "measurement" {
		return faultKernelCell{}, fmt.Errorf("invalid fault-kernel phase")
	}
	base := fmt.Sprintf("f4-%s-%d-%d", phaseCode, seed, repetition)
	workflowID, targetID, survivorID := base+"-workflow", base+"-target", base+"-survivor"
	target := faultKernelTask(targetID, workflowID, faultKernelTargetAgentID, 2, 1)
	survivor := faultKernelTask(survivorID, workflowID, faultKernelSurvivorAgentID, 1, 10)
	if err := target.Validate(); err != nil {
		return faultKernelCell{}, err
	}
	if err := survivor.Validate(); err != nil {
		return faultKernelCell{}, err
	}
	return faultKernelCell{
		Phase: phase, Seed: seed, Repetition: repetition, PairID: base,
		WorkflowID: workflowID, Target: target, Survivor: survivor,
	}, nil
}

func faultKernelTask(id, workflowID, agentID string, attempts, priority int) domain.TaskSpec {
	return domain.TaskSpec{
		ID: id, WorkflowID: workflowID, AgentID: agentID,
		Goal: "execute deterministic F4 fixture", Priority: priority,
		Resources: faultKernelTaskResources.Clone(), MaxAttempts: attempts,
		Timeout: faultKernelTaskTimeout, IdempotencyKey: id + "-key",
	}
}

func fixedFaultKernelRuntimeConfig() faultKernelRuntimeConfig {
	return faultKernelRuntimeConfig{
		SchedulerPolicy: "dag-drcfs",
		Capacity:        domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 512, TokenBudget: 4096},
		MaxWorkers:      2, PollIntervalMS: int(faultKernelPollInterval.Milliseconds()),
		EventCapacity: faultKernelEventCapacity, PIDsMax: 64, CPUPeriodMicros: 100000,
		ShutdownMS: faultKernelShutdownTimeout.Milliseconds(), AgentTimeoutMS: faultKernelAgentTimeout.Milliseconds(),
		TaskTimeoutMS: faultKernelTaskTimeout.Milliseconds(), TargetHoldMS: faultKernelTargetHold.Milliseconds(),
		SpawnChild: true,
	}
}

func marshalFaultKernelWorkload(value faultKernelWorkload) ([]byte, error) {
	return canonicalJSON(value)
}

func faultKernelRuntimeConfigHash(value faultKernelRuntimeConfig) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hashBytes(payload), nil
}
