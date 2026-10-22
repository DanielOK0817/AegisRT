package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"aegisrt/internal/contextdispatch"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/taskgraph"
	"aegisrt/internal/worker"
)

var (
	ErrUnknownAgent        = errors.New("unknown agent")
	ErrDuplicateAgent      = errors.New("duplicate agent")
	ErrContextBudget       = errors.New("context token budget exceeds platform limits")
	ErrAgentResourcePolicy = errors.New("task exceeds local agent resource policy")
	ErrAttemptInterrupted  = errors.New("attempt interrupted by runtime shutdown")
)

type ContextResolver interface {
	Resolve(context.Context, domain.TaskSpec, domain.AgentSpec, int) (contextdispatch.Resolution, error)
}

type healthyContextResolver interface {
	Healthy() bool
}

type Options struct {
	ContextResolver ContextResolver
	OutputPublisher OutputPublisher
}

type ContextResult struct {
	ManifestID string                    `json:"manifest_id"`
	RefCount   int                       `json:"ref_count"`
	Metrics    contextvm.AssemblyMetrics `json:"metrics"`
}

type TickResult struct {
	Decision   scheduler.Decision `json:"decision"`
	Execution  worker.Result      `json:"execution"`
	NewlyReady []domain.TaskSpec  `json:"newly_ready,omitempty"`
	Context    *ContextResult     `json:"context,omitempty"`
}

// Coordinator closes the M1 control loop: schedule, launch, account, and
// transition. It stays independent from HTTP so execution is not exposed as
// an unauthenticated remote-command endpoint.
type Coordinator struct {
	mu             sync.Mutex
	runtime        *runtimecore.Runtime
	worker         *worker.Manager
	workerID       string
	agents         map[string]domain.AgentSpec
	running        map[string]int
	active         map[string]context.CancelFunc
	resolver       ContextResolver
	publisher      OutputPublisher
	publicationErr error
}

func New(runtime *runtimecore.Runtime, manager *worker.Manager, workerID string, agents []domain.AgentSpec) (*Coordinator, error) {
	return NewWithOptions(runtime, manager, workerID, agents, Options{})
}

func NewWithOptions(runtime *runtimecore.Runtime, manager *worker.Manager, workerID string, agents []domain.AgentSpec, options Options) (*Coordinator, error) {
	if runtime == nil || manager == nil || workerID == "" {
		return nil, errors.New("runtime, worker manager, and worker id are required")
	}
	registry := make(map[string]domain.AgentSpec, len(agents))
	for _, agent := range agents {
		if err := agent.Validate(); err != nil {
			return nil, fmt.Errorf("agent %q: %w", agent.ID, err)
		}
		if _, exists := registry[agent.ID]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateAgent, agent.ID)
		}
		registry[agent.ID] = cloneAgentSpec(agent)
	}
	return &Coordinator{
		runtime: runtime, worker: manager, workerID: workerID,
		agents: registry, running: make(map[string]int),
		active: make(map[string]context.CancelFunc), resolver: options.ContextResolver,
		publisher: options.OutputPublisher,
	}, nil
}

func (c *Coordinator) ApplyPatch(tasks []domain.TaskSpec, now time.Time) error {
	normalized := make([]domain.TaskSpec, 0, len(tasks))
	for _, task := range tasks {
		agent, exists := c.agents[task.AgentID]
		if !exists {
			return fmt.Errorf("%w: %q", ErrUnknownAgent, task.AgentID)
		}
		if err := task.Validate(); err != nil {
			return err
		}
		copy := task.Clone()
		if copy.Resources.CPUMilli == 0 {
			copy.Resources.CPUMilli = agent.Resources.CPUMilli
		}
		if copy.Resources.MemoryMiB == 0 {
			copy.Resources.MemoryMiB = agent.Resources.MemoryMiB
		}
		if !copy.Resources.FitsWithin(agent.Resources) {
			return fmt.Errorf("%w: task %q agent %q", ErrAgentResourcePolicy, task.ID, agent.ID)
		}
		normalized = append(normalized, copy)
	}
	return c.runtime.ApplyPatch(normalized, now)
}

func (c *Coordinator) Task(taskID string) (taskgraph.TaskSnapshot, bool) {
	return c.runtime.Task(taskID)
}

