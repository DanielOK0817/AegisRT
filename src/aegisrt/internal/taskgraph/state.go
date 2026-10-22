package taskgraph

import (
	"errors"
	"fmt"
	"sort"

	"aegisrt/internal/domain"
)

var ErrInvalidState = errors.New("invalid task graph state")

// State is the durable representation of a TaskGraph. Tasks are emitted in
// task-id order so equivalent graph states have a deterministic encoding.
type State struct {
	Version uint64         `json:"version"`
	Tasks   []TaskSnapshot `json:"tasks"`
}

// State returns a deep copy of the graph's durable state.
func (g *TaskGraph) State() State {
	g.mu.RLock()
	defer g.mu.RUnlock()

	result := State{Version: g.version, Tasks: make([]TaskSnapshot, 0, len(g.nodes))}
	for _, item := range g.nodes {
		result.Tasks = append(result.Tasks, snapshot(item))
	}
	sort.Slice(result.Tasks, func(i, j int) bool {
		return result.Tasks[i].Spec.ID < result.Tasks[j].Spec.ID
	})
	return result
}

// Restore validates and reconstructs a TaskGraph without exposing any mutable
// fields owned by the caller. Invalid states never produce a partial graph.
func Restore(state State) (*TaskGraph, error) {
	if len(state.Tasks) > MaxGraphTasks {
		return nil, fmt.Errorf("%w: %w: a graph may contain at most %d tasks", ErrInvalidState, ErrGraphLimit, MaxGraphTasks)
	}
	if len(state.Tasks) == 0 {
		if state.Version != 0 {
			return nil, fmt.Errorf("%w: empty graph must have version zero", ErrInvalidState)
		}
		return New()
	}
	if state.Version == 0 {
		return nil, fmt.Errorf("%w: non-empty graph must have a positive version", ErrInvalidState)
	}

	nodes := make(map[string]*node, len(state.Tasks))
	specs := make(map[string]domain.TaskSpec, len(state.Tasks))
	for index, item := range state.Tasks {
		if len(item.Spec.Dependencies) > MaxTaskDependencies {
			return nil, fmt.Errorf("%w: %w: task %q has too many dependencies", ErrInvalidState, ErrGraphLimit, item.Spec.ID)
		}
		if err := item.Spec.Validate(); err != nil {
			return nil, fmt.Errorf("%w: tasks[%d] spec: %v", ErrInvalidState, index, err)
		}
		if err := item.Status.Validate(); err != nil {
			return nil, fmt.Errorf("%w: task %q: %v", ErrInvalidState, item.Spec.ID, err)
		}
		if item.Status == domain.TaskStatusSubmitted {
			return nil, fmt.Errorf("%w: task %q has unreachable status %s", ErrInvalidState, item.Spec.ID, item.Status)
		}
		if _, exists := nodes[item.Spec.ID]; exists {
			return nil, fmt.Errorf("%w: %w: %q", ErrInvalidState, ErrDuplicateTask, item.Spec.ID)
		}
		cloned := item.Spec.Clone()
		nodes[cloned.ID] = &node{spec: cloned, status: item.Status}
		specs[cloned.ID] = cloned
	}
	if err := validateGraphEdges(specs); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidState, err)
	}

	dependents := make(map[string]map[string]struct{}, len(nodes))
	for id := range nodes {
		dependents[id] = make(map[string]struct{})
	}
	for id, item := range nodes {
		for _, dependencyID := range item.spec.Dependencies {
			dependency, exists := nodes[dependencyID]
			if !exists {
				return nil, fmt.Errorf("%w: %w: task %q requires %q", ErrInvalidState, ErrMissingDependency, id, dependencyID)
			}
			if dependency.spec.WorkflowID != item.spec.WorkflowID {
				return nil, fmt.Errorf("%w: %w: task %q requires %q", ErrInvalidState, ErrCrossWorkflowDependency, id, dependencyID)
			}
			dependents[dependencyID][id] = struct{}{}
		}
	}
	if cycle := findCycleMembers(specs); len(cycle) != 0 {
		return nil, fmt.Errorf("%w: %w: %v", ErrInvalidState, ErrCycleDetected, cycle)
	}

	for id, item := range nodes {
		complete := restoredDependenciesSatisfied(item.spec, nodes)
		switch item.status {
		case domain.TaskStatusWaitingDependency:
			if complete {
				return nil, fmt.Errorf("%w: waiting task %q has no incomplete dependency", ErrInvalidState, id)
			}
		case domain.TaskStatusReady, domain.TaskStatusAdmitted, domain.TaskStatusRunning,
			domain.TaskStatusSucceeded, domain.TaskStatusLost, domain.TaskStatusRecovering:
			if !complete {
				return nil, fmt.Errorf("%w: task %q in %s has incomplete dependencies", ErrInvalidState, id, item.status)
			}
		}
	}

	return &TaskGraph{
		nodes:      nodes,
		dependents: dependents,
		version:    state.Version,
	}, nil
}

func restoredDependenciesSatisfied(task domain.TaskSpec, nodes map[string]*node) bool {
	for _, dependencyID := range task.Dependencies {
		if nodes[dependencyID].status != domain.TaskStatusSucceeded {
			return false
		}
	}
	return true
}
