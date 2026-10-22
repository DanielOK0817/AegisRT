package runtimecore

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/taskgraph"
)

var ErrTaskExceedsCapacity = errors.New("task resource request exceeds runtime capacity")

type Runtime struct {
	mu                            sync.Mutex
	instanceID                    string
	legacyContextMigrationPending bool
	graph                         *taskgraph.TaskGraph
	scheduler                     *scheduler.Engine
	readySince                    map[string]time.Time
	attempts                      map[string]int
	cancelRequested               map[string]struct{}
	pendingCompletions            map[string]PendingCompletion
	checkpoint                    CheckpointStore
	configFingerprint             string
	generation                    uint64
	lastCheckpointAt              time.Time
	poisoned                      error
	recoveryHook                  RecoveryHook
}

type Stats struct {
	Policy                 string                `json:"policy"`
	GraphVersion           uint64                `json:"graph_version"`
	TaskCount              int                   `json:"task_count"`
	ReadyCount             int                   `json:"ready_count"`
	PendingCompletionCount int                   `json:"pending_completion_count"`
	Capacity               domain.ResourceVector `json:"capacity"`
	Allocated              domain.ResourceVector `json:"allocated"`
	Durable                bool                  `json:"durable"`
	CheckpointHealthy      bool                  `json:"checkpoint_healthy"`
	Generation             uint64                `json:"generation"`
	LastCheckpointAt       time.Time             `json:"last_checkpoint_at,omitempty"`
}

func New(policy scheduler.Policy, capacity domain.ResourceVector) (*Runtime, error) {
	instanceID, err := newRuntimeInstanceID()
	if err != nil {
		return nil, err
	}
	graph, err := taskgraph.New()
	if err != nil {
		return nil, err
	}
	engine, err := scheduler.NewEngine(policy, capacity)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		instanceID:         instanceID,
		graph:              graph,
		scheduler:          engine,
		readySince:         make(map[string]time.Time),
		attempts:           make(map[string]int),
		cancelRequested:    make(map[string]struct{}),
		pendingCompletions: make(map[string]PendingCompletion),
	}, nil
}

// InstanceID returns the durable identity of this Runtime state lineage.
// Persistent runtimes retain the same identity across process restarts.
func (r *Runtime) InstanceID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instanceID
}

func (r *Runtime) ApplyPatch(tasks []domain.TaskSpec, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	capacity := r.scheduler.Capacity()
	for _, task := range tasks {
		if !task.Resources.FitsWithin(capacity) {
			return fmt.Errorf("%w: task %q", ErrTaskExceedsCapacity, task.ID)
		}
	}
	before := r.mutationSnapshotLocked(now)
	version := r.graph.Version()
	if err := r.graph.ApplyPatch(taskgraph.Patch{Tasks: tasks}); err != nil {
		return err
	}
	if r.graph.Version() == version {
		return nil
	}
	r.recordReadyLocked(r.graph.ReadyTasks(), now)
	return r.commitMutationLocked(before, now)
}

func (r *Runtime) Schedule(now time.Time) (scheduler.Decision, error) {
	return r.ScheduleEligible(now, nil)
}

// ScheduleEligible schedules only tasks accepted by eligible. A nil predicate
// accepts every ready task. The predicate runs while the Runtime lock is held
// and must not call back into Runtime.
func (r *Runtime) ScheduleEligible(now time.Time, eligible func(domain.TaskSpec) bool) (scheduler.Decision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return scheduler.Decision{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ready := r.graph.ReadyTasks()
	if eligible != nil {
		filtered := make([]domain.TaskSpec, 0, len(ready))
		for _, task := range ready {
			if eligible(task) && r.attempts[task.ID] < task.MaxAttempts {
				filtered = append(filtered, task)
			}
		}
		ready = filtered
	} else {
		filtered := ready[:0]
		for _, task := range ready {
			if r.attempts[task.ID] < task.MaxAttempts {
				filtered = append(filtered, task)
			}
		}
		ready = filtered
	}
	if len(ready) == 0 {
		return scheduler.Decision{}, scheduler.ErrNoCandidate
	}
	candidates := r.candidatesLocked(ready, now)
	before := r.mutationSnapshotLocked(now)
	decision, err := r.scheduler.DecideAndReserve(candidates, now)
	if err != nil {
		return scheduler.Decision{}, err
	}
	taskID := decision.Candidate.TaskID
	if _, err = r.graph.Transition(taskID, domain.TaskStatusAdmitted); err != nil {
		_ = r.scheduler.Complete(taskID, domain.ResourceVector{})
		return scheduler.Decision{}, fmt.Errorf("admit selected task: %w", err)
	}
	delete(r.readySince, taskID)
	r.attempts[taskID]++
	if err = r.commitMutationLocked(before, now); err != nil {
		return scheduler.Decision{}, err
	}
	return decision, nil
}

func (r *Runtime) Complete(taskID string, actualUsage domain.ResourceVector, now time.Time) ([]domain.TaskSpec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	item, exists := r.graph.Task(taskID)
	if !exists {
		return nil, fmt.Errorf("task %q not found", taskID)
	}
	if item.Status != domain.TaskStatusRunning && item.Status != domain.TaskStatusAdmitted {
		return nil, fmt.Errorf("task %q is not active: %s", taskID, item.Status)
	}
	before := r.mutationSnapshotLocked(now)
	settledAt := now
	if pending, ok := r.pendingCompletions[taskID]; ok {
		settledAt = pending.PreparedAt
	}
	if err := r.scheduler.CompleteAt(taskID, actualUsage, settledAt); err != nil {
		return nil, err
	}
	newlyReady, err := r.graph.Complete(taskID)
	if err != nil {
		return nil, fmt.Errorf("complete task graph node after releasing resources: %w", err)
	}
	r.recordReadyLocked(newlyReady, now)
	delete(r.cancelRequested, taskID)
	delete(r.pendingCompletions, taskID)
	if err = r.commitMutationLocked(before, now); err != nil {
		return nil, err
	}
	return newlyReady, nil
}

// Fail terminates an active task attempt and releases its reservation. Retry
// policy and recovery transitions are introduced in M3.
func (r *Runtime) Fail(taskID string, actualUsage domain.ResourceVector) (taskgraph.TaskSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	if _, pending := r.pendingCompletions[taskID]; pending {
		return taskgraph.TaskSnapshot{}, ErrCompletionPending
	}
	item, exists := r.graph.Task(taskID)
	if !exists {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("task %q not found", taskID)
	}
	if item.Status != domain.TaskStatusRunning && item.Status != domain.TaskStatusAdmitted {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("task %q is not active: %s", taskID, item.Status)
	}
	if err := actualUsage.Validate(); err != nil {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("actual usage: %w", err)
	}
	before := r.mutationSnapshotLocked(time.Now().UTC())
	if err := r.scheduler.Complete(taskID, actualUsage); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	if _, err := r.graph.Transition(taskID, domain.TaskStatusFailed); err != nil {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("fail task graph node after releasing resources: %w", err)
	}
	failed, exists := r.graph.Task(taskID)
	if !exists {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("failed task %q disappeared", taskID)
	}
	delete(r.cancelRequested, taskID)
	if err := r.commitMutationLocked(before, time.Now().UTC()); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	return failed, nil
}

func (r *Runtime) Tasks() []taskgraph.TaskSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.graph.Snapshot()
}

