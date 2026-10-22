package runtimecore

import (
	"context"
	"fmt"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

// MarkStarted records an observed process start for the current admission.
// A stale worker callback must never advance a different attempt.
func (r *Runtime) MarkStarted(taskID string, attempt int, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return err
	}
	item, exists := r.graph.Task(taskID)
	if !exists {
		return fmt.Errorf("%w: %q", taskgraph.ErrTaskNotFound, taskID)
	}
	if attempt <= 0 || r.attempts[taskID] != attempt {
		return fmt.Errorf("stale start for task %q attempt %d", taskID, attempt)
	}
	if _, canceled := r.cancelRequested[taskID]; canceled {
		return context.Canceled
	}
	if item.Status == domain.TaskStatusRunning {
		return nil
	}
	if item.Status != domain.TaskStatusAdmitted {
		return fmt.Errorf("task %q is not admitted: %s", taskID, item.Status)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	before := r.mutationSnapshotLocked(now)
	if err := r.scheduler.MarkStarted(taskID, now); err != nil {
		return err
	}
	if _, err := r.graph.Transition(taskID, domain.TaskStatusRunning); err != nil {
		return err
	}
	return r.commitMutationLocked(before, now)
}

// RequeueUnstarted releases a scheduled admission when infrastructure failed
// before a Worker process was launched. It restores the attempt counter so a
// Context or control-plane outage cannot consume the task's retry budget.
func (r *Runtime) RequeueUnstarted(taskID string, now time.Time) (taskgraph.TaskSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	item, exists := r.graph.Task(taskID)
	if !exists {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("%w: %q", taskgraph.ErrTaskNotFound, taskID)
	}
	if item.Status != domain.TaskStatusAdmitted {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("task %q is not an unstarted admission: %s", taskID, item.Status)
	}
	if r.attempts[taskID] <= 0 {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("task %q has no reserved attempt", taskID)
	}
	before := r.mutationSnapshotLocked(now)
	if err := r.scheduler.Complete(taskID, domain.ResourceVector{}); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	for _, next := range []domain.TaskStatus{
		domain.TaskStatusLost, domain.TaskStatusRecovering, domain.TaskStatusReady,
	} {
		if _, err := r.graph.Transition(taskID, next); err != nil {
			return taskgraph.TaskSnapshot{}, err
		}
	}
	r.attempts[taskID]--
	if r.attempts[taskID] == 0 {
		delete(r.attempts, taskID)
	}
	delete(r.cancelRequested, taskID)
	r.readySince[taskID] = now
	result, exists := r.graph.Task(taskID)
	if !exists {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("%w: %q disappeared after requeue", taskgraph.ErrTaskNotFound, taskID)
	}
	if err := r.commitMutationLocked(before, now); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	return result, nil
}
