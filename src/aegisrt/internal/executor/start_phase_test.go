package executor

import (
	"context"
	"errors"
	"testing"
)

func TestRequestStartHookRunsAfterProcessStartAndCancelsCleanly(t *testing.T) {
	request := helperRequest("sleep")
	called := false
	request.OnStarted = func(_ context.Context, _ Request, pid int) error {
		called = true
		if pid <= 0 {
			t.Fatal("start callback without process")
		}
		return context.Canceled
	}
	result, err := NewProcessRunner(MinimalHostEnvironment()).Run(context.Background(), request)
	if !called || !result.ProcessStarted || !result.WaitCompleted || !result.Canceled || !errors.Is(err, ErrCanceled) {
		t.Fatalf("callback cancellation result=%+v err=%v", result, err)
	}
}

func TestFailedStartDoesNotHaveProcessStartTimeOrCallback(t *testing.T) {
	request := helperRequest("success")
	request.Command = "aegisrt-intentionally-missing-executable"
	request.OnStarted = func(context.Context, Request, int) error { t.Fatal("callback before process start"); return nil }
	result, err := NewProcessRunner(nil).Run(context.Background(), request)
	if !errors.Is(err, ErrStart) || !result.StartedAt.IsZero() {
		t.Fatalf("start failure reported execution start: %+v, %v", result, err)
	}
}
