package controlplane

import (
	"context"
	"errors"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
	"aegisrt/internal/runtimecore"
)

// finalizeOutput serializes the cancellation boundary with the durable
// completion decision. After PrepareCompletion, only publication may retry.
func (c *Coordinator) finalizeOutput(ctx context.Context, task domain.TaskSpec, attempt int, artifacts []agentprotocol.Artifact) ([]domain.TaskSpec, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.finishActiveLocked(task.ID, task.AgentID)
	if c.runtime.CancelRequested(task.ID) {
		_, err := c.runtime.Cancel(task.ID)
		return nil, true, err
	}
	if err := c.runtime.PrepareCompletion(task.ID, attempt, artifacts, time.Now().UTC()); err != nil {
		if c.runtime.Healthy() && !errors.Is(err, runtimecore.ErrCompletionPending) {
			if c.runtime.CancelRequested(task.ID) {
				_, cancelErr := c.runtime.Cancel(task.ID)
				return nil, true, cancelErr
			}
			_, failErr := c.runtime.Fail(task.ID, task.Resources)
			return nil, false, errors.Join(err, failErr)
		}
		return nil, false, err
	}
	if err := c.publishAgentOutput(ctx, task.ID, attempt, artifacts); err != nil {
		c.publicationErr = errors.Join(runtimecore.ErrRuntimeUnavailable, err)
		return nil, false, c.publicationErr
	}
	ready, err := c.runtime.Complete(task.ID, task.Resources, time.Now().UTC())
	return ready, false, err
}

// RecoverOutputs runs before admitting new work. It replays the exact saved
// outputs without launching an Agent or spending another attempt.
func (c *Coordinator) RecoverOutputs(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, record := range c.runtime.PendingCompletions() {
		if err := c.publishAgentOutput(ctx, record.TaskID, record.AttemptNumber, record.Artifacts); err != nil {
			c.publicationErr = errors.Join(runtimecore.ErrRuntimeUnavailable, err)
			return c.publicationErr
		}
		task, exists := c.runtime.Task(record.TaskID)
		if !exists {
			return runtimecore.ErrInvalidCheckpoint
		}
		if _, err := c.runtime.Complete(record.TaskID, task.Spec.Resources, time.Now().UTC()); err != nil {
			return err
		}
	}
	c.publicationErr = nil
	return nil
}

func (c *Coordinator) finishOutput(ctx context.Context, task domain.TaskSpec, attempt int, result TickResult) (TickResult, error) {
	if c.publisher == nil && len(result.Execution.Output.Artifacts) != 0 {
		return result, c.finishExecutionError(ctx, task.ID, task.AgentID, task.Resources, &result, ErrOutputPublisherRequired)
	}
	if validator, ok := c.publisher.(OutputValidator); ok {
		if err := validator.ValidateAgentOutput(ctx, task.ID, attempt, cloneArtifacts(result.Execution.Output.Artifacts)); err != nil {
			return result, c.finishExecutionError(ctx, task.ID, task.AgentID, task.Resources, &result, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, c.finishExecutionError(ctx, task.ID, task.AgentID, task.Resources, &result, err)
	}
	newlyReady, canceled, completeErr := c.finalizeOutput(ctx, task, attempt, result.Execution.Output.Artifacts)
	if completeErr != nil {
		// The process exited, but the terminal task state is not confirmed.
		// Do not publish success or dependency notifications to observers.
		result.Execution.Attempt.Status = domain.AttemptStatusLost
		if task, ok := c.runtime.Task(task.ID); ok && task.Status == domain.TaskStatusFailed {
			result.Execution.Attempt.Status = domain.AttemptStatusFailed
		}
		result.Execution.Attempt.Failure = completeErr.Error()
		return result, completeErr
	}
	result.NewlyReady = newlyReady
	if canceled {
		markExecutionCanceled(&result)
		return result, errors.Join(context.Canceled, completeErr)
	}
	return result, nil
}
