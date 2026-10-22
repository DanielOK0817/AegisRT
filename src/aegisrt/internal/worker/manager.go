package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
)

var (
	ErrInvalidExecution   = errors.New("invalid worker execution")
	ErrUnsupportedRuntime = errors.New("unsupported agent runtime")
)

type Isolation interface {
	PrepareHook(domain.ResourceVector) executor.PrepareHook
}

type Request struct {
	AttemptID     string
	AttemptNumber int
	WorkerID      string
	Agent         domain.AgentSpec
	Task          domain.TaskSpec
	Environment   map[string]string
	ContextRef    string
	ContextText   string
	OnStarted     executor.StartHook
}

type Result struct {
	Attempt domain.AgentAttempt    `json:"attempt"`
	Process executor.Result        `json:"process"`
	Output  agentprotocol.Envelope `json:"output,omitempty"`
}

type Manager struct {
	baseEnvironment []string
	isolation       Isolation
}

func NewManager(baseEnvironment []string, isolation Isolation) *Manager {
	return &Manager{
		baseEnvironment: append([]string(nil), baseEnvironment...),
		isolation:       isolation,
	}
}

func (m *Manager) Run(ctx context.Context, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	input, err := json.Marshal(struct {
		TaskID         string `json:"task_id"`
		WorkflowID     string `json:"workflow_id"`
		AttemptID      string `json:"attempt_id"`
		AttemptNumber  int    `json:"attempt_number"`
		IdempotencyKey string `json:"idempotency_key"`
		Goal           string `json:"goal"`
		ContextRef     string `json:"context_ref,omitempty"`
		Context        string `json:"context,omitempty"`
	}{
		TaskID: request.Task.ID, WorkflowID: request.Task.WorkflowID,
		AttemptID: request.AttemptID, AttemptNumber: request.AttemptNumber,
		IdempotencyKey: request.Task.IdempotencyKey, Goal: request.Task.Goal,
		ContextRef: request.ContextRef, Context: request.ContextText,
	})
	if err != nil {
		return Result{}, fmt.Errorf("encode worker input: %w", err)
	}
	hooks := make([]executor.PrepareHook, 0, 1)
	if m.isolation != nil {
		hooks = append(hooks, m.isolation.PrepareHook(request.Task.Resources))
	}
	runner := executor.NewProcessRunnerWithPrepare(m.baseEnvironment, hooks...)
	processResult, runErr := runner.Run(ctx, executor.Request{
		AttemptID:      request.AttemptID,
		Command:        request.Agent.Entrypoint,
		Args:           append([]string(nil), request.Agent.Args...),
		Env:            cloneEnvironment(request.Environment),
		Stdin:          input,
		Timeout:        effectiveTimeout(request.Agent.ExecutionTimeout, request.Task.Timeout),
		MaxOutputBytes: agentprotocol.MaxOutputBytes,
		OnStarted:      request.OnStarted,
	})
	output := agentprotocol.Envelope{}
	if runErr == nil {
		var outputErr error
		output, outputErr = agentprotocol.Parse(processResult.Stdout, processResult.StdoutTruncated)
		if outputErr != nil {
			runErr = fmt.Errorf("parse Agent output: %w", outputErr)
		}
	}
	attempt := domain.AgentAttempt{
		ID:             request.AttemptID,
		TaskID:         request.Task.ID,
		AgentID:        request.Agent.ID,
		Number:         request.AttemptNumber,
		WorkerID:       request.WorkerID,
		Allocated:      request.Task.Resources.Clone(),
		IdempotencyKey: request.Task.IdempotencyKey,
		StartedAt:      processResult.StartedAt,
		FinishedAt:     processResult.FinishedAt,
	}
	switch {
	case runErr == nil:
		attempt.Status = domain.AttemptStatusSucceeded
	case errors.Is(runErr, executor.ErrCanceled):
		attempt.Status = domain.AttemptStatusCanceled
	default:
		attempt.Status = domain.AttemptStatusFailed
		attempt.Failure = runErr.Error()
	}
	if err := attempt.Validate(); err != nil {
		return Result{}, fmt.Errorf("final attempt invariant: %w", err)
	}
	return Result{Attempt: attempt, Process: processResult, Output: output}, runErr
}

func validateRequest(request Request) error {
	if err := request.Agent.Validate(); err != nil {
		return fmt.Errorf("%w: agent: %v", ErrInvalidExecution, err)
	}
	if err := request.Task.Validate(); err != nil {
		return fmt.Errorf("%w: task: %v", ErrInvalidExecution, err)
	}
	if request.Agent.Runtime != "process" {
		return fmt.Errorf("%w: %q", ErrUnsupportedRuntime, request.Agent.Runtime)
	}
	if request.Task.AgentID != request.Agent.ID {
		return fmt.Errorf("%w: task agent %q does not match %q", ErrInvalidExecution, request.Task.AgentID, request.Agent.ID)
	}
	if request.AttemptNumber <= 0 || request.AttemptID == "" || request.WorkerID == "" {
		return fmt.Errorf("%w: attempt id, number, and worker id are required", ErrInvalidExecution)
	}
	pending := domain.AgentAttempt{
		ID: request.AttemptID, TaskID: request.Task.ID, AgentID: request.Agent.ID,
		Number: request.AttemptNumber, Status: domain.AttemptStatusPending,
		Allocated: request.Task.Resources.Clone(), IdempotencyKey: request.Task.IdempotencyKey,
	}
	if err := pending.Validate(); err != nil {
		return fmt.Errorf("%w: attempt: %v", ErrInvalidExecution, err)
	}
	return nil
}

func effectiveTimeout(agentTimeout, taskTimeout time.Duration) time.Duration {
	if agentTimeout < taskTimeout {
		return agentTimeout
	}
	return taskTimeout
}

func cloneEnvironment(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}
