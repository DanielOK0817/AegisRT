package taskgraph

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/domain"
)

func taskSpec(id string, dependencies ...string) domain.TaskSpec {
	return domain.TaskSpec{
		ID:             id,
		WorkflowID:     "workflow-1",
		AgentID:        "agent-1",
		Goal:           "Execute " + id,
		Dependencies:   dependencies,
		Priority:       1,
		Resources:      domain.ResourceVector{CPUMilli: 100, ToolSlots: map[string]int64{"shell": 1}},
		MaxAttempts:    2,
		Timeout:        time.Minute,
		IdempotencyKey: "key-" + id,
	}
}

func taskIDs(tasks []domain.TaskSpec) []string {
	if len(tasks) == 0 {
		return nil
	}
	result := make([]string, len(tasks))
	for index, task := range tasks {
		result[index] = task.ID
	}
	return result
}

func TestNewRejectsInvalidGraphs(t *testing.T) {
	otherWorkflow := taskSpec("other", "root")
	otherWorkflow.WorkflowID = "workflow-2"
	invalid := taskSpec("invalid")
	invalid.Goal = " "
	tests := []struct {
		name  string
		tasks []domain.TaskSpec
		want  error
	}{
		{name: "duplicate id", tasks: []domain.TaskSpec{taskSpec("root"), taskSpec("root")}, want: ErrDuplicateTask},
		{name: "missing dependency", tasks: []domain.TaskSpec{taskSpec("child", "missing")}, want: ErrMissingDependency},
		{name: "two node cycle", tasks: []domain.TaskSpec{taskSpec("a", "b"), taskSpec("b", "a")}, want: ErrCycleDetected},
		{name: "cross workflow dependency", tasks: []domain.TaskSpec{taskSpec("root"), otherWorkflow}, want: ErrCrossWorkflowDependency},
		{name: "invalid spec", tasks: []domain.TaskSpec{invalid}, want: ErrInvalidTaskSpec},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			graph, err := New(test.tasks...)
			if !errors.Is(err, test.want) {
				t.Fatalf("New() error = %v, want errors.Is(%v)", err, test.want)
			}
			if graph != nil {
				t.Fatal("New() returned a partially initialized graph")
			}
		})
	}
}

func TestReadyTasksAndCompletionUnlock(t *testing.T) {
	graph, err := New(
		taskSpec("join", "left", "right"),
		taskSpec("right", "root"),
		taskSpec("root"),
		taskSpec("left", "root"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := taskIDs(graph.ReadyTasks()), []string{"root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial ready tasks = %v, want %v", got, want)
	}

	newlyReady, err := graph.Complete("root")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := taskIDs(newlyReady), []string{"left", "right"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root unlocked = %v, want %v", got, want)
	}
	if newlyReady, err = graph.Complete("left"); err != nil || len(newlyReady) != 0 {
		t.Fatalf("left completion = (%v, %v), want no unlock", taskIDs(newlyReady), err)
	}
	if newlyReady, err = graph.Complete("right"); err != nil {
		t.Fatal(err)
	}
	if got, want := taskIDs(newlyReady), []string{"join"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("right unlocked = %v, want %v", got, want)
	}
	if status, _ := graph.Status("join"); status != domain.TaskStatusReady {
		t.Fatalf("join status = %s, want READY", status)
	}

	version := graph.Version()
	if unlocked, err := graph.Complete("root"); err != nil || len(unlocked) != 0 {
		t.Fatalf("duplicate completion = (%v, %v), want idempotent success", unlocked, err)
	}
	if graph.Version() != version {
		t.Fatal("idempotent completion changed graph version")
	}
}

func TestApplyPatchIsAtomicAndSupportsForwardReferences(t *testing.T) {
	graph, err := New(taskSpec("root"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Complete("root"); err != nil {
		t.Fatal(err)
	}
	beforeVersion := graph.Version()
	badPatch := Patch{Tasks: []domain.TaskSpec{
		taskSpec("valid", "root"),
		taskSpec("invalid", "missing"),
	}}
	if err := graph.ApplyPatch(badPatch); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("ApplyPatch() error = %v, want missing dependency", err)
	}
	if graph.Len() != 1 || graph.Version() != beforeVersion {
		t.Fatal("failed patch mutated the graph")
	}
	if _, exists := graph.Task("valid"); exists {
		t.Fatal("failed patch left a partial task")
	}

	goodPatch := Patch{Tasks: []domain.TaskSpec{
		taskSpec("last", "middle"),
		taskSpec("middle", "root"),
	}}
	if err := graph.ApplyPatch(goodPatch); err != nil {
		t.Fatal(err)
	}
	if got, want := taskIDs(graph.ReadyTasks()), []string{"middle"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ready tasks after patch = %v, want %v", got, want)
	}
	if err := graph.ApplyPatch(Patch{}); !errors.Is(err, ErrEmptyPatch) {
		t.Fatalf("empty patch error = %v, want ErrEmptyPatch", err)
	}
}

func TestGraphCopiesMutableSpecFields(t *testing.T) {
	original := taskSpec("root")
	graph, err := New(original)
	if err != nil {
		t.Fatal(err)
	}
	original.Resources.ToolSlots["shell"] = 99

	view, exists := graph.Task("root")
	if !exists {
		t.Fatal("root missing")
	}
	view.Spec.Resources.ToolSlots["shell"] = 88
	view.Spec.Dependencies = append(view.Spec.Dependencies, "foreign")
	viewAgain, _ := graph.Task("root")
	if viewAgain.Spec.Resources.ToolSlots["shell"] != 1 || len(viewAgain.Spec.Dependencies) != 0 {
		t.Fatal("graph exposed mutable internal TaskSpec fields")
	}
}

func TestGraphConcurrentReadersAndPatches(t *testing.T) {
	graph, err := New(taskSpec("root"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Complete("root"); err != nil {
		t.Fatal(err)
	}

	const count = 32
	errs := make(chan error, count)
	var writers sync.WaitGroup
	for index := 0; index < count; index++ {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			id := fmt.Sprintf("task-%02d", index)
			if err := graph.AddTask(taskSpec(id, "root")); err != nil {
				errs <- err
			}
		}(index)
	}
	var readers sync.WaitGroup
	for index := 0; index < 8; index++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for iteration := 0; iteration < 100; iteration++ {
				_ = graph.ReadyTasks()
				_ = graph.Snapshot()
				_ = graph.Version()
			}
		}()
	}
	writers.Wait()
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent AddTask() failed: %v", err)
	}
	if got := graph.Len(); got != count+1 {
		t.Fatalf("Len() = %d, want %d", got, count+1)
	}
	if got := len(graph.ReadyTasks()); got != count {
		t.Fatalf("ready count = %d, want %d", got, count)
	}
}
