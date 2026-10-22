package benchmark

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/taskgraph"
)

type taskTiming struct {
	readyMS          int64
	dispatchMS       int64
	terminalMS       int64
	dispatchSequence int
}

type completion struct {
	taskID   string
	finishMS int64
	resource domain.ResourceVector
}

type trialMetrics struct {
	makespanMS          float64
	queueP50MS          float64
	queueP95MS          float64
	criticalPathMS      float64
	dependencyUnlockMS  float64
	throughputTasksS    float64
	jainFairness        float64
	admissionRejections int
	starvationCount     int
	decisionCount       int
}

func SimulateScheduler(plan SchedulerPlan) (SchedulerResult, error) {
	evidence, err := SimulateSchedulerEvidence(plan)
	return evidence.Result, err
}

func SimulateSchedulerEvidence(plan SchedulerPlan) (SchedulerEvidence, error) {
	return simulateSchedulerEvidenceContext(context.Background(), plan, nil)
}

func simulateSchedulerEvidenceContext(ctx context.Context, plan SchedulerPlan, clock func() time.Time) (SchedulerEvidence, error) {
	if ctx == nil {
		return SchedulerEvidence{}, fmt.Errorf("context is required")
	}
	if err := validatePlan(plan); err != nil {
		return SchedulerEvidence{}, err
	}
	profile, err := resolveSchedulerProfile(plan)
	if err != nil {
		return SchedulerEvidence{}, err
	}
	bundle := workloadBundle{Version: profile.BundleVersion}
	for _, phase := range []struct {
		name  string
		seeds []uint32
	}{{"warmup", profile.WarmupSeeds}, {"measurement", plan.Seeds}} {
		for _, definition := range profile.Cases {
			for _, seed := range phase.seeds {
				workload := definition.Build(seed)
				workload.Phase = phase.name
				bundle.Workloads = append(bundle.Workloads, workload)
			}
		}
	}
	workloadJSON, err := canonicalJSON(bundle)
	if err != nil {
		return SchedulerEvidence{}, err
	}
	workloadHash := hashBytes(workloadJSON)
	inputJSON, err := canonicalJSON(buildSchedulerInput(plan, profile, workloadHash))
	if err != nil {
		return SchedulerEvidence{}, err
	}
	inputHash := hashBytes(inputJSON)
	result := SchedulerResult{WorkloadSHA256: workloadHash, InputSHA256: inputHash}
	evidence := SchedulerEvidence{
		workloadJSON: append([]byte(nil), workloadJSON...),
		inputJSON:    append([]byte(nil), inputJSON...),
	}
	phaseIndexes := map[string]int{}
	for _, workload := range bundle.Workloads {
		phaseKey := workload.Phase + "/" + workload.CaseID
		seedIndex := phaseIndexes[phaseKey]
		phaseIndexes[phaseKey]++
		for repetition := 0; repetition < plan.Repetitions; repetition++ {
			definition, findErr := schedulerCaseByID(profile, workload.CaseID)
			if findErr != nil {
				return SchedulerEvidence{}, findErr
			}
			for _, treatment := range definition.Treatments {
				pairID := schedulerPairID(workload, repetition, treatment)
				policies := []struct{ name, role string }{{baselinePolicy(workload.CaseID), "baseline"}, {treatment, "treatment"}}
				if seedIndex%2 == 1 {
					policies[0], policies[1] = policies[1], policies[0]
				}
				for order, item := range policies {
					if err := ctx.Err(); err != nil {
						return SchedulerEvidence{}, err
					}
					var started time.Time
					if clock != nil {
						started = clock()
					}
					metrics, timings, runErr := runSchedulerWorkload(workload, item.name)
					if runErr != nil {
						return SchedulerEvidence{}, fmt.Errorf("run %s seed %d: %w", item.name, workload.Seed, runErr)
					}
					if err := ctx.Err(); err != nil {
						return SchedulerEvidence{}, err
					}
					finished := time.Time{}
					if clock != nil {
						finished = clock()
					}
					if workload.Phase == "warmup" {
						evidence.warmupTrials = append(evidence.warmupTrials, trialObservation{
							Phase: "warmup", CaseID: workload.CaseID, PairID: pairID, Seed: workload.Seed,
							Repetition: repetition, TrialOrder: order, Treatment: treatment, StartedAt: started, FinishedAt: finished,
						})
						continue
					}
					row := makeSchedulerRow(workload, metrics, workloadHash, pairID, repetition, order, treatment, item.name, item.role)
					result.Rows = append(result.Rows, row)
					taskTraces, traceErr := makeSchedulerTaskTraces(workload, timings, row, inputHash)
					if traceErr != nil {
						return SchedulerEvidence{}, fmt.Errorf("trace %s seed %d: %w", item.name, workload.Seed, traceErr)
					}
					evidence.taskTraces = append(evidence.taskTraces, taskTraces...)
					if clock != nil {
						result.traces = append(result.traces, trialTrace{rowIndex: len(result.Rows) - 1, started: started, finished: finished})
					}
				}
			}
		}
	}
	evidence.Result = result
	return evidence, nil
}

