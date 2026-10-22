package taskgraph

import (
	"fmt"
	"reflect"
	"sort"

	"aegisrt/internal/domain"
)

// These admission limits bound graph validation and scheduling work. They are
// conservative safety limits, not measured throughput or scalability claims.
const (
	MaxPatchTasks       = 256
	MaxGraphTasks       = 4096
	MaxTaskDependencies = 256
	MaxGraphEdges       = 65536
)

func validateGraphEdges(tasks map[string]domain.TaskSpec) error {
	edges := 0
	for _, task := range tasks {
		if len(task.Dependencies) > MaxTaskDependencies {
			return fmt.Errorf("%w: task %q may have at most %d dependencies", ErrGraphLimit, task.ID, MaxTaskDependencies)
		}
		edges += len(task.Dependencies)
		if edges > MaxGraphEdges {
			return fmt.Errorf("%w: a graph may contain at most %d dependency edges", ErrGraphLimit, MaxGraphEdges)
		}
	}
	return nil
}

func sameTaskSpec(left, right domain.TaskSpec) bool {
	canonical := func(task domain.TaskSpec) domain.TaskSpec {
		result := task.Clone()
		sort.Strings(result.Dependencies)
		if len(result.Resources.ToolSlots) == 0 {
			result.Resources.ToolSlots = nil
		}
		return result
	}
	return reflect.DeepEqual(canonical(left), canonical(right))
}

// A canceled task can never succeed. Its existing descendants cannot satisfy
// their dependencies either, even when their own status is still WAITING.
// Failed tasks are deliberately excluded because recovery can retry them.
func (g *TaskGraph) canceledDescendantsLocked() map[string]bool {
	blocked := make(map[string]bool)
	queue := make([]string, 0)
	for id, item := range g.nodes {
		if item.status == domain.TaskStatusCanceled {
			blocked[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for child := range g.dependents[id] {
			if !blocked[child] {
				blocked[child] = true
				queue = append(queue, child)
			}
		}
	}
	return blocked
}
