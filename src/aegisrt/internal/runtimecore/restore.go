package runtimecore

import (
	"fmt"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/taskgraph"
)

func (r *Runtime) restoreExactLocked(checkpoint Checkpoint) error {
	if err := validateRuntimeInstanceID(checkpoint.InstanceID); err != nil {
		return fmt.Errorf("%w: instance_id: %v", ErrInvalidCheckpoint, err)
	}
	graph, err := taskgraph.Restore(checkpoint.Graph)
	if err != nil {
		return fmt.Errorf("%w: graph: %v", ErrInvalidCheckpoint, err)
	}
	readySince, attempts, cancelRequested, err := validateRuntimeRows(checkpoint, graph)
	if err != nil {
		return err
	}
	if err = validateReservations(checkpoint.Scheduler, graph); err != nil {
		return err
	}
	if checkpoint.Scheduler.PolicyName == "dag-drcfs-time" {
		for _, candidate := range checkpoint.Scheduler.Reservations {
			item, _ := graph.Task(candidate.TaskID)
			if (item.Status == domain.TaskStatusRunning) == candidate.ServiceStartedAt.IsZero() || candidate.ServiceStartedAt.After(checkpoint.SavedAt) {
				return fmt.Errorf("%w: invalid observed service interval for %q", ErrInvalidCheckpoint, candidate.TaskID)
			}
		}
	}
	pending, err := validatePendingCompletions(checkpoint, graph, attempts, cancelRequested)
	if err != nil {
		return err
	}
	if err = r.scheduler.Restore(checkpoint.Scheduler); err != nil {
		return fmt.Errorf("%w: scheduler: %v", ErrInvalidCheckpoint, err)
	}
	r.graph = graph
	r.instanceID = checkpoint.InstanceID
	r.legacyContextMigrationPending = checkpoint.LegacyContextMigrationPending
	r.readySince = readySince
	r.attempts = attempts
	r.cancelRequested = cancelRequested
	r.pendingCompletions = pending
	r.generation = checkpoint.Generation
	r.lastCheckpointAt = checkpoint.SavedAt
	return nil
}

func validateRuntimeRows(
	checkpoint Checkpoint,
	graph *taskgraph.TaskGraph,
) (map[string]time.Time, map[string]int, map[string]struct{}, error) {
	tasks := make(map[string]taskgraph.TaskSnapshot, graph.Len())
	for _, item := range graph.Snapshot() {
		tasks[item.Spec.ID] = item
	}
	readySince := make(map[string]time.Time, len(checkpoint.ReadySince))
	for _, item := range checkpoint.ReadySince {
		task, exists := tasks[item.TaskID]
		if !exists || task.Status != domain.TaskStatusReady || item.ReadyAt.IsZero() {
			return nil, nil, nil, fmt.Errorf("%w: invalid ready timestamp for %q", ErrInvalidCheckpoint, item.TaskID)
		}
		if _, exists = readySince[item.TaskID]; exists {
			return nil, nil, nil, fmt.Errorf("%w: duplicate ready timestamp for %q", ErrInvalidCheckpoint, item.TaskID)
		}
		readySince[item.TaskID] = item.ReadyAt
	}
	for taskID, item := range tasks {
		_, tracked := readySince[taskID]
		if item.Status == domain.TaskStatusReady && !tracked {
			return nil, nil, nil, fmt.Errorf("%w: ready task %q has no timestamp", ErrInvalidCheckpoint, taskID)
		}
	}
	attempts := make(map[string]int, len(checkpoint.Attempts))
	for _, item := range checkpoint.Attempts {
		task, exists := tasks[item.TaskID]
		if !exists || item.Number <= 0 || item.Number > task.Spec.MaxAttempts {
			return nil, nil, nil, fmt.Errorf("%w: invalid attempt counter for %q", ErrInvalidCheckpoint, item.TaskID)
		}
		if _, exists = attempts[item.TaskID]; exists {
			return nil, nil, nil, fmt.Errorf("%w: duplicate attempt counter for %q", ErrInvalidCheckpoint, item.TaskID)
		}
		attempts[item.TaskID] = item.Number
	}
	for taskID, item := range tasks {
		if isRecoverableStatus(item.Status) && attempts[taskID] == 0 {
			return nil, nil, nil, fmt.Errorf("%w: active task %q has no attempt", ErrInvalidCheckpoint, taskID)
		}
		if item.Status == domain.TaskStatusReady && attempts[taskID] >= item.Spec.MaxAttempts {
			return nil, nil, nil, fmt.Errorf("%w: ready task %q exhausted attempts", ErrInvalidCheckpoint, taskID)
		}
	}
	cancelRequested := make(map[string]struct{}, len(checkpoint.CancelRequested))
	for _, taskID := range checkpoint.CancelRequested {
		task, exists := tasks[taskID]
		if !exists || (task.Status != domain.TaskStatusAdmitted && task.Status != domain.TaskStatusRunning) {
			return nil, nil, nil, fmt.Errorf("%w: invalid cancel intent for %q", ErrInvalidCheckpoint, taskID)
		}
		if _, exists = cancelRequested[taskID]; exists {
			return nil, nil, nil, fmt.Errorf("%w: duplicate cancel intent for %q", ErrInvalidCheckpoint, taskID)
		}
		cancelRequested[taskID] = struct{}{}
	}
	return readySince, attempts, cancelRequested, nil
}

