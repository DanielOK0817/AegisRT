package runtimecore

import (
	"errors"
	"testing"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/scheduler"
)

func TestRuntimeSchedulesAndUnlocksDAG(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	runtime := newTestRuntime(t)
	if err := runtime.ApplyPatch([]domain.TaskSpec{
		task("analyze", "workflow", nil, 1, 300),
		task("report", "workflow", []string{"analyze"}, 1, 200),
	}, now); err != nil {
		t.Fatal(err)
	}

	assertTaskStatus(t, runtime, "analyze", domain.TaskStatusReady)
	assertTaskStatus(t, runtime, "report", domain.TaskStatusWaitingDependency)
	decision, err := runtime.Schedule(now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Candidate.TaskID != "analyze" {
		t.Fatalf("scheduled %q, want analyze", decision.Candidate.TaskID)
	}
	assertTaskStatus(t, runtime, "analyze", domain.TaskStatusAdmitted)
	if err := runtime.MarkStarted("analyze", 1, now); err != nil {
		t.Fatal(err)
	}
	assertTaskStatus(t, runtime, "analyze", domain.TaskStatusRunning)
	if got := runtime.Stats().Allocated.CPUMilli; got != 300 {
		t.Fatalf("allocated CPU = %d, want 300", got)
	}

	unlocked, err := runtime.Complete("analyze", domain.ResourceVector{CPUMilli: 250}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(unlocked) != 1 || unlocked[0].ID != "report" {
		t.Fatalf("unlocked = %+v, want report", unlocked)
	}
	assertTaskStatus(t, runtime, "analyze", domain.TaskStatusSucceeded)
	assertTaskStatus(t, runtime, "report", domain.TaskStatusReady)
	if got := runtime.Stats().Allocated.CPUMilli; got != 0 {
		t.Fatalf("allocated CPU = %d, want 0 after completion", got)
	}
}

func TestRuntimeDAGSignalsPrioritizeBlockingTask(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	runtime := newTestRuntime(t)
	if err := runtime.ApplyPatch([]domain.TaskSpec{
		task("critical-root", "workflow", nil, 0, 100),
		task("background", "workflow", nil, 2, 100),
		task("urgent-child", "workflow", []string{"critical-root"}, 10, 100),
	}, now); err != nil {
		t.Fatal(err)
	}
	decision, err := runtime.Schedule(now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Candidate.TaskID != "critical-root" {
		t.Fatalf("scheduled %q, want inherited-priority critical-root", decision.Candidate.TaskID)
	}
}

func TestRuntimeRejectsTaskThatCanNeverFit(t *testing.T) {
	runtime := newTestRuntime(t)
	err := runtime.ApplyPatch([]domain.TaskSpec{task("huge", "workflow", nil, 1, 2000)}, time.Now())
	if !errors.Is(err, ErrTaskExceedsCapacity) {
		t.Fatalf("error = %v, want ErrTaskExceedsCapacity", err)
	}
	if runtime.Stats().TaskCount != 0 {
		t.Fatal("rejected patch changed graph")
	}
}

func TestRuntimeInstancesHaveDistinctPersistentIdentities(t *testing.T) {
	first := newTestRuntime(t)
	second := newTestRuntime(t)
	if err := validateRuntimeInstanceID(first.InstanceID()); err != nil {
		t.Fatalf("first runtime instance ID is invalid: %v", err)
	}
	if err := validateRuntimeInstanceID(second.InstanceID()); err != nil {
		t.Fatalf("second runtime instance ID is invalid: %v", err)
	}
	if first.InstanceID() == second.InstanceID() {
		t.Fatalf("separate runtimes share instance ID %q", first.InstanceID())
	}
}

func TestRuntimeFairnessAcrossWorkflows(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	runtime := newTestRuntime(t)
	if err := runtime.ApplyPatch([]domain.TaskSpec{
		task("a1", "a", nil, 0, 500),
		task("a2", "a", nil, 0, 500),
		task("b1", "b", nil, 0, 500),
	}, now); err != nil {
		t.Fatal(err)
	}
	first, err := runtime.Schedule(now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Candidate.WorkflowID != "a" {
		t.Fatalf("first workflow = %q, want a", first.Candidate.WorkflowID)
	}
	if _, err = runtime.Complete(first.Candidate.TaskID, domain.ResourceVector{CPUMilli: 500}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	second, err := runtime.Schedule(now.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if second.Candidate.WorkflowID != "b" {
		t.Fatalf("second workflow = %q, want b after a consumed service", second.Candidate.WorkflowID)
	}
}

func TestRuntimeFailureReleasesResourcesWithoutUnlockingDependents(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	runtime := newTestRuntime(t)
	if err := runtime.ApplyPatch([]domain.TaskSpec{
		task("build", "workflow", nil, 1, 300),
		task("test", "workflow", []string{"build"}, 1, 200),
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(now); err != nil {
		t.Fatal(err)
	}
	failed, err := runtime.Fail("build", domain.ResourceVector{CPUMilli: 200})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != domain.TaskStatusFailed {
		t.Fatalf("failed status = %s", failed.Status)
	}
	assertTaskStatus(t, runtime, "test", domain.TaskStatusWaitingDependency)
	if got := runtime.Stats().Allocated.CPUMilli; got != 0 {
		t.Fatalf("allocated CPU = %d, want 0 after failure", got)
	}
}

func TestRuntimeFailureRejectsInvalidUsageWithoutReleasing(t *testing.T) {
	runtime := newTestRuntime(t)
	if err := runtime.ApplyPatch([]domain.TaskSpec{task("work", "workflow", nil, 1, 300)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Fail("work", domain.ResourceVector{CPUMilli: -1}); err == nil {
		t.Fatal("Fail accepted negative actual usage")
	}
	assertTaskStatus(t, runtime, "work", domain.TaskStatusAdmitted)
	if got := runtime.Stats().Allocated.CPUMilli; got != 300 {
		t.Fatalf("allocated CPU = %d, want reservation retained", got)
	}
}

func TestRuntimeScheduleEligibleFiltersReadyTasks(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	runtime := newTestRuntime(t)
	if err := runtime.ApplyPatch([]domain.TaskSpec{
		task("blocked-by-admission", "workflow", nil, 10, 100),
		task("eligible", "workflow", nil, 1, 100),
	}, now); err != nil {
		t.Fatal(err)
	}
	decision, err := runtime.ScheduleEligible(now, func(task domain.TaskSpec) bool {
		return task.ID == "eligible"
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Candidate.TaskID != "eligible" {
		t.Fatalf("scheduled %q, want eligible", decision.Candidate.TaskID)
	}
	assertTaskStatus(t, runtime, "blocked-by-admission", domain.TaskStatusReady)
}

func TestRuntimeScheduleEligibleRejectsAllWithoutMutation(t *testing.T) {
	runtime := newTestRuntime(t)
	if err := runtime.ApplyPatch([]domain.TaskSpec{task("ready", "workflow", nil, 1, 100)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ScheduleEligible(time.Now(), func(domain.TaskSpec) bool { return false }); !errors.Is(err, scheduler.ErrNoCandidate) {
		t.Fatalf("error = %v, want ErrNoCandidate", err)
	}
	assertTaskStatus(t, runtime, "ready", domain.TaskStatusReady)
	if !runtime.Stats().Allocated.IsZero() {
		t.Fatalf("filter rejection allocated resources: %+v", runtime.Stats().Allocated)
	}
}

func TestRuntimeRequeuesUnstartedAdmissionWithoutConsumingAttempt(t *testing.T) {
	runtime := newTestRuntime(t)
	spec := task("context-infra", "workflow", nil, 1, 100)
	spec.MaxAttempts = 1
	if err := runtime.ApplyPatch([]domain.TaskSpec{spec}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Schedule(time.Now()); err != nil {
		t.Fatal(err)
	}
	if runtime.AttemptNumber(spec.ID) != 1 {
		t.Fatal("schedule did not reserve first attempt")
	}
	if _, err := runtime.RequeueUnstarted(spec.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertTaskStatus(t, runtime, spec.ID, domain.TaskStatusReady)
	if runtime.AttemptNumber(spec.ID) != 0 || !runtime.Stats().Allocated.IsZero() {
		t.Fatalf("unstarted admission consumed attempt or resources: attempt=%d stats=%+v",
			runtime.AttemptNumber(spec.ID), runtime.Stats())
	}
	if _, err := runtime.Schedule(time.Now()); err != nil {
		t.Fatalf("requeued admission did not schedule: %v", err)
	}
	if runtime.AttemptNumber(spec.ID) != 1 {
		t.Fatalf("rescheduled attempt=%d, want 1", runtime.AttemptNumber(spec.ID))
	}
}

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	capacity := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 1024, LLMSlots: 2, TokenBudget: 10000}
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(policy, capacity)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func task(id, workflow string, dependencies []string, priority int, cpu int64) domain.TaskSpec {
	return domain.TaskSpec{
		ID:             id,
		WorkflowID:     workflow,
		AgentID:        "worker",
		Goal:           "test goal",
		Dependencies:   dependencies,
		Priority:       priority,
		Resources:      domain.ResourceVector{CPUMilli: cpu},
		MaxAttempts:    2,
		Timeout:        time.Minute,
		IdempotencyKey: workflow + "-" + id,
	}
}

func assertTaskStatus(t *testing.T, runtime *Runtime, taskID string, want domain.TaskStatus) {
	t.Helper()
	item, exists := runtime.Task(taskID)
	if !exists {
		t.Fatalf("task %q not found", taskID)
	}
	if item.Status != want {
		t.Fatalf("task %q status = %s, want %s", taskID, item.Status, want)
	}
}
