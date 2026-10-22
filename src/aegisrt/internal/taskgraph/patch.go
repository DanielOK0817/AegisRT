package taskgraph

import (
	"fmt"
	"sort"

	"aegisrt/internal/domain"
)

func (g *TaskGraph) applyTasks(tasks []domain.TaskSpec) error {
	if len(tasks) == 0 {
		return ErrEmptyPatch
	}
	if len(tasks) > MaxGraphTasks {
		return fmt.Errorf("%w: a graph may contain at most %d tasks", ErrGraphLimit, MaxGraphTasks)
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	pending := make(map[string]domain.TaskSpec, len(tasks))
	existing := 0
	for index, task := range tasks {
		if len(task.Dependencies) > MaxTaskDependencies {
			return fmt.Errorf("%w: task %q may have at most %d dependencies", ErrGraphLimit, task.ID, MaxTaskDependencies)
		}
		if err := task.Validate(); err != nil {
			return fmt.Errorf("%w at tasks[%d]: %v", ErrInvalidTaskSpec, index, err)
		}
		if _, exists := pending[task.ID]; exists {
			return fmt.Errorf("%w: %q appears more than once in patch", ErrDuplicateTask, task.ID)
		}
		if item, exists := g.nodes[task.ID]; exists {
			if !sameTaskSpec(item.spec, task) {
				return fmt.Errorf("%w: %q already exists with different content", ErrDuplicateTask, task.ID)
			}
			existing++
		}
		pending[task.ID] = task.Clone()
	}
	if existing == len(tasks) {
		return nil
	}
	if existing != 0 {
		return fmt.Errorf("%w: patch mixes existing and new task ids", ErrDuplicateTask)
	}
	if len(g.nodes)+len(pending) > MaxGraphTasks {
		return fmt.Errorf("%w: a graph may contain at most %d tasks", ErrGraphLimit, MaxGraphTasks)
	}

	all := make(map[string]domain.TaskSpec, len(g.nodes)+len(pending))
	for id, item := range g.nodes {
		all[id] = item.spec
	}
	for id, task := range pending {
		all[id] = task
	}
	if err := validateGraphEdges(all); err != nil {
		return err
	}
	blocked := g.canceledDescendantsLocked()
	for _, task := range pending {
		for _, dependencyID := range task.Dependencies {
			dependency, exists := all[dependencyID]
			if !exists {
				return fmt.Errorf("%w: task %q requires %q", ErrMissingDependency, task.ID, dependencyID)
			}
			if dependency.WorkflowID != task.WorkflowID {
				return fmt.Errorf("%w: task %q in %q requires %q in %q",
					ErrCrossWorkflowDependency, task.ID, task.WorkflowID, dependencyID, dependency.WorkflowID)
			}
			if blocked[dependencyID] {
				return fmt.Errorf("%w: task %q requires %q", ErrCanceledDependency, task.ID, dependencyID)
			}
		}
	}
	if cycle := findCycleMembers(all); len(cycle) > 0 {
		return fmt.Errorf("%w: %v", ErrCycleDetected, cycle)
	}

	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		task := pending[id]
		g.nodes[id] = &node{spec: task, status: domain.TaskStatusWaitingDependency}
		for _, dependencyID := range task.Dependencies {
			if g.dependents[dependencyID] == nil {
				g.dependents[dependencyID] = make(map[string]struct{})
			}
			g.dependents[dependencyID][id] = struct{}{}
		}
		if g.dependents[id] == nil {
			g.dependents[id] = make(map[string]struct{})
		}
	}
	for _, id := range ids {
		item := g.nodes[id]
		if g.dependenciesSatisfiedLocked(item.spec) {
			item.status = domain.TaskStatusReady
		}
	}
	g.version++
	return nil
}

func findCycleMembers(tasks map[string]domain.TaskSpec) []string {
	indegree := make(map[string]int, len(tasks))
	dependents := make(map[string][]string, len(tasks))
	for id, task := range tasks {
		indegree[id] = len(task.Dependencies)
		for _, dependencyID := range task.Dependencies {
			dependents[dependencyID] = append(dependents[dependencyID], id)
		}
	}
	queue := make([]string, 0, len(tasks))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		visited++
		for _, dependentID := range dependents[id] {
			indegree[dependentID]--
			if indegree[dependentID] == 0 {
				queue = append(queue, dependentID)
			}
		}
	}
	if visited == len(tasks) {
		return nil
	}
	cycle := make([]string, 0, len(tasks)-visited)
	for id, degree := range indegree {
		if degree > 0 {
			cycle = append(cycle, id)
		}
	}
	sort.Strings(cycle)
	return cycle
}

func (g *TaskGraph) dependenciesSatisfiedLocked(task domain.TaskSpec) bool {
	for _, dependencyID := range task.Dependencies {
		dependency, exists := g.nodes[dependencyID]
		if !exists || dependency.status != domain.TaskStatusSucceeded {
			return false
		}
	}
	return true
}