func schedulerCaseByID(profile schedulerProfile, caseID string) (schedulerCase, error) {
	for _, definition := range profile.Cases {
		if definition.ID == caseID {
			return definition, nil
		}
	}
	return schedulerCase{}, fmt.Errorf("scheduler case %q is not declared", caseID)
}

func schedulerPairID(workload schedulerWorkload, repetition int, treatment string) string {
	phase := ""
	if workload.Phase == "warmup" {
		phase = "-warmup"
	}
	return fmt.Sprintf("%s%s-%d-%d-%s", strings.ToLower(workload.CaseID), phase, workload.Seed, repetition, treatment)
}

func validatePlan(plan SchedulerPlan) error {
	if len(plan.Seeds) == 0 || len(plan.Seeds) > 128 {
		return fmt.Errorf("seeds must contain between 1 and 128 values")
	}
	if plan.Repetitions <= 0 || plan.Repetitions > 1000 {
		return fmt.Errorf("repetitions must be between 1 and 1000")
	}
	if len(plan.Seeds)*plan.Repetitions > maxSchedulerPlanCells {
		return fmt.Errorf("seed-repetition cells must not exceed %d", maxSchedulerPlanCells)
	}
	seen := make(map[uint32]struct{}, len(plan.Seeds))
	for _, seed := range plan.Seeds {
		if _, exists := seen[seed]; exists {
			return fmt.Errorf("duplicate seed %d", seed)
		}
		seen[seed] = struct{}{}
	}
	return nil
}

