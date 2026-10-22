package taskgraph

import "errors"

var (
	ErrDuplicateTask           = errors.New("duplicate task id")
	ErrMissingDependency       = errors.New("missing dependency")
	ErrCycleDetected           = errors.New("task graph contains a cycle")
	ErrCrossWorkflowDependency = errors.New("cross-workflow dependency")
	ErrTaskNotFound            = errors.New("task not found")
	ErrInvalidTaskSpec         = errors.New("invalid task spec")
	ErrInvalidTransition       = errors.New("invalid task status transition")
	ErrDependenciesIncomplete  = errors.New("task dependencies are incomplete")
	ErrEmptyPatch              = errors.New("task graph patch is empty")
	ErrGraphLimit              = errors.New("task graph limit exceeded")
	ErrCanceledDependency      = errors.New("dependency cannot succeed after cancellation")
)