func validateReservations(state scheduler.State, graph *taskgraph.TaskGraph) error {
	reservations := make(map[string]scheduler.Candidate, len(state.Reservations))
	for _, candidate := range state.Reservations {
		item, exists := graph.Task(candidate.TaskID)
		if !exists || (item.Status != domain.TaskStatusAdmitted && item.Status != domain.TaskStatusRunning) {
			return fmt.Errorf("%w: reservation %q has no active task", ErrInvalidCheckpoint, candidate.TaskID)
		}
		if candidate.WorkflowID != item.Spec.WorkflowID || candidate.Priority != item.Spec.Priority ||
			!equalResourceVectors(candidate.Resources, item.Spec.Resources) {
			return fmt.Errorf("%w: reservation %q does not match task", ErrInvalidCheckpoint, candidate.TaskID)
		}
		reservations[candidate.TaskID] = candidate
	}
	for _, item := range graph.Snapshot() {
		if item.Status == domain.TaskStatusAdmitted || item.Status == domain.TaskStatusRunning {
			if _, exists := reservations[item.Spec.ID]; !exists {
				return fmt.Errorf("%w: active task %q has no reservation", ErrInvalidCheckpoint, item.Spec.ID)
			}
		}
	}
	return nil
}

func (r *Runtime) normalizeRecoveredLocked(now time.Time) (RecoveryReport, error) {
	report := RecoveryReport{}
	snapshot := r.graph.Snapshot()
	for _, item := range snapshot {
		if _, pending := r.pendingCompletions[item.Spec.ID]; pending {
			continue
		}
		if !isRecoverableStatus(item.Status) {
			continue
		}
		if r.recoveryHook == nil {
			return RecoveryReport{}, fmt.Errorf("%w: active attempt cleanup hook is required", ErrRecoveryBlocked)
		}
		if err := r.recoveryHook(item.Spec.ID, r.attempts[item.Spec.ID]); err != nil {
			return RecoveryReport{}, fmt.Errorf("%w: clean attempt for task %q: %v", ErrRecoveryBlocked, item.Spec.ID, err)
		}
	}
	for _, item := range snapshot {
		if _, pending := r.pendingCompletions[item.Spec.ID]; pending {
			continue
		}
		if !isRecoverableStatus(item.Status) {
			continue
		}
		report.Recovered = true
		taskID := item.Spec.ID
		if item.Status == domain.TaskStatusAdmitted || item.Status == domain.TaskStatusRunning {
			// The last durable observation bounds measured service. Offline time
			// and unobserved execution after that checkpoint are not invented.
			if err := r.scheduler.CompleteAt(taskID, declaredAttemptCharge(item), r.lastCheckpointAt); err != nil {
				return RecoveryReport{}, fmt.Errorf("%w: release %q: %v", ErrInvalidCheckpoint, taskID, err)
			}
			if _, canceled := r.cancelRequested[taskID]; canceled {
				if _, err := r.graph.Transition(taskID, domain.TaskStatusCanceled); err != nil {
					return RecoveryReport{}, err
				}
				report.CanceledTaskIDs = append(report.CanceledTaskIDs, taskID)
				delete(r.cancelRequested, taskID)
				continue
			}
			if _, err := r.graph.Transition(taskID, domain.TaskStatusLost); err != nil {
				return RecoveryReport{}, err
			}
		}
		current, _ := r.graph.Task(taskID)
		if current.Status == domain.TaskStatusLost {
			if _, err := r.graph.Transition(taskID, domain.TaskStatusRecovering); err != nil {
				return RecoveryReport{}, err
			}
		}
		next := domain.TaskStatusFailed
		if r.attempts[taskID] < item.Spec.MaxAttempts {
			next = domain.TaskStatusReady
		}
		if _, err := r.graph.Transition(taskID, next); err != nil {
			return RecoveryReport{}, err
		}
		if next == domain.TaskStatusReady {
			r.readySince[taskID] = now
			report.RequeuedTaskIDs = append(report.RequeuedTaskIDs, taskID)
		} else {
			delete(r.readySince, taskID)
			report.FailedTaskIDs = append(report.FailedTaskIDs, taskID)
		}
		delete(r.cancelRequested, taskID)
	}
	return report, nil
}

func isRecoverableStatus(status domain.TaskStatus) bool {
	return status == domain.TaskStatusAdmitted || status == domain.TaskStatusRunning ||
		status == domain.TaskStatusLost || status == domain.TaskStatusRecovering
}

func equalResourceVectors(left, right domain.ResourceVector) bool {
	return left.FitsWithin(right) && right.FitsWithin(left)
}
