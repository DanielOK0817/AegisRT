package runtimecore

import (
	"fmt"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

// RequestCancel durably records explicit user intent before the owning worker
// receives a cancellation signal. A restart can therefore distinguish user
// cancellation from daemon shutdown.
func (r *Runtime) RequestCancel(taskID string) (taskgraph.TaskSnapshot, error) {
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
		return taskgraph.TaskSnapshot{}, fmt.Errorf("%w: %q", taskgraph.ErrTaskNotFound, taskID)
	}
	if item.Status != domain.TaskStatusRunning && item.Status != domain.TaskStatusAdmitted {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("task %q is not active: %s", taskID, item.Status)
	}
	if _, exists := r.cancelRequested[taskID]; exists {
		return item, nil
	}
	now := time.Now().UTC()
	before := r.mutationSnapshotLocked(now)
	r.cancelRequested[taskID] = struct{}{}
	if err := r.commitMutationLocked(before, now); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	return item, nil
}

func (r *Runtime) CancelRequested(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.cancelRequested[taskID]
	return exists
}

// Interrupt records an attempt lost because its execution owner stopped. It
// requeues only when the task still has an attempt available.
func (r *Runtime) Interrupt(taskID string, now time.Time) (taskgraph.TaskSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	if _, pending := r.pendingCompletions[taskID]; pending {
		return taskgraph.TaskSnapshot{}, ErrCompletionPending
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	item, exists := r.graph.Task(taskID)
	if !exists {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("%w: %q", taskgraph.ErrTaskNotFound, taskID)
	}
	if item.Status != domain.TaskStatusRunning && item.Status != domain.TaskStatusAdmitted {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("task %q is not active: %s", taskID, item.Status)
	}
	before := r.mutationSnapshotLocked(now)
	if err := r.scheduler.CompleteAt(taskID, declaredAttemptCharge(item), now); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	if _, err := r.graph.Transition(taskID, domain.TaskStatusLost); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	if _, err := r.graph.Transition(taskID, domain.TaskStatusRecovering); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	next := domain.TaskStatusFailed
	if r.attempts[taskID] < item.Spec.MaxAttempts {
		next = domain.TaskStatusReady
	}
	if _, err := r.graph.Transition(taskID, next); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	if next == domain.TaskStatusReady {
		r.readySince[taskID] = now
	} else {
		delete(r.readySince, taskID)
	}
	delete(r.cancelRequested, taskID)
	result, exists := r.graph.Task(taskID)
	if !exists {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("%w: %q disappeared after interruption", taskgraph.ErrTaskNotFound, taskID)
	}
	if err := r.commitMutationLocked(before, now); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	return result, nil
}
