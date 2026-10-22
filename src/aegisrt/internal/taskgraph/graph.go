package taskgraph

import (
	"fmt"
	"sort"
	"sync"

	"aegisrt/internal/domain"
)

type node struct {
	spec   domain.TaskSpec
	status domain.TaskStatus
}

// TaskGraph owns task lifecycle state while keeping submitted TaskSpecs
// immutable. Every public method is safe for concurrent use.
type TaskGraph struct {
	mu         sync.RWMutex
	nodes      map[string]*node
	dependents map[string]map[string]struct{}
	version    uint64
}

type Patch struct {
	Tasks []domain.TaskSpec `json:"tasks"`
}

type TaskSnapshot struct {
	Spec   domain.TaskSpec   `json:"spec"`
	Status domain.TaskStatus `json:"status"`
}

// New constructs a graph atomically. Tasks may refer to dependencies that
// appear later in the input slice.
func New(tasks ...domain.TaskSpec) (*TaskGraph, error) {
	graph := &TaskGraph{
		nodes:      make(map[string]*node),
		dependents: make(map[string]map[string]struct{}),
	}
	if len(tasks) == 0 {
		return graph, nil
	}
	if err := graph.applyTasks(tasks); err != nil {
		return nil, err
	}
	return graph, nil
}

func (g *TaskGraph) AddTask(task domain.TaskSpec) error {
	return g.applyTasks([]domain.TaskSpec{task})
}

func (g *TaskGraph) ApplyPatch(patch Patch) error {
	if len(patch.Tasks) == 0 {
		return ErrEmptyPatch
	}
	if len(patch.Tasks) > MaxPatchTasks {
		return fmt.Errorf("%w: a patch may contain at most %d tasks", ErrGraphLimit, MaxPatchTasks)
	}
	return g.applyTasks(patch.Tasks)
}

// Patch is a convenience for callers that already hold a task slice.
func (g *TaskGraph) Patch(tasks []domain.TaskSpec) error {
	return g.ApplyPatch(Patch{Tasks: tasks})
}

func (g *TaskGraph) Len() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

func (g *TaskGraph) Version() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.version
}

func (g *TaskGraph) Task(taskID string) (TaskSnapshot, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	item, exists := g.nodes[taskID]
	if !exists {
		return TaskSnapshot{}, false
	}
	return snapshot(item), true
}

func (g *TaskGraph) Status(taskID string) (domain.TaskStatus, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	item, exists := g.nodes[taskID]
	if !exists {
		return "", fmt.Errorf("%w: %q", ErrTaskNotFound, taskID)
	}
	return item.status, nil
}

func (g *TaskGraph) Snapshot() []TaskSnapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]TaskSnapshot, 0, len(g.nodes))
	for _, item := range g.nodes {
		result = append(result, snapshot(item))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Spec.ID < result[j].Spec.ID })
	return result
}

func (g *TaskGraph) ReadyTasks() []domain.TaskSpec {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]domain.TaskSpec, 0)
	for _, item := range g.nodes {
		if item.status == domain.TaskStatusReady {
			result = append(result, item.spec.Clone())
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (g *TaskGraph) DependenciesSatisfied(taskID string) (bool, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	item, exists := g.nodes[taskID]
	if !exists {
		return false, fmt.Errorf("%w: %q", ErrTaskNotFound, taskID)
	}
	return g.dependenciesSatisfiedLocked(item.spec), nil
}

func (g *TaskGraph) Dependents(taskID string) ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if _, exists := g.nodes[taskID]; !exists {
		return nil, fmt.Errorf("%w: %q", ErrTaskNotFound, taskID)
	}
	result := make([]string, 0, len(g.dependents[taskID]))
	for dependentID := range g.dependents[taskID] {
		result = append(result, dependentID)
	}
	sort.Strings(result)
	return result, nil
}

func snapshot(item *node) TaskSnapshot {
	return TaskSnapshot{Spec: item.spec.Clone(), Status: item.status}
}
