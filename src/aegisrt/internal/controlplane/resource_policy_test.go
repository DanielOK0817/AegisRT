package controlplane

import (
	"errors"
	"testing"
	"time"

	"aegisrt/internal/domain"
)

func TestCoordinatorAppliesAgentOSResourceDefaults(t *testing.T) {
	agent := processAgent("success")
	coordinator, runtime := newTestCoordinator(t, agent)
	task := coordinatorTask("defaulted", nil)
	task.Resources.CPUMilli = 0
	task.Resources.MemoryMiB = 0
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, exists := runtime.Task(task.ID)
	if !exists {
		t.Fatal("task was not admitted")
	}
	if snapshot.Spec.Resources.CPUMilli != agent.Resources.CPUMilli ||
		snapshot.Spec.Resources.MemoryMiB != agent.Resources.MemoryMiB {
		t.Fatalf("resources were not defaulted: %+v", snapshot.Spec.Resources)
	}
}

func TestCoordinatorRejectsResourcesAboveLocalAgentPolicy(t *testing.T) {
	agent := processAgent("success")
	coordinator, runtime := newTestCoordinator(t, agent)
	task := coordinatorTask("oversized", nil)
	task.Resources.CPUMilli = agent.Resources.CPUMilli + 1
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); !errors.Is(err, ErrAgentResourcePolicy) {
		t.Fatalf("error = %v, want ErrAgentResourcePolicy", err)
	}
	if runtime.Stats().TaskCount != 0 {
		t.Fatal("rejected resource request mutated task graph")
	}
}