func (c *Coordinator) Tick(ctx context.Context, now time.Time) (TickResult, error) {
	if health, ok := c.resolver.(healthyContextResolver); ok && !health.Healthy() {
		return TickResult{}, errors.Join(runtimecore.ErrRuntimeUnavailable, errors.New("Context repository is unavailable"))
	}
	c.mu.Lock()
	if c.publicationErr != nil {
		err := c.publicationErr
		c.mu.Unlock()
		return TickResult{}, err
	}
	if len(c.runtime.PendingCompletions()) != 0 {
		c.mu.Unlock()
		return TickResult{}, errors.Join(runtimecore.ErrRuntimeUnavailable, runtimecore.ErrCompletionPending)
	}
	decision, err := c.runtime.ScheduleEligible(now, func(task domain.TaskSpec) bool {
		agent, exists := c.agents[task.AgentID]
		return exists && c.running[agent.ID] < agent.MaxConcurrency
	})
	if err != nil {
		c.mu.Unlock()
		return TickResult{}, err
	}
	taskID := decision.Candidate.TaskID
	snapshot, exists := c.runtime.Task(taskID)
	if !exists {
		c.mu.Unlock()
		return TickResult{Decision: decision}, fmt.Errorf("scheduled task %q disappeared", taskID)
	}
	agent, exists := c.agents[snapshot.Spec.AgentID]
	if !exists {
		_, failErr := c.runtime.Fail(taskID, snapshot.Spec.Resources)
		c.mu.Unlock()
		return TickResult{Decision: decision}, errors.Join(fmt.Errorf("%w: %q", ErrUnknownAgent, snapshot.Spec.AgentID), failErr)
	}
	attemptNumber := c.runtime.AttemptNumber(taskID)
	if attemptNumber <= 0 {
		_, failErr := c.runtime.Fail(taskID, snapshot.Spec.Resources)
		c.mu.Unlock()
		return TickResult{Decision: decision}, errors.Join(errors.New("scheduled task has no attempt number"), failErr)
	}
	c.running[agent.ID]++
	runCtx, cancel := context.WithTimeout(ctx, attemptTimeout(snapshot.Spec.Timeout, agent.ExecutionTimeout))
	c.active[taskID] = cancel
	c.mu.Unlock()
	defer cancel()
	result := TickResult{Decision: decision}
	resolved, resolveErr := c.resolveContext(runCtx, snapshot.Spec, agent)
	if resolveErr != nil {
		if health, ok := c.resolver.(healthyContextResolver); ok && !health.Healthy() {
			requeueErr := c.finalizeContextUnavailable(taskID, agent.ID)
			return result, errors.Join(runtimecore.ErrRuntimeUnavailable, resolveErr, requeueErr)
		}
		deadlineExceeded := errors.Is(resolveErr, context.DeadlineExceeded) ||
			errors.Is(runCtx.Err(), context.DeadlineExceeded)
		canceled, timedOut, interrupted, terminalErr := c.finalizeFailure(runCtx, taskID, agent.ID, snapshot.Spec.Resources,
			!deadlineExceeded && (errors.Is(resolveErr, context.Canceled) || errors.Is(runCtx.Err(), context.Canceled)),
			deadlineExceeded)
		if canceled && !errors.Is(resolveErr, context.Canceled) {
			resolveErr = errors.Join(resolveErr, context.Canceled)
		}
		if timedOut && !errors.Is(resolveErr, context.DeadlineExceeded) {
			resolveErr = errors.Join(resolveErr, context.DeadlineExceeded)
		}
		if interrupted {
			resolveErr = errors.Join(resolveErr, ErrAttemptInterrupted)
		}
		return result, errors.Join(resolveErr, terminalErr)
	}
	contextRef := ""
	contextText := ""
	if c.resolver != nil {
		contextRef = resolved.ManifestID
		contextText = resolved.Text
		result.Context = &ContextResult{
			ManifestID: resolved.ManifestID,
			RefCount:   len(resolved.Refs),
			Metrics:    resolved.Metrics,
		}
	}
	execution, runErr := c.worker.Run(runCtx, worker.Request{
		AttemptID:     AttemptID(taskID, attemptNumber),
		AttemptNumber: attemptNumber,
		WorkerID:      c.workerID,
		Agent:         agent,
		Task:          snapshot.Spec,
		ContextRef:    contextRef,
		ContextText:   contextText,
		OnStarted: func(_ context.Context, _ executor.Request, _ int) error {
			return c.runtime.MarkStarted(taskID, attemptNumber, time.Now().UTC())
		},
	})
	result.Execution = execution
	if errors.Is(runErr, executor.ErrCleanup) {
		c.abandonLocalExecution(taskID, agent.ID)
		markExecutionCleanupUncertain(&result)
		return result, errors.Join(runtimecore.ErrRuntimeUnavailable, runErr)
	}
	if runErr != nil {
		return result, c.finishExecutionError(runCtx, taskID, agent.ID, snapshot.Spec.Resources, &result, runErr)
	}
	return c.finishOutput(runCtx, snapshot.Spec, attemptNumber, result)
}

func (c *Coordinator) finalizeContextUnavailable(taskID, agentID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.finishActiveLocked(taskID, agentID)
	if c.runtime.CancelRequested(taskID) {
		_, err := c.runtime.Cancel(taskID)
		return err
	}
	_, err := c.runtime.RequeueUnstarted(taskID, time.Now().UTC())
	return err
}

