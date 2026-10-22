// Package runtimeapp composes internal Runtime components without exposing
// process execution primitives through the HTTP package.
package runtimeapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/taskgraph"
)

var (
	ErrInvalidControl = errors.New("invalid runtime application control")
	ErrUnknownAgent   = errors.New("unknown runtime application agent")
	ErrDuplicateAgent = errors.New("duplicate runtime application agent")
)

type Control interface {
	ApplyPatch([]domain.TaskSpec, time.Time) error
	Cancel(string) (taskgraph.TaskSnapshot, error)
	Task(string) (taskgraph.TaskSnapshot, bool)
	Tasks() []taskgraph.TaskSnapshot
	Stats() runtimecore.Stats
}

type ManifestRepository interface {
	EnsureEmptyWithOwnership(string, string) (contextvm.Manifest, bool, error)
	DeleteIfID(string, string, string) (bool, error)
}

type ContextRepository interface {
	ManifestRepository
	Inspect(string, string) (contextstate.Snapshot, error)
	Append(context.Context, contextstate.AppendCommand, time.Time) (contextstate.AppendResult, error)
	Fanout(context.Context, []contextstate.AppendCommand, time.Time) ([]contextstate.AppendResult, error)
	PublishOutput(context.Context, contextstate.OutputCommand, time.Time) (contextstate.OutputResult, error)
	Stats() contextstate.Stats
}

type outputRepository interface {
	Output(string) (contextstate.OutputRecord, error)
}

// OutputSummary is the content-free durable publication view exposed to
// supervisors. It deliberately omits artifact names, hashes, recipients, and
// Context contents.
type OutputSummary struct {
	RecordCount   int `json:"record_count"`
	ArtifactCount int `json:"artifact_count"`
}

// ContextControl ensures the resolver can always find a principal-bound
// manifest before newly submitted work becomes visible to the scheduler.
type ContextControl struct {
	mu        sync.Mutex
	control   Control
	manifests ManifestRepository
	contexts  ContextRepository
	outputs   outputRepository
	agents    map[string]struct{}
}

func NewContextControl(control Control, manifests ManifestRepository, agents []domain.AgentSpec) (*ContextControl, error) {
	if control == nil || manifests == nil {
		return nil, ErrInvalidControl
	}
	known := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		if err := agent.Validate(); err != nil {
			return nil, fmt.Errorf("%w: agent %q: %v", ErrInvalidControl, agent.ID, err)
		}
		if _, exists := known[agent.ID]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateAgent, agent.ID)
		}
		known[agent.ID] = struct{}{}
	}
	if len(known) == 0 {
		return nil, fmt.Errorf("%w: at least one agent is required", ErrInvalidControl)
	}
	contexts, _ := manifests.(ContextRepository)
	outputs, _ := manifests.(outputRepository)
	return &ContextControl{
		control: control, manifests: manifests, contexts: contexts, outputs: outputs, agents: known,
	}, nil
}

// InspectOutputSummary reports only whether one durable output record exists
// and how many artifacts it contains. Replayed callbacks do not create extra
// durable records and therefore are intentionally not represented here.
func (c *ContextControl) InspectOutputSummary(taskID string) (OutputSummary, error) {
	if c == nil || c.outputs == nil {
		return OutputSummary{}, contextstate.ErrUnavailable
	}
	task, exists := c.control.Task(taskID)
	if !exists || task.Status != domain.TaskStatusSucceeded {
		return OutputSummary{}, contextstate.ErrOutputNotFound
	}
	record, err := c.outputs.Output(taskID)
	if err != nil {
		return OutputSummary{}, err
	}
	return OutputSummary{RecordCount: 1, ArtifactCount: len(record.Artifacts)}, nil
}

