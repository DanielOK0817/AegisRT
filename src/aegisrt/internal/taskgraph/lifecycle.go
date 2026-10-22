package taskgraph

import (
	"fmt"
	"sort"

	"aegisrt/internal/domain"
)

// Transition applies a normal lifecycle transition. When a task succeeds,
// direct dependents whose prerequisites are all successful become READY and
// are returned to the caller.
func (g *TaskGraph) Transition(taskID string, next domain.TaskStatus) ([]domain.TaskSpec, error) {
	if err := next.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTransition, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	item, exists := g.nodes[taskID]
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrTaskNotFound, taskID)
	}
	if !item.status.CanTransitionTo(next) {
		return nil, fmt.Errorf("%w: task %q cannot move from %s to %s",
			ErrInvalidTransition, taskID, item.status, next)
	}
	if next == domain.TaskStatusReady && !g.dependenciesSatisfiedLocked(item.spec) {
		return nil, fmt.Errorf("%w: %q", ErrDependenciesIncomplete, taskID)
	}
	item.status = next
	newlyReady := g.unlockDependentsLocked(taskID, next)
	g.version++
	return newlyReady, nil
}

// Complete is an idempotent completion-event handler. It also accepts READY or
// ADMITTED tasks so deterministic simulators can complete a task without
// manufacturing worker lifecycle events.
func (g *TaskGraph) Complete(taskID string) ([]domain.TaskSpec, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	item, exists := g.nodes[taskID]
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrTaskNotFound, taskID)
	}
	if item.status == domain.TaskStatusSucceeded {
		return nil, nil
	}
	if item.status != domain.TaskStatusReady && item.status != domain.TaskStatusAdmitted &&
		item.status != domain.TaskStatusRunning {
		if item.status == domain.TaskStatusWaitingDependency {
			return nil, fmt.Errorf("%w: %q", ErrDependenciesIncomplete, taskID)
		}
		return nil, fmt.Errorf("%w: task %q cannot complete from %s",
			ErrInvalidTransition, taskID, item.status)
	}
	if !g.dependenciesSatisfiedLocked(item.spec) {
		return nil, fmt.Errorf("%w: %q", ErrDependenciesIncomplete, taskID)
	}
	item.status = domain.TaskStatusSucceeded
	newlyReady := g.unlockDependentsLocked(taskID, domain.TaskStatusSucceeded)
	g.version++
	return newlyReady, nil
}

func (g *TaskGraph) unlockDependentsLocked(taskID string, status domain.TaskStatus) []domain.TaskSpec {
	if status != domain.TaskStatusSucceeded {
		return nil
	}
	ready := make([]domain.TaskSpec, 0)
	for dependentID := range g.dependents[taskID] {
		dependent := g.nodes[dependentID]
		if dependent.status != domain.TaskStatusWaitingDependency {
			continue
		}
		if g.dependenciesSatisfiedLocked(dependent.spec) {
			dependent.status = domain.TaskStatusReady
			ready = append(ready, dependent.spec.Clone())
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })
	return ready
}