func (c *Coordinator) resolveContext(ctx context.Context, task domain.TaskSpec, agent domain.AgentSpec) (contextdispatch.Resolution, error) {
	if c.resolver == nil {
		return contextdispatch.Resolution{}, nil
	}
	budget, err := contextTokenBudget(task.Resources.TokenBudget)
	if err != nil {
		return contextdispatch.Resolution{}, err
	}
	resolved, err := c.resolver.Resolve(ctx, task, agent, budget)
	if err != nil {
		return contextdispatch.Resolution{}, err
	}
	return resolved, nil
}

func (c *Coordinator) finalizeFailure(
	ctx context.Context,
	taskID, agentID string,
	resources domain.ResourceVector,
	canceled, timedOut bool,
) (bool, bool, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.finishActiveLocked(taskID, agentID)
	if c.runtime.CancelRequested(taskID) {
		_, err := c.runtime.Cancel(taskID)
		return true, false, false, err
	}
	timedOut = timedOut || errors.Is(ctx.Err(), context.DeadlineExceeded)
	canceled = !timedOut && (canceled || errors.Is(ctx.Err(), context.Canceled))
	if canceled {
		_, err := c.runtime.Interrupt(taskID, time.Now().UTC())
		return false, false, true, err
	}
	// Daemon fairness is charged by declared resources per confirmed start.
	// Failed preparation and process start release the admission at zero cost.
	if task, exists := c.runtime.Task(taskID); exists && task.Status == domain.TaskStatusAdmitted {
		resources = domain.ResourceVector{}
	}
	_, err := c.runtime.Fail(taskID, resources)
	return false, timedOut, false, err
}

func executionFailureSignals(runErr, contextErr error) (timedOut, canceled bool) {
	if runErr == nil {
		return false, false
	}
	timedOut = errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, executor.ErrTimeout) ||
		errors.Is(contextErr, context.DeadlineExceeded)
	canceled = !timedOut && (errors.Is(runErr, executor.ErrCanceled) ||
		errors.Is(runErr, context.Canceled) || errors.Is(contextErr, context.Canceled))
	return timedOut, canceled
}

func (c *Coordinator) finishActiveLocked(taskID, agentID string) {
	delete(c.active, taskID)
	if c.running[agentID] <= 1 {
		delete(c.running, agentID)
		return
	}
	c.running[agentID]--
}

func (c *Coordinator) abandonLocalExecution(taskID, agentID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finishActiveLocked(taskID, agentID)
}

func markExecutionCanceled(result *TickResult) {
	if result.Execution.Attempt.ID == "" {
		return
	}
	result.Execution.Attempt.Status = domain.AttemptStatusCanceled
	result.Execution.Attempt.Failure = ""
	result.Execution.Process.Canceled = true
}

func markExecutionTimedOut(result *TickResult) {
	if result.Execution.Attempt.ID == "" {
		return
	}
	result.Execution.Attempt.Status = domain.AttemptStatusFailed
	result.Execution.Attempt.Failure = context.DeadlineExceeded.Error()
	result.Execution.Process.TimedOut = true
	result.Execution.Process.Canceled = false
}

func markExecutionInterrupted(result *TickResult) {
	if result.Execution.Attempt.ID == "" {
		return
	}
	result.Execution.Attempt.Status = domain.AttemptStatusLost
	result.Execution.Attempt.Failure = ErrAttemptInterrupted.Error()
	result.Execution.Process.Canceled = true
	result.Execution.Process.TimedOut = false
}

func markExecutionCleanupUncertain(result *TickResult) {
	if result.Execution.Attempt.ID == "" {
		return
	}
	result.Execution.Attempt.Status = domain.AttemptStatusLost
	result.Execution.Attempt.Failure = executor.ErrCleanup.Error()
	result.Execution.Process.Canceled = false
	result.Execution.Process.TimedOut = false
}

func AttemptID(taskID string, attemptNumber int) string {
	suffix := "-attempt-" + strconv.Itoa(attemptNumber)
	if len(taskID)+len(suffix) <= 128 {
		return taskID + suffix
	}
	digest := sha256.Sum256([]byte(taskID))
	return "task-" + hex.EncodeToString(digest[:]) + suffix
}

func contextTokenBudget(value int64) (int, error) {
	if strconv.IntSize == 32 && value > 1<<31-1 {
		return 0, fmt.Errorf("%w: %d", ErrContextBudget, value)
	}
	return int(value), nil
}

func attemptTimeout(taskTimeout, agentTimeout time.Duration) time.Duration {
	if agentTimeout < taskTimeout {
		return agentTimeout
	}
	return taskTimeout
}

func cloneAgentSpec(agent domain.AgentSpec) domain.AgentSpec {
	agent.Args = append([]string(nil), agent.Args...)
	agent.Capabilities = append([]string(nil), agent.Capabilities...)
	agent.Resources = agent.Resources.Clone()
	return agent
}
