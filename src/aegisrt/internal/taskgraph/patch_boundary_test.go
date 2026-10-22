package taskgraph

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"aegisrt/internal/domain"
)

func TestPatchReplayPreservesLifecycleAndVersion(t *testing.T) {
	tasks := []domain.TaskSpec{taskSpec("root"), taskSpec("child", "root")}
	graph, err := New(tasks...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Complete("root"); err != nil {
		t.Fatal(err)
	}
	before := graph.State()
	if err := graph.Patch(tasks); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if !reflect.DeepEqual(graph.State(), before) {
		t.Fatal("replay changed lifecycle or graph version")
	}
	restored, err := Restore(before)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Patch(tasks); err != nil {
		t.Fatalf("replay after restore: %v", err)
	}
}

func TestPatchConcurrentReplayCommitsOnce(t *testing.T) {
	graph, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errs <- graph.Patch([]domain.TaskSpec{taskSpec("root"), taskSpec("child", "root")})
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent replay: %v", err)
		}
	}
	if graph.Len() != 2 || graph.Version() != 1 {
		t.Fatalf("tasks=%d version=%d, want 2 tasks at version 1", graph.Len(), graph.Version())
	}
}

func TestPatchRejectsCanceledDependencyAncestryAtomically(t *testing.T) {
	for _, dependency := range []string{"root", "waiting"} {
		t.Run(dependency, func(t *testing.T) {
			graph, err := New(taskSpec("root"), taskSpec("waiting", "root"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := graph.Transition("root", domain.TaskStatusCanceled); err != nil {
				t.Fatal(err)
			}
			before := graph.State()
			if err := graph.Patch([]domain.TaskSpec{taskSpec("independent"), taskSpec("child", dependency)}); err == nil {
				t.Fatal("accepted a dependency that can never succeed")
			}
			if !reflect.DeepEqual(graph.State(), before) {
				t.Fatal("rejected dependency mutated graph")
			}
		})
	}
}

func TestPatchRejectsBatchAndGraphCapacityAtomically(t *testing.T) {
	t.Run("batch", func(t *testing.T) {
		graph, _ := New()
		if err := graph.Patch(boundaryTasks(MaxPatchTasks + 1)); !errors.Is(err, ErrGraphLimit) {
			t.Fatal("accepted more than 256 tasks in one patch")
		}
		if graph.Len() != 0 || graph.Version() != 0 {
			t.Fatal("oversized patch mutated graph")
		}
	})
	t.Run("graph", func(t *testing.T) {
		graph, err := New(boundaryTasks(MaxGraphTasks)...)
		if err != nil {
			t.Fatal(err)
		}
		before := graph.State()
		if err := graph.AddTask(taskSpec("overflow")); !errors.Is(err, ErrGraphLimit) {
			t.Fatal("accepted more than 4096 graph tasks")
		}
		if !reflect.DeepEqual(graph.State(), before) {
			t.Fatal("oversized graph mutation was not atomic")
		}
	})
}

func TestPatchRejectsExcessiveDependencies(t *testing.T) {
	tasks := boundaryTasks(MaxTaskDependencies + 1)
	graph, err := New(tasks...)
	if err != nil {
		t.Fatal(err)
	}
	before := graph.State()
	if err := graph.AddTask(taskSpec("join", taskIDs(tasks)...)); !errors.Is(err, ErrGraphLimit) {
		t.Fatal("accepted more than 256 dependencies")
	}
	if !reflect.DeepEqual(graph.State(), before) {
		t.Fatal("dependency limit rejection mutated graph")
	}
}

func TestPatchReplayRejectsConflictsAndMixedNewTasks(t *testing.T) {
	for _, field := range []string{"goal", "workflow", "agent", "dependencies", "priority", "resources", "attempts", "timeout", "key", "mixed", "duplicate"} {
		t.Run(field, func(t *testing.T) {
			original := taskSpec("root")
			graph, err := New(original)
			if err != nil {
				t.Fatal(err)
			}
			before := graph.State()
			changed := original.Clone()
			switch field {
			case "goal":
				changed.Goal = "changed"
			case "workflow":
				changed.WorkflowID = "other"
			case "agent":
				changed.AgentID = "other"
			case "dependencies":
				changed.Dependencies = []string{"missing"}
			case "priority":
				changed.Priority++
			case "resources":
				changed.Resources.ToolSlots["shell"]++
			case "attempts":
				changed.MaxAttempts++
			case "timeout":
				changed.Timeout++
			case "key":
				changed.IdempotencyKey = "other"
			}
			tasks := []domain.TaskSpec{changed}
			if field == "mixed" {
				tasks = append(tasks, taskSpec("new", "root"))
			} else if field == "duplicate" {
				tasks = append(tasks, original)
			}
			if err := graph.Patch(tasks); !errors.Is(err, ErrDuplicateTask) {
				t.Fatalf("patch returned %v, want duplicate conflict", err)
			}
			if !reflect.DeepEqual(graph.State(), before) {
				t.Fatal("conflicting patch changed graph")
			}
		})
	}
}

func TestPatchReplayAcceptsEquivalentDependencyOrder(t *testing.T) {
	tasks := []domain.TaskSpec{taskSpec("a"), taskSpec("b"), taskSpec("join", "a", "b")}
	tasks[0].Resources.ToolSlots = nil
	graph, err := New(tasks...)
	if err != nil {
		t.Fatal(err)
	}
	before := graph.State()
	tasks[0].Resources.ToolSlots = map[string]int64{}
	tasks[2].Dependencies = []string{"b", "a"}
	if err := graph.Patch(tasks); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(graph.State(), before) {
		t.Fatal("equivalent replay modified immutable specs")
	}
}

func TestPatchRejectsGraphEdgeCapacityAndRestore(t *testing.T) {
	tasks := boundaryTasks(MaxTaskDependencies)
	dependencies := taskIDs(tasks)
	for index := 0; index < MaxGraphEdges/MaxTaskDependencies; index++ {
		tasks = append(tasks, taskSpec(fmt.Sprintf("join-%04d", index), dependencies...))
	}
	graph, err := New(tasks...)
	if err != nil {
		t.Fatal(err)
	}
	before := graph.State()
	if err := graph.AddTask(taskSpec("overflow", dependencies[0])); !errors.Is(err, ErrGraphLimit) {
		t.Fatalf("edge capacity error = %v", err)
	}
	if !reflect.DeepEqual(graph.State(), before) {
		t.Fatal("edge capacity rejection mutated graph")
	}
	before.Tasks = append(before.Tasks, TaskSnapshot{Spec: taskSpec("overflow", dependencies[0]), Status: domain.TaskStatusWaitingDependency})
	if graph, err := Restore(before); graph != nil || !errors.Is(err, ErrInvalidState) || !errors.Is(err, ErrGraphLimit) {
		t.Fatalf("oversized restore = (%v, %v)", graph, err)
	}
}

func TestPatchCapacitySerializesConcurrentWriters(t *testing.T) {
	graph, err := New(boundaryTasks(MaxGraphTasks - 1)...)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 8)
	var workers sync.WaitGroup
	for index := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errs <- graph.AddTask(taskSpec(fmt.Sprintf("new-%d", index)))
		}()
	}
	workers.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrGraphLimit) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if accepted != 1 || graph.Len() != MaxGraphTasks || graph.Version() != 2 {
		t.Fatalf("accepted=%d count=%d version=%d", accepted, graph.Len(), graph.Version())
	}
}

func boundaryTasks(count int) []domain.TaskSpec {
	tasks := make([]domain.TaskSpec, count)
	for index := range tasks {
		tasks[index] = taskSpec(fmt.Sprintf("node-%04d", index))
	}
	return tasks
}
