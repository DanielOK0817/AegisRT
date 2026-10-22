package controlplane

import (
	"context"
	"errors"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
)

var ErrOutputPublisherRequired = errors.New("Agent output publisher is required")

type OutputPublisher interface {
	PublishAgentOutput(context.Context, string, int, []agentprotocol.Artifact) error
}

type OutputValidator interface {
	ValidateAgentOutput(context.Context, string, int, []agentprotocol.Artifact) error
}

func (c *Coordinator) publishAgentOutput(
	ctx context.Context,
	taskID string,
	attempt int,
	artifacts []agentprotocol.Artifact,
) error {
	if c.publisher == nil {
		if len(artifacts) == 0 {
			return nil
		}
		return ErrOutputPublisherRequired
	}
	return c.publisher.PublishAgentOutput(ctx, taskID, attempt, cloneArtifacts(artifacts))
}

func (c *Coordinator) finishExecutionError(
	ctx context.Context,
	taskID, agentID string,
	resources domain.ResourceVector,
	result *TickResult,
	executionErr error,
) error {
	deadlineExceeded, wasCanceled := executionFailureSignals(executionErr, ctx.Err())
	canceled, timedOut, interrupted, terminalErr := c.finalizeFailure(
		ctx, taskID, agentID, resources, wasCanceled, deadlineExceeded,
	)
	if canceled && !errors.Is(executionErr, executor.ErrCanceled) && !errors.Is(executionErr, context.Canceled) {
		executionErr = errors.Join(executionErr, context.Canceled)
	}
	if timedOut && !errors.Is(executionErr, executor.ErrTimeout) && !errors.Is(executionErr, context.DeadlineExceeded) {
		executionErr = errors.Join(executionErr, context.DeadlineExceeded)
	}
	if canceled {
		markExecutionCanceled(result)
	}
	if timedOut {
		markExecutionTimedOut(result)
	}
	if interrupted {
		markExecutionInterrupted(result)
		executionErr = errors.Join(executionErr, ErrAttemptInterrupted)
	}
	if !canceled && !timedOut && !interrupted && result.Execution.Attempt.Status == domain.AttemptStatusSucceeded {
		result.Execution.Attempt.Status = domain.AttemptStatusFailed
		result.Execution.Attempt.Failure = executionErr.Error()
	}
	return errors.Join(executionErr, terminalErr)
}

func cloneArtifacts(artifacts []agentprotocol.Artifact) []agentprotocol.Artifact {
	cloned := make([]agentprotocol.Artifact, len(artifacts))
	copy(cloned, artifacts)
	for index := range cloned {
		cloned[index].Recipients = append([]string(nil), artifacts[index].Recipients...)
	}
	return cloned
}
