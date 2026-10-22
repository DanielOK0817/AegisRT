package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
)

func TestManagerRunsProcessAgent(t *testing.T) {
	isolation := &spyIsolation{}
	manager := NewManager(testHostEnvironment(t), isolation)
	request := validRequest("success")
	request.Environment["AEGISRT_VISIBLE"] = "allowed"
	result, err := manager.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempt.Status != domain.AttemptStatusSucceeded || result.Process.ExitCode != 0 {
		t.Fatalf("unexpected execution result: %+v", result)
	}
	output := string(result.Process.Stderr)
	if !strings.Contains(output, `"task_id":"task-1"`) ||
		!strings.Contains(output, `"attempt_id":"attempt-1"`) ||
		!strings.Contains(output, `"attempt_number":1`) ||
		!strings.Contains(output, `"idempotency_key":"task-key-1"`) ||
		!strings.Contains(output, `"context_ref":"manifest-1"`) ||
		!strings.Contains(output, `"context":"resolved context"`) ||
		!strings.HasSuffix(output, "|allowed") {
		t.Fatalf("worker output = %q", output)
	}
	if result.Output.Version != agentprotocol.VersionV1 || len(result.Output.Artifacts) != 0 {
		t.Fatalf("unexpected parsed output: %+v", result.Output)
	}
	if !isolation.prepared || !isolation.cleaned || isolation.resources.CPUMilli != 250 {
		t.Fatalf("isolation was not applied: %+v", isolation)
	}
}

func TestManagerParsesAgentArtifacts(t *testing.T) {
	manager := NewManager(testHostEnvironment(t), nil)
	result, err := manager.Run(context.Background(), validRequest("artifact"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output.Artifacts) != 1 || result.Output.Artifacts[0].Name != "finding" ||
		result.Output.Artifacts[0].Content != "durable result" {
		t.Fatalf("unexpected parsed Agent output: %+v", result.Output)
	}
}

func TestManagerRejectsInvalidAndTruncatedSuccessfulOutput(t *testing.T) {
	manager := NewManager(testHostEnvironment(t), nil)
	for _, test := range []struct {
		mode string
		want error
	}{
		{mode: "invalid-output", want: agentprotocol.ErrInvalidOutput},
		{mode: "truncated-output", want: agentprotocol.ErrOutputTruncated},
	} {
		t.Run(test.mode, func(t *testing.T) {
			result, err := manager.Run(context.Background(), validRequest(test.mode))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if result.Attempt.Status != domain.AttemptStatusFailed || result.Attempt.Failure == "" {
				t.Fatalf("invalid output was not recorded as failure: %+v", result.Attempt)
			}
		})
	}
}

func TestManagerRecordsProcessFailure(t *testing.T) {
	manager := NewManager(testHostEnvironment(t), nil)
	result, err := manager.Run(context.Background(), validRequest("fail"))
	if !errors.Is(err, executor.ErrExit) {
		t.Fatalf("error = %v, want ErrExit", err)
	}
	if result.Attempt.Status != domain.AttemptStatusFailed || result.Attempt.Failure == "" {
		t.Fatalf("unexpected failed attempt: %+v", result.Attempt)
	}
}

func TestManagerUsesShortestTimeout(t *testing.T) {
	manager := NewManager(testHostEnvironment(t), nil)
	request := validRequest("sleep")
	request.Agent.ExecutionTimeout = 40 * time.Millisecond
	request.Task.Timeout = time.Second
	result, err := manager.Run(context.Background(), request)
	if !errors.Is(err, executor.ErrTimeout) || result.Attempt.Status != domain.AttemptStatusFailed {
		t.Fatalf("result=%+v error=%v, want timeout failure", result, err)
	}
}

func TestManagerValidatesExecutionBoundary(t *testing.T) {
	manager := NewManager(nil, nil)
	tests := []struct {
		name   string
		mutate func(*Request)
		want   error
	}{
		{name: "unsupported runtime", mutate: func(r *Request) { r.Agent.Runtime = "container" }, want: ErrUnsupportedRuntime},
		{name: "agent mismatch", mutate: func(r *Request) { r.Task.AgentID = "other-agent" }, want: ErrInvalidExecution},
		{name: "bad attempt", mutate: func(r *Request) { r.AttemptNumber = 0 }, want: ErrInvalidExecution},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest("success")
			test.mutate(&request)
			if _, err := manager.Run(context.Background(), request); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func validRequest(mode string) Request {
	return Request{
		AttemptID:     "attempt-1",
		AttemptNumber: 1,
		WorkerID:      "worker-local",
		Agent: domain.AgentSpec{
			ID: "agent-1", Name: "Test Agent", Runtime: "process",
			Entrypoint:     os.Args[0],
			Args:           []string{"-test.run=TestWorkerManagerHelperProcess", "--", mode},
			Resources:      domain.ResourceVector{CPUMilli: 500, MemoryMiB: 128},
			MaxConcurrency: 1, ExecutionTimeout: 5 * time.Second,
		},
		Task: domain.TaskSpec{
			ID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
			Goal: "return the supplied input", Priority: 1,
			Resources:   domain.ResourceVector{CPUMilli: 250, MemoryMiB: 64},
			MaxAttempts: 2, Timeout: 5 * time.Second, IdempotencyKey: "task-key-1",
		},
		Environment: map[string]string{"GO_WANT_AEGISRT_WORKER_HELPER": "1"},
		ContextRef:  "manifest-1",
		ContextText: "resolved context",
	}
}

func testHostEnvironment(t *testing.T) []string {
	t.Helper()
	environment := executor.MinimalHostEnvironment()
	return append(environment, "GOCOVERDIR="+t.TempDir())
}

type spyIsolation struct {
	prepared  bool
	cleaned   bool
	resources domain.ResourceVector
}

func (s *spyIsolation) PrepareHook(resources domain.ResourceVector) executor.PrepareHook {
	s.resources = resources.Clone()
	return func(_ context.Context, _ executor.Request, command *exec.Cmd) (executor.Cleanup, error) {
		s.prepared = command != nil
		return func() error {
			s.cleaned = true
			return nil
		}, nil
	}
}

func TestWorkerManagerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_AEGISRT_WORKER_HELPER") != "1" {
		return
	}
	mode := ""
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			break
		}
	}
	switch mode {
	case "success":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(8)
		}
		_, _ = fmt.Fprintf(os.Stderr, "%s|%s", input, os.Getenv("AEGISRT_VISIBLE"))
		fmt.Print(`{"version":"aegisrt.agent-output/v1","artifacts":[]}`)
	case "artifact":
		fmt.Print(`{"version":"aegisrt.agent-output/v1","artifacts":[{"name":"finding","content":"durable result","visibility":"workflow"}]}`)
	case "invalid-output":
		fmt.Print("not-json")
	case "truncated-output":
		fmt.Print(strings.Repeat("x", (1<<20)+1))
	case "fail":
		os.Exit(7)
	case "sleep":
		time.Sleep(5 * time.Second)
	default:
		os.Exit(9)
	}
	os.Exit(0)
}
