package runtimecore

import (
	"errors"
	"testing"
	"time"

	"aegisrt/internal/domain"
)

func TestStartedCancellationAndInterruptionSettleDeclaredShare(t *testing.T) {
	for _, terminal := range []string{"cancel", "interrupt"} {
		for _, started := range []bool{false, true} {
			t.Run(terminal+map[bool]string{false: "/admitted", true: "/running"}[started], func(t *testing.T) {
				r := newTestRuntime(t)
				now := time.Now()
				spec := task("work", "workflow", nil, 1, 300)
				if err := r.ApplyPatch([]domain.TaskSpec{spec}, now); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Schedule(now); err != nil {
					t.Fatal(err)
				}
				if started {
					if err := r.MarkStarted(spec.ID, 1, now); err != nil {
						t.Fatal(err)
					}
				}
				if terminal == "cancel" {
					if _, err := r.Cancel(spec.ID); err != nil {
						t.Fatal(err)
					}
					if _, err := r.Cancel(spec.ID); err != nil {
						t.Fatal(err)
					}
				} else if _, err := r.Interrupt(spec.ID, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				want := 0.0
				if started {
					want = spec.Resources.DominantShare(r.scheduler.Capacity()) / float64(spec.Priority+1)
				}
				assertSettledShare(t, r, spec.WorkflowID, want)
			})
		}
	}
}

func TestRecoverySettlesOnlyPersistedRunningAttemptOnce(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "admitted", true: "running"}[started], func(t *testing.T) {
			store := &memoryCheckpointStore{}
			now := time.Now()
			r := openTestRuntime(t, store, testCapacity(), now)
			spec := task("work", "workflow", nil, 1, 300)
			if err := r.ApplyPatch([]domain.TaskSpec{spec}, now); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Schedule(now); err != nil {
				t.Fatal(err)
			}
			if started {
				if err := r.MarkStarted(spec.ID, 1, now); err != nil {
					t.Fatal(err)
				}
			}
			want := 0.0
			if started {
				want = spec.Resources.DominantShare(testCapacity()) / float64(spec.Priority+1)
			}
			for range 2 {
				r, _ = reopenTestRuntime(t, store, testCapacity(), now.Add(time.Minute))
				assertSettledShare(t, r, spec.WorkflowID, want)
			}
		})
	}
}

func TestDeclaredShareSettlementDoesNotInventDurationUsage(t *testing.T) {
	for _, duration := range []time.Duration{time.Millisecond, time.Hour} {
		r := newTestRuntime(t)
		now := time.Now()
		spec := task("work", "workflow", nil, 1, 300)
		if err := r.ApplyPatch([]domain.TaskSpec{spec}, now); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Schedule(now); err != nil {
			t.Fatal(err)
		}
		if err := r.MarkStarted(spec.ID, 1, now); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Complete(spec.ID, spec.Resources, now.Add(duration)); err != nil {
			t.Fatal(err)
		}
		assertSettledShare(t, r, spec.WorkflowID, spec.Resources.DominantShare(r.scheduler.Capacity())/float64(spec.Priority+1))
	}
}

func TestCancellationSettlementSurvivesCheckpointFailureWithoutDoubleCharge(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		for _, written := range []bool{false, true} {
			if written && !uncertain {
				continue
			}
			store := &memoryCheckpointStore{}
			now := time.Now()
			r := openTestRuntime(t, store, testCapacity(), now)
			spec := task("work", "workflow", nil, 1, 300)
			if err := r.ApplyPatch([]domain.TaskSpec{spec}, now); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Schedule(now); err != nil {
				t.Fatal(err)
			}
			if err := r.MarkStarted(spec.ID, 1, now); err != nil {
				t.Fatal(err)
			}
			if _, err := r.RequestCancel(spec.ID); err != nil {
				t.Fatal(err)
			}
			store.saveErr = errors.New("injected settlement checkpoint failure")
			store.saveOnError = written
			if uncertain {
				store.saveErr = uncertainSaveError{cause: store.saveErr}
			}
			if _, err := r.Cancel(spec.ID); !errors.Is(err, ErrCheckpointUnavailable) {
				t.Fatalf("Cancel error=%v", err)
			}
			if !uncertain && (r.Stats().Allocated.IsZero() || len(r.scheduler.State().PolicyState.WorkflowVRuntime) != 0) {
				t.Fatal("failed checkpoint released a reservation or retained a charge")
			}
			store.saveErr = nil
			for range 2 {
				r, _ = reopenTestRuntime(t, store, testCapacity(), now.Add(time.Minute))
				assertTaskStatus(t, r, spec.ID, domain.TaskStatusCanceled)
				assertSettledShare(t, r, spec.WorkflowID, spec.Resources.DominantShare(testCapacity())/float64(spec.Priority+1))
			}
		}
	}
}

func assertSettledShare(t *testing.T, r *Runtime, workflow string, want float64) {
	t.Helper()
	got := 0.0
	state := r.scheduler.State()
	for _, row := range state.PolicyState.WorkflowVRuntime {
		if row.WorkflowID == workflow {
			got = row.VRuntime
		}
	}
	if got != want || !state.Allocated.IsZero() || len(state.Reservations) != 0 {
		t.Fatalf("settlement=%v want=%v allocated=%+v reservations=%v", got, want, state.Allocated, state.Reservations)
	}
}
