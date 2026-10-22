package runtimecore

import (
	"errors"
	"fmt"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

var ErrTaskNotCancelable = errors.New("task is not cancelable")

// Cancel atomically releases an active reservation and moves the task to the
// CANCELED terminal state. Repeated cancellation of an already canceled task
// is idempotent.
func (r *Runtime) Cancel(taskID string) (taskgraph.TaskSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	if _, pending := r.pendingCompletions[taskID]; pending {
		return taskgraph.TaskSnapshot{}, errors.Join(ErrTaskNotCancelable, ErrCompletionPending)
	}

	item, exists := r.graph.Task(taskID)
	if !exists {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("%w: %q", taskgraph.ErrTaskNotFound, taskID)
	}
	switch item.Status {
	case domain.TaskStatusCanceled:
		return item, nil
	case domain.TaskStatusSucceeded, domain.TaskStatusFailed:
		return taskgraph.TaskSnapshot{}, fmt.Errorf("%w: task %q is %s", ErrTaskNotCancelable, taskID, item.Status)
	case domain.TaskStatusAdmitted, domain.TaskStatusRunning:
		before := r.mutationSnapshotLocked(time.Now().UTC())
		if err := r.scheduler.Complete(taskID, declaredAttemptCharge(item)); err != nil {
			return taskgraph.TaskSnapshot{}, fmt.Errorf("release task %q reservation: %w", taskID, err)
		}
		return r.cancelLocked(taskID, before)
	case domain.TaskStatusSubmitted, domain.TaskStatusWaitingDependency,
		domain.TaskStatusReady, domain.TaskStatusLost, domain.TaskStatusRecovering:
		// These states do not own a scheduler reservation.
	default:
		return taskgraph.TaskSnapshot{}, fmt.Errorf("%w: task %q is %s", ErrTaskNotCancelable, taskID, item.Status)
	}

	return r.cancelLocked(taskID, r.mutationSnapshotLocked(time.Now().UTC()))
}

func (r *Runtime) cancelLocked(taskID string, before *Checkpoint) (taskgraph.TaskSnapshot, error) {
	if _, err := r.graph.Transition(taskID, domain.TaskStatusCanceled); err != nil {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("cancel task %q: %w", taskID, err)
	}
	delete(r.readySince, taskID)
	delete(r.cancelRequested, taskID)
	canceled, exists := r.graph.Task(taskID)
	if !exists {
		return taskgraph.TaskSnapshot{}, fmt.Errorf("%w: %q disappeared after cancellation", taskgraph.ErrTaskNotFound, taskID)
	}
	if err := r.commitMutationLocked(before, time.Now().UTC()); err != nil {
		return taskgraph.TaskSnapshot{}, err
	}
	return canceled, nil
}
