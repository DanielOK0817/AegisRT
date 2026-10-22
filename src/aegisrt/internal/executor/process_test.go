package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestProcessRunnerSuccessAndEnvironmentIsolation(t *testing.T) {
	t.Setenv("AEGISRT_PARENT_SECRET", "must-not-leak")
	runner := NewProcessRunner(MinimalHostEnvironment())
	request := helperRequest("success")
	request.Env["AEGISRT_ALLOWED"] = "visible"
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || string(result.Stdout) != "visible|" {
		t.Fatalf("unexpected result: exit=%d stdout=%q", result.ExitCode, result.Stdout)
	}
	if !result.ProcessStarted || !result.WaitCompleted {
		t.Fatalf("successful process lifecycle = started=%v waited=%v", result.ProcessStarted, result.WaitCompleted)
	}
	if result.ProcessStartedAt.IsZero() || result.WaitCompletedAt.Before(result.ProcessStartedAt) || result.DeadlineAt.IsZero() {
		t.Fatalf("successful process timestamps = %#v", result)
	}
	if result.ProcessStartedAt == result.ProcessStartedAt.Round(0) || result.DeadlineAt == result.DeadlineAt.Round(0) {
		t.Fatal("process lifecycle timestamps lost their monotonic clock readings")
	}
	if !strings.HasPrefix(string(result.Stderr), "diagnostic") {
		t.Fatalf("stderr = %q, want diagnostic prefix", result.Stderr)
	}
}

func TestProcessRunnerNonZeroExit(t *testing.T) {
	runner := NewProcessRunner(MinimalHostEnvironment())
	result, err := runner.Run(context.Background(), helperRequest("fail"))
	if !errors.Is(err, ErrExit) {
		t.Fatalf("error = %v, want ErrExit", err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", result.ExitCode)
	}
	if !result.ProcessStarted || !result.WaitCompleted {
		t.Fatalf("failed process lifecycle = started=%v waited=%v", result.ProcessStarted, result.WaitCompleted)
	}
	if result.ProcessStartedAt.IsZero() || result.WaitCompletedAt.Before(result.ProcessStartedAt) {
		t.Fatalf("failed process timestamps = %#v", result)
	}
}

func TestProcessRunnerTimeout(t *testing.T) {
	runner := NewProcessRunner(MinimalHostEnvironment())
	request := helperRequest("sleep")
	request.Timeout = 50 * time.Millisecond
	result, err := runner.Run(context.Background(), request)
	if !errors.Is(err, ErrTimeout) || !result.TimedOut {
		t.Fatalf("result=%+v error=%v, want timeout", result, err)
	}
}

func TestProcessRunnerCancellation(t *testing.T) {
	runner := NewProcessRunner(MinimalHostEnvironment())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runner.Run(ctx, helperRequest("sleep"))
	if !errors.Is(err, ErrCanceled) || !result.Canceled {
		t.Fatalf("result=%+v error=%v, want cancellation", result, err)
	}
}

func TestProcessRunnerBoundsOutput(t *testing.T) {
	runner := NewProcessRunner(MinimalHostEnvironment())
	request := helperRequest("large")
	request.MaxOutputBytes = 32
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stdout) != 32 || !result.StdoutTruncated {
		t.Fatalf("stdout length=%d truncated=%v", len(result.Stdout), result.StdoutTruncated)
	}
}

func TestProcessRunnerValidatesRequest(t *testing.T) {
	runner := NewProcessRunner(nil)
	_, err := runner.Run(context.Background(), Request{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

func TestProcessRunnerDistinguishesStartFailureFromWaitCompletion(t *testing.T) {
	runner := NewProcessRunner(nil)
	request := helperRequest("success")
	request.Command = `Z:\aegisrt-missing\agent.exe`
	result, err := runner.Run(context.Background(), request)
	if !errors.Is(err, ErrStart) {
		t.Fatalf("error = %v, want ErrStart", err)
	}
	if result.ProcessStarted || result.WaitCompleted || !result.ProcessStartedAt.IsZero() || !result.WaitCompletedAt.IsZero() {
		t.Fatalf("start failure lifecycle = started=%v waited=%v", result.ProcessStarted, result.WaitCompleted)
	}
}

func TestProcessRunnerInvokesStartHook(t *testing.T) {
	called := false
	runner := NewProcessRunner(MinimalHostEnvironment(), func(_ context.Context, request Request, pid int) error {
		called = true
		if request.AttemptID != "attempt-1" || pid <= 0 {
			t.Fatalf("unexpected hook values: request=%+v pid=%d", request, pid)
		}
		return nil
	})
	if _, err := runner.Run(context.Background(), helperRequest("success")); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("start hook was not invoked")
	}
}

func TestProcessRunnerStopsProcessWhenStartHookFails(t *testing.T) {
	runner := NewProcessRunner(MinimalHostEnvironment(), func(context.Context, Request, int) error {
		return errors.New("isolation unavailable")
	})
	result, err := runner.Run(context.Background(), helperRequest("sleep"))
	if !errors.Is(err, ErrStartHook) {
		t.Fatalf("error = %v, want ErrStartHook", err)
	}
	if result.FinishedAt.IsZero() {
		t.Fatal("failed start hook did not finalize the result")
	}
}

func TestProcessRunnerPreparesAndCleansExecution(t *testing.T) {
	prepared := false
	cleaned := false
	runner := NewProcessRunnerWithPrepare(MinimalHostEnvironment(), func(_ context.Context, request Request, command *exec.Cmd) (Cleanup, error) {
		prepared = true
		if request.AttemptID != "attempt-1" || command.Path == "" {
			t.Fatalf("unexpected prepare values: request=%+v command=%+v", request, command)
		}
		return func() error {
			cleaned = true
			return nil
		}, nil
	})
	if _, err := runner.Run(context.Background(), helperRequest("success")); err != nil {
		t.Fatal(err)
	}
	if !prepared || !cleaned {
		t.Fatalf("lifecycle prepared=%v cleaned=%v", prepared, cleaned)
	}
}

func TestProcessRunnerReportsCleanupFailure(t *testing.T) {
	wantErr := errors.New("cleanup failed")
	runner := NewProcessRunnerWithPrepare(MinimalHostEnvironment(), func(context.Context, Request, *exec.Cmd) (Cleanup, error) {
		return func() error { return wantErr }, nil
	})
	result, err := runner.Run(context.Background(), helperRequest("success"))
	if !errors.Is(err, ErrCleanup) || !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want cleanup failure", err)
	}
	if result.FinishedAt.IsZero() {
		t.Fatal("cleanup failure lost process result")
	}
}

func helperRequest(mode string) Request {
	return Request{
		AttemptID: "attempt-1",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestWorkerHelperProcess", "--", mode},
		Env:       map[string]string{"GO_WANT_AEGISRT_HELPER": "1"},
		Timeout:   5 * time.Second,
	}
}

func TestWorkerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_AEGISRT_HELPER") != "1" {
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
		fmt.Printf("%s|%s", os.Getenv("AEGISRT_ALLOWED"), os.Getenv("AEGISRT_PARENT_SECRET"))
		fmt.Fprint(os.Stderr, "diagnostic")
	case "fail":
		os.Exit(7)
	case "sleep":
		time.Sleep(5 * time.Second)
	case "large":
		fmt.Print(strings.Repeat("x", 1024))
	default:
		os.Exit(9)
	}
	os.Exit(0)
}
