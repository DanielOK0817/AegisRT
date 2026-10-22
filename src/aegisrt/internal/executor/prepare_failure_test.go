package executor

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestPreparationFailurePreservesCauseAndCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("prepare unavailable")} {
		t.Run(cause.Error(), func(t *testing.T) {
			cleaned := 0
			runner := NewProcessRunnerWithPrepare(MinimalHostEnvironment(), func(context.Context, Request, *exec.Cmd) (Cleanup, error) {
				return func() error { cleaned++; return nil }, cause
			})
			result, err := runner.Run(context.Background(), helperRequest("success"))
			if !errors.Is(err, cause) || !errors.Is(err, ErrPrepareHook) || cleaned != 1 || result.ProcessStarted {
				t.Fatalf("result=%+v err=%v cleanup=%d", result, err, cleaned)
			}
			if result.Canceled != errors.Is(cause, context.Canceled) || result.TimedOut != errors.Is(cause, context.DeadlineExceeded) {
				t.Fatalf("preparation classification=%+v", result)
			}
		})
	}
}