func runSchedulerWorkload(workload schedulerWorkload, policyName string) (trialMetrics, map[string]*taskTiming, error) {
	policy, err := schedulerPolicy(policyName, workload.Capacity)
	if err != nil {
		return trialMetrics{}, nil, err
	}
	engine, err := scheduler.NewEngine(policy, workload.Capacity)
	if err != nil {
		return trialMetrics{}, nil, err
	}
	specs := make([]domain.TaskSpec, 0, len(workload.Tasks))
	metadata := make(map[string]workloadTask, len(workload.Tasks))
	for _, item := range workload.Tasks {
		specs = append(specs, item.Spec.Clone())
		metadata[item.Spec.ID] = item
	}
	graph, err := taskgraph.New(specs...)
	if err != nil {
		return trialMetrics{}, nil, err
	}
	timings := make(map[string]*taskTiming, len(specs))
	for _, ready := range graph.ReadyTasks() {
		timings[ready.ID] = newTaskTiming(0)
	}
	logicalMS := int64(0)
	running := make([]completion, 0, len(specs))
	metrics := trialMetrics{}
	for completed := 0; completed < len(specs); {
		finished := popFinished(&running, logicalMS)
		for _, item := range finished {
			if err := engine.Complete(item.taskID, item.resource); err != nil {
				return trialMetrics{}, nil, err
			}
			newlyReady, completeErr := graph.Complete(item.taskID)
			if completeErr != nil {
				return trialMetrics{}, nil, completeErr
			}
			timings[item.taskID].terminalMS = logicalMS
			completed++
			for _, task := range newlyReady {
				timings[task.ID] = newTaskTiming(logicalMS)
			}
		}
		for {
			ready := graph.ReadyTasks()
			if len(ready) == 0 {
				break
			}
			candidates := make([]scheduler.Candidate, 0, len(ready))
			for _, spec := range ready {
				item := metadata[spec.ID]
				candidates = append(candidates, scheduler.Candidate{
					TaskID: spec.ID, WorkflowID: spec.WorkflowID, Priority: spec.Priority,
					InheritedPriority: item.InheritedPriority, CriticalPath: item.CriticalPath,
					UnlockCount: item.UnlockCount, ReadyAt: logicalTime(timings[spec.ID].readyMS), Resources: spec.Resources.Clone(),
				})
			}
			decision, decideErr := engine.DecideAndReserve(candidates, logicalTime(logicalMS))
			if errors.Is(decideErr, scheduler.ErrNoCandidate) {
				// All requests fit total capacity. NoCandidate here only means the
				// single slot is temporarily reserved, so the task remains queued;
				// it is not a permanent admission rejection.
				break
			}
			if decideErr != nil {
				return trialMetrics{}, nil, decideErr
			}
			if _, err := graph.Transition(decision.Candidate.TaskID, domain.TaskStatusAdmitted); err != nil {
				return trialMetrics{}, nil, err
			}
			if _, err := graph.Transition(decision.Candidate.TaskID, domain.TaskStatusRunning); err != nil {
				return trialMetrics{}, nil, err
			}
			timings[decision.Candidate.TaskID].dispatchMS = logicalMS
			timings[decision.Candidate.TaskID].dispatchSequence = metrics.decisionCount
			item := metadata[decision.Candidate.TaskID]
			running = append(running, completion{taskID: item.Spec.ID, finishMS: logicalMS + item.ServiceMS, resource: item.Spec.Resources.Clone()})
			metrics.decisionCount++
		}
		if completed == len(specs) {
			break
		}
		if len(running) == 0 {
			return trialMetrics{}, nil, fmt.Errorf("simulation deadlock at %dms", logicalMS)
		}
		logicalMS = nextCompletion(running)
	}
	metrics, err = calculateMetrics(workload, timings, metrics)
	return metrics, timings, err
}

func newTaskTiming(readyMS int64) *taskTiming {
	return &taskTiming{readyMS: readyMS, dispatchMS: -1, terminalMS: -1, dispatchSequence: -1}
}

func schedulerPolicy(name string, capacity domain.ResourceVector) (scheduler.Policy, error) {
	switch name {
	case "fifo":
		return scheduler.FIFO{}, nil
	case "priority":
		return scheduler.Priority{AgingInterval: time.Duration(policyAgingMS) * time.Millisecond}, nil
	case "dag-drcfs":
		return scheduler.NewDAGDRCFS(capacity, time.Duration(policyAgingMS)*time.Millisecond)
	default:
		return nil, fmt.Errorf("unsupported scheduler policy %q", name)
	}
}

func logicalTime(milliseconds int64) time.Time {
	return time.Unix(0, milliseconds*int64(time.Millisecond)).UTC()
}

func popFinished(running *[]completion, now int64) []completion {
	finished := make([]completion, 0)
	remaining := (*running)[:0]
	for _, item := range *running {
		if item.finishMS == now {
			finished = append(finished, item)
		} else {
			remaining = append(remaining, item)
		}
	}
	*running = remaining
	sort.Slice(finished, func(i, j int) bool { return finished[i].taskID < finished[j].taskID })
	return finished
}

func nextCompletion(running []completion) int64 {
	next := int64(math.MaxInt64)
	for _, item := range running {
		if item.finishMS < next {
			next = item.finishMS
		}
	}
	return next
}