func (r *Runtime) Task(taskID string) (taskgraph.TaskSnapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.graph.Task(taskID)
}

func (r *Runtime) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Stats{
		Policy:                 r.scheduler.PolicyName(),
		GraphVersion:           r.graph.Version(),
		TaskCount:              r.graph.Len(),
		ReadyCount:             len(r.graph.ReadyTasks()),
		PendingCompletionCount: len(r.pendingCompletions),
		Capacity:               r.scheduler.Capacity(),
		Allocated:              r.scheduler.Allocated(),
		Durable:                r.checkpoint != nil,
		CheckpointHealthy:      r.poisoned == nil,
		Generation:             r.generation,
		LastCheckpointAt:       r.lastCheckpointAt,
	}
}

func (r *Runtime) AttemptNumber(taskID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts[taskID]
}

func (r *Runtime) Healthy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.poisoned == nil
}

func (r *Runtime) recordReadyLocked(tasks []domain.TaskSpec, now time.Time) {
	for _, task := range tasks {
		if _, exists := r.readySince[task.ID]; !exists {
			r.readySince[task.ID] = now
		}
	}
}

func (r *Runtime) candidatesLocked(ready []domain.TaskSpec, now time.Time) []scheduler.Candidate {
	snapshot := r.graph.Snapshot()
	dependents := make(map[string][]string, len(snapshot))
	priorities := make(map[string]int, len(snapshot))
	for _, task := range snapshot {
		priorities[task.Spec.ID] = task.Spec.Priority
		for _, dependencyID := range task.Spec.Dependencies {
			dependents[dependencyID] = append(dependents[dependencyID], task.Spec.ID)
		}
	}
	memo := make(map[string]dagMetrics, len(snapshot))
	var metrics func(string) dagMetrics
	metrics = func(taskID string) dagMetrics {
		if value, exists := memo[taskID]; exists {
			return value
		}
		value := dagMetrics{criticalPath: 1}
		for _, childID := range dependents[taskID] {
			child := metrics(childID)
			if child.criticalPath+1 > value.criticalPath {
				value.criticalPath = child.criticalPath + 1
			}
			if priorities[childID] > value.inheritedPriority {
				value.inheritedPriority = priorities[childID]
			}
			if child.inheritedPriority > value.inheritedPriority {
				value.inheritedPriority = child.inheritedPriority
			}
		}
		memo[taskID] = value
		return value
	}

	candidates := make([]scheduler.Candidate, 0, len(ready))
	for _, task := range ready {
		value := metrics(task.ID)
		readyAt := r.readySince[task.ID]
		if readyAt.IsZero() {
			readyAt = now
		}
		candidates = append(candidates, scheduler.Candidate{
			TaskID:            task.ID,
			WorkflowID:        task.WorkflowID,
			Priority:          task.Priority,
			InheritedPriority: value.inheritedPriority,
			CriticalPath:      value.criticalPath,
			UnlockCount:       len(dependents[task.ID]),
			ReadyAt:           readyAt,
			Resources:         task.Resources.Clone(),
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].TaskID < candidates[j].TaskID })
	return candidates
}

type dagMetrics struct {
	criticalPath      int
	inheritedPriority int
}
