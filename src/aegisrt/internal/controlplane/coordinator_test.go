package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/contextdispatch"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/worker"
)

func TestCoordinatorRunsDAGEndToEnd(t *testing.T) {
	coordinator, runtime := newTestCoordinator(t, processAgent("success"))
	now := time.Unix(100, 0).UTC()
	if err := coordinator.ApplyPatch([]domain.TaskSpec{
		coordinatorTask("first", nil),
		coordinatorTask("second", []string{"first"}),
	}, now); err != nil {
		t.Fatal(err)
	}
	first, err := coordinator.Tick(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Execution.Attempt.Status != domain.AttemptStatusSucceeded ||
		len(first.NewlyReady) != 1 || first.NewlyReady[0].ID != "second" {
		t.Fatalf("unexpected first tick: %+v", first)
	}
	second, err := coordinator.Tick(context.Background(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if second.Decision.Candidate.TaskID != "second" {
		t.Fatalf("scheduled %q, want second", second.Decision.Candidate.TaskID)
	}
	assertCoordinatorStatus(t, runtime, "first", domain.TaskStatusSucceeded)
	assertCoordinatorStatus(t, runtime, "second", domain.TaskStatusSucceeded)
	if !runtime.Stats().Allocated.IsZero() {
		t.Fatalf("resources remained allocated: %+v", runtime.Stats().Allocated)
	}
}

func TestCoordinatorFailsTaskAndReleasesReservation(t *testing.T) {
	coordinator, runtime := newTestCoordinator(t, processAgent("fail"))
	if err := coordinator.ApplyPatch([]domain.TaskSpec{coordinatorTask("broken", nil)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Tick(context.Background(), time.Now())
	if !errors.Is(err, executor.ErrExit) {
		t.Fatalf("error = %v, want ErrExit", err)
	}
	if result.Execution.Attempt.Status != domain.AttemptStatusFailed {
		t.Fatalf("attempt status = %s", result.Execution.Attempt.Status)
	}
	assertCoordinatorStatus(t, runtime, "broken", domain.TaskStatusFailed)
	if !runtime.Stats().Allocated.IsZero() {
		t.Fatalf("resources remained allocated: %+v", runtime.Stats().Allocated)
	}
}

func TestCoordinatorRejectsUnknownAgentBeforeGraphMutation(t *testing.T) {
	coordinator, runtime := newTestCoordinator(t, processAgent("success"))
	task := coordinatorTask("unknown", nil)
	task.AgentID = "missing-agent"
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("error = %v, want ErrUnknownAgent", err)
	}
	if runtime.Stats().TaskCount != 0 {
		t.Fatal("rejected patch mutated the task graph")
	}
}

func TestCoordinatorRejectsDuplicateAgent(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	manager := worker.NewManager(coordinatorTestHostEnvironment(t), nil)
	agent := processAgent("success")
	if _, err := New(runtime, manager, "worker-1", []domain.AgentSpec{agent, agent}); !errors.Is(err, ErrDuplicateAgent) {
		t.Fatalf("error = %v, want ErrDuplicateAgent", err)
	}
}

func TestCoordinatorCopiesAgentRegistry(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	manager := worker.NewManager(coordinatorTestHostEnvironment(t), nil)
	agent := processAgent("success")
	agent.Capabilities = []string{"analyze"}
	agent.Resources.ToolSlots = map[string]int64{"shell": 1}
	coordinator, err := New(runtime, manager, "worker-1", []domain.AgentSpec{agent})
	if err != nil {
		t.Fatal(err)
	}
	agent.Args[len(agent.Args)-1] = "fail"
	agent.Capabilities[0] = "mutated"
	agent.Resources.ToolSlots["shell"] = 99
	registered := coordinator.agents[agent.ID]
	if registered.Args[len(registered.Args)-1] != "success" ||
		registered.Capabilities[0] != "analyze" || registered.Resources.ToolSlots["shell"] != 1 {
		t.Fatalf("agent registry retained caller-owned state: %#v", registered)
	}
}

func TestCoordinatorResolvesContextIntoWorkerInput(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	manager := worker.NewManager(coordinatorTestHostEnvironment(t), nil)
	agent := processAgent("stdin")
	resolver := newCoordinatorResolver(t, contextvm.PageSpec{
		Content: "private runtime context", Scope: contextvm.ScopeAgent,
		WorkflowID: "workflow-1", AgentID: agent.ID,
	}, agent.ID)
	coordinator, err := NewWithOptions(runtime, manager, "worker-1", []domain.AgentSpec{agent}, Options{ContextResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("with-context", nil)
	task.Resources.TokenBudget = 10
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Tick(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		ContextRef string `json:"context_ref"`
		Context    string `json:"context"`
	}
	if err := json.Unmarshal(result.Execution.Process.Stderr, &input); err != nil {
		t.Fatalf("decode worker stdin echo: %v", err)
	}
	if input.Context != "private runtime context" || input.ContextRef == "" {
		t.Fatalf("worker input did not contain resolved context: %#v", input)
	}
	if result.Context == nil || result.Context.ManifestID != input.ContextRef || result.Context.RefCount != 1 {
		t.Fatalf("safe context metadata = %#v", result.Context)
	}
}

func TestCoordinatorContextACLFailureDoesNotLaunchWorker(t *testing.T) {
	runtime := newCoordinatorRuntime(t)
	manager := worker.NewManager(coordinatorTestHostEnvironment(t), nil)
	agent := processAgent("stdin")
	resolver := newCoordinatorResolver(t, contextvm.PageSpec{
		Content: "other agent secret", Scope: contextvm.ScopeAgent,
		WorkflowID: "workflow-1", AgentID: "agent-2",
	}, agent.ID)
	coordinator, err := NewWithOptions(runtime, manager, "worker-1", []domain.AgentSpec{agent}, Options{ContextResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	task := coordinatorTask("acl-denied", nil)
	task.Resources.TokenBudget = 10
	if err := coordinator.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Tick(context.Background(), time.Now())
	if !errors.Is(err, contextvm.ErrAccessDenied) {
		t.Fatalf("Tick error = %v, want ErrAccessDenied", err)
	}
	if result.Execution.Attempt.ID != "" {
		t.Fatalf("worker launched after ACL failure: %#v", result.Execution)
	}
	assertCoordinatorStatus(t, runtime, task.ID, domain.TaskStatusFailed)
	if !runtime.Stats().Allocated.IsZero() {
		t.Fatalf("context failure leaked reservation: %+v", runtime.Stats().Allocated)
	}
}

func TestCoordinatorEnforcesAgentMaxConcurrency(t *testing.T) {
	agent := processAgent("sleep")
	agent.MaxConcurrency = 1
	coordinator, runtime := newTestCoordinator(t, agent)
	if err := coordinator.ApplyPatch([]domain.TaskSpec{
		coordinatorTask("first", nil), coordinatorTask("second", nil),
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Tick(context.Background(), time.Now())
		firstDone <- err
	}()
	waitForAgentRunning(t, coordinator, "agent-1")
	if _, err := coordinator.Tick(context.Background(), time.Now()); !errors.Is(err, scheduler.ErrNoCandidate) {
		t.Fatalf("second Tick error = %v, want ErrNoCandidate while agent is saturated", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Tick(context.Background(), time.Now()); err != nil {
		t.Fatalf("second task did not run after capacity was released: %v", err)
	}
	assertCoordinatorStatus(t, runtime, "first", domain.TaskStatusSucceeded)
	assertCoordinatorStatus(t, runtime, "second", domain.TaskStatusSucceeded)
}

func TestBuildAttemptIDBoundsLongTaskIDs(t *testing.T) {
	taskID := "t" + strings.Repeat("x", 127)
	first := AttemptID(taskID, 1)
	second := AttemptID(taskID, 2)
	if len(first) > 128 || first == second || !strings.HasSuffix(first, "-attempt-1") {
		t.Fatalf("unexpected attempt IDs: first=%q second=%q", first, second)
	}
}

func newTestCoordinator(t *testing.T, agent domain.AgentSpec) (*Coordinator, *runtimecore.Runtime) {
	t.Helper()
	runtime := newCoordinatorRuntime(t)
	manager := worker.NewManager(coordinatorTestHostEnvironment(t), nil)
	coordinator, err := New(runtime, manager, "worker-1", []domain.AgentSpec{agent})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, runtime
}

func coordinatorTestHostEnvironment(t *testing.T) []string {
	t.Helper()
	environment := executor.MinimalHostEnvironment()
	return append(environment, "GOCOVERDIR="+t.TempDir())
}

func newCoordinatorRuntime(t *testing.T) *runtimecore.Runtime {
	t.Helper()
	capacity := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 1024, LLMSlots: 2, TokenBudget: 10000}
	policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := runtimecore.New(policy, capacity)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func processAgent(mode string) domain.AgentSpec {
	return domain.AgentSpec{
		ID: "agent-1", Name: "Process Agent", Runtime: "process",
		Entrypoint:     os.Args[0],
		Args:           []string{"-test.run=TestCoordinatorHelperProcess", "--", mode},
		Resources:      domain.ResourceVector{CPUMilli: 250, MemoryMiB: 64, TokenBudget: 10000},
		MaxConcurrency: 1, ExecutionTimeout: 5 * time.Second,
	}
}

func coordinatorTask(id string, dependencies []string) domain.TaskSpec {
	return domain.TaskSpec{
		ID: id, WorkflowID: "workflow-1", AgentID: "agent-1", Goal: "test process execution",
		Dependencies: dependencies, Priority: 1,
		Resources:   domain.ResourceVector{CPUMilli: 250, MemoryMiB: 64},
		MaxAttempts: 2, Timeout: 5 * time.Second, IdempotencyKey: "key-" + id,
	}
}

func assertCoordinatorStatus(t *testing.T, runtime *runtimecore.Runtime, taskID string, expected domain.TaskStatus) {
	t.Helper()
	snapshot, exists := runtime.Task(taskID)
	if !exists || snapshot.Status != expected {
		t.Fatalf("task %q = %+v exists=%v, want %s", taskID, snapshot, exists, expected)
	}
}

func waitForAgentRunning(t *testing.T, coordinator *Coordinator, agentID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		coordinator.mu.Lock()
		running := coordinator.running[agentID]
		coordinator.mu.Unlock()
		if running == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("agent did not enter running state")
}

func newCoordinatorResolver(t *testing.T, pageSpec contextvm.PageSpec, manifestAgentID string) *contextdispatch.Resolver {
	t.Helper()
	store := contextvm.NewStore()
	page, err := contextvm.NewPage(pageSpec, contextvm.WhitespaceCounter{})
	if err != nil {
		t.Fatal(err)
	}
	ref, _, err := store.Put(page)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := contextvm.NewManifest("workflow-1", manifestAgentID, nil, []contextvm.ContextRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	registry := contextdispatch.NewRegistry()
	if err := registry.Set(manifest); err != nil {
		t.Fatal(err)
	}
	assembler, err := contextvm.NewAssembler(store, contextvm.WhitespaceCounter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := contextdispatch.NewResolver(registry, assembler)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func TestCoordinatorHelperProcess(t *testing.T) {
	mode := ""
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			break
		}
	}
	switch mode {
	case "success":
		os.Exit(0)
	case "fail":
		os.Exit(7)
	case "sleep":
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	case "stdin":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(8)
		}
		_, _ = os.Stderr.Write(input)
		_, _ = os.Stdout.Write([]byte(`{"version":"aegisrt.agent-output/v1","artifacts":[]}`))
		os.Exit(0)
	case "artifact":
		_, _ = os.Stdout.Write([]byte(`{"version":"aegisrt.agent-output/v1","artifacts":[{"name":"result","content":"coordinator durable result","visibility":"workflow"}]}`))
		os.Exit(0)
	}
}