func (c *ContextControl) ApplyPatch(tasks []domain.TaskSpec, now time.Time) error {
	for _, task := range tasks {
		if err := task.Validate(); err != nil {
			return err
		}
		if _, exists := c.agents[task.AgentID]; !exists {
			return fmt.Errorf("%w: %q", ErrUnknownAgent, task.AgentID)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	allExisting := len(tasks) != 0
	for _, task := range tasks {
		if _, exists := c.control.Task(task.ID); !exists {
			allExisting = false
			break
		}
	}
	if allExisting {
		// The graph validates exact replay or conflicting specs. Already
		// admitted tasks must not be revalidated against later Context growth.
		return c.control.ApplyPatch(tasks, now)
	}
	created := make([]provisionedManifest, 0, len(tasks))
	seen := make(map[string]struct{}, len(tasks))
	snapshots := make(map[string]contextstate.Snapshot, len(tasks))
	for _, task := range tasks {
		key := task.WorkflowID + "\x00" + task.AgentID
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			manifest, owned, err := c.manifests.EnsureEmptyWithOwnership(task.WorkflowID, task.AgentID)
			if err != nil {
				return errors.Join(err, c.rollback(created))
			}
			if owned {
				created = append(created, provisionedManifest{
					workflowID: task.WorkflowID, agentID: task.AgentID, manifestID: manifest.ID,
				})
			}
			if c.contexts != nil {
				snapshot, inspectErr := c.contexts.Inspect(task.WorkflowID, task.AgentID)
				if inspectErr != nil {
					return errors.Join(inspectErr, c.rollback(created))
				}
				snapshots[key] = snapshot
			}
		}
		if snapshot, exists := snapshots[key]; exists && int64(snapshot.PinnedTokens) > task.Resources.TokenBudget {
			budgetErr := fmt.Errorf("%w: task %q budget=%d pinned=%d",
				contextstate.ErrPinnedTokenLimit, task.ID, task.Resources.TokenBudget, snapshot.PinnedTokens)
			return errors.Join(budgetErr, c.rollback(created))
		}
	}
	if err := c.control.ApplyPatch(tasks, now); err != nil {
		if errors.Is(err, runtimecore.ErrCheckpointUnavailable) || errors.Is(err, runtimecore.ErrRuntimeUnavailable) {
			return err
		}
		return errors.Join(err, c.rollback(created))
	}
	return nil
}

type provisionedManifest struct {
	workflowID string
	agentID    string
	manifestID string
}

func (c *ContextControl) rollback(created []provisionedManifest) error {
	var result error
	for index := len(created) - 1; index >= 0; index-- {
		item := created[index]
		if _, err := c.manifests.DeleteIfID(item.workflowID, item.agentID, item.manifestID); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

type ContextSnapshot struct {
	TaskID   string                `json:"task_id"`
	Manifest contextstate.Snapshot `json:"manifest"`
}

type AppendContextCommand struct {
	TaskID             string
	ExpectedManifestID string
	IdempotencyKey     string
	Content            string
	Visibility         contextstate.Visibility
	Pin                bool
	Cold               bool
}

type AppendContextResult = contextstate.AppendResult

func (c *ContextControl) InspectContext(taskID string) (ContextSnapshot, error) {
	if c.contexts == nil {
		return ContextSnapshot{}, contextstate.ErrUnavailable
	}
	task, exists := c.control.Task(taskID)
	if !exists {
		return ContextSnapshot{}, fmt.Errorf("%w: %q", taskgraph.ErrTaskNotFound, taskID)
	}
	snapshot, err := c.contexts.Inspect(task.Spec.WorkflowID, task.Spec.AgentID)
	if err != nil {
		return ContextSnapshot{}, err
	}
	return ContextSnapshot{TaskID: taskID, Manifest: snapshot}, nil
}

func (c *ContextControl) AppendContext(ctx context.Context, command AppendContextCommand) (AppendContextResult, error) {
	if c.contexts == nil {
		return AppendContextResult{}, contextstate.ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	task, exists := c.control.Task(command.TaskID)
	if !exists {
		return AppendContextResult{}, fmt.Errorf("%w: %q", taskgraph.ErrTaskNotFound, command.TaskID)
	}
	pinnedBudget := task.Spec.Resources.TokenBudget
	for _, candidate := range c.control.Tasks() {
		if candidate.Status.IsTerminal() || candidate.Spec.WorkflowID != task.Spec.WorkflowID ||
			candidate.Spec.AgentID != task.Spec.AgentID {
			continue
		}
		if candidate.Spec.Resources.TokenBudget < pinnedBudget {
			pinnedBudget = candidate.Spec.Resources.TokenBudget
		}
	}
	return c.contexts.Append(ctx, contextstate.AppendCommand{
		TaskID: command.TaskID, WorkflowID: task.Spec.WorkflowID, AgentID: task.Spec.AgentID,
		ExpectedManifestID: command.ExpectedManifestID, IdempotencyKey: command.IdempotencyKey,
		Content: command.Content, Visibility: command.Visibility, Pin: command.Pin, Cold: command.Cold,
		TaskTokenBudget: task.Spec.Resources.TokenBudget, PrincipalPinnedBudget: pinnedBudget,
	}, time.Now().UTC())
}

func (c *ContextControl) Cancel(taskID string) (taskgraph.TaskSnapshot, error) {
	return c.control.Cancel(taskID)
}

func (c *ContextControl) Tasks() []taskgraph.TaskSnapshot {
	return c.control.Tasks()
}

func (c *ContextControl) Stats() runtimecore.Stats {
	return c.control.Stats()
}

func (c *ContextControl) ContextStats() contextstate.Stats {
	if c.contexts == nil {
		return contextstate.Stats{}
	}
	return c.contexts.Stats()
}
