package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/worker"
)

func TestDaemonSettlesDeclaredShareOnlyAfterProcessStart(t *testing.T) {
	for _, mode := range []string{"success", "fail", "missing", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			capacity := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 1024}
			policy, err := scheduler.NewDAGDRCFS(capacity, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			r, err := runtimecore.New(policy, capacity)
			if err != nil {
				t.Fatal(err)
			}
			agent := processAgent(mode)
			if mode == "missing" {
				agent.Entrypoint = "aegisrt-nonexistent-settlement-executable"
			}
			spec := coordinatorTask("work", nil)
			if mode == "timeout" {
				agent = processAgent("sleep")
				spec.Timeout = 50 * time.Millisecond
			}
			c, err := New(r, worker.NewManager(coordinatorTestHostEnvironment(t), nil), "worker-1", []domain.AgentSpec{agent})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.ApplyPatch([]domain.TaskSpec{spec}, time.Now()); err != nil {
				t.Fatal(err)
			}
			result, runErr := c.Tick(context.Background(), time.Now())
			want := spec.Resources.DominantShare(capacity) / float64(spec.Priority+1)
			if mode == "missing" {
				want = 0
				if !errors.Is(runErr, executor.ErrStart) || result.Execution.Process.ProcessStarted {
					t.Fatalf("start failure=%+v err=%v", result, runErr)
				}
			} else if !result.Execution.Process.ProcessStarted {
				t.Fatalf("missing real process start: result=%+v err=%v", result, runErr)
			}
			if mode == "success" && runErr != nil {
				t.Fatal(runErr)
			}
			if mode == "timeout" && !errors.Is(runErr, executor.ErrTimeout) {
				t.Fatalf("timeout err=%v", runErr)
			}
			if got := policy.VRuntime(spec.WorkflowID); got != want || !r.Stats().Allocated.IsZero() {
				t.Fatalf("charge=%v want=%v allocated=%+v", got, want, r.Stats().Allocated)
			}
		})
	}
}
