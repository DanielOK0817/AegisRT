package contextstate

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"
)

func TestOutputAttemptIdentityPersistsAndRejectsAnotherAttempt(t *testing.T) {
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	r, err := NewPersistent(testConfig(), store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	command := OutputCommand{TaskID: "task", WorkflowID: "workflow-1", ProducerAgentID: "producer", AttemptNumber: 2}
	if _, err := r.PublishOutput(context.Background(), command, time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err = NewPersistent(testConfig(), store, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.PublishOutput(context.Background(), command, time.Now())
	if err != nil || !replay.IdempotentReplay || replay.Record.AttemptNumber != 2 {
		t.Fatalf("replay=%+v error=%v", replay, err)
	}
	command.AttemptNumber = 3
	if _, err := r.PublishOutput(context.Background(), command, time.Now()); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-attempt overwrite=%v", err)
	}
}

func TestOutputPreflightDoesNotExposeOrPersistArtifacts(t *testing.T) {
	r, producer, consumer := outputRepository(t)
	command := testOutputCommand(producer, consumer)
	command.AttemptNumber = 1
	before := r.Stats()
	if err := r.ValidateOutput(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if r.Stats() != before {
		t.Fatal("preflight changed repository")
	}
	if _, err := r.Output(command.TaskID); !errors.Is(err, ErrOutputNotFound) {
		t.Fatalf("preflight exposed result: %v", err)
	}
	command.Targets[0].TaskTokenBudget = 1
	if err := r.ValidateOutput(context.Background(), command); err == nil {
		t.Fatal("preflight ignored budget")
	}
}

func TestOutputCheckpointFailureReplaysSavedAttemptExactlyOnce(t *testing.T) {
	for _, written := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-written", true: "written-ack-lost"}[written], func(t *testing.T) {
			store := &outputFailureStore{scriptedStore: scriptedStore{loadErr: fs.ErrNotExist}, written: written}
			r, err := NewPersistent(testConfig(), store, true, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			producer, _, err := r.EnsureEmptyWithOwnership("workflow-1", "producer")
			if err != nil {
				t.Fatal(err)
			}
			consumer, _, err := r.EnsureEmptyWithOwnership("workflow-1", "consumer")
			if err != nil {
				t.Fatal(err)
			}
			command := testOutputCommand(producer.ID, consumer.ID)
			command.AttemptNumber = 1
			store.fail = true
			if _, err := r.PublishOutput(context.Background(), command, time.Now()); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("publication fault=%v", err)
			}
			store.fail = false
			r, err = NewPersistent(testConfig(), store, false, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			result, err := r.PublishOutput(context.Background(), command, time.Now())
			if err != nil || result.IdempotentReplay != written || result.Record.AttemptNumber != 1 {
				t.Fatalf("recovery=%+v err=%v", result, err)
			}
			before := r.Stats()
			if _, err := r.PublishOutput(context.Background(), command, time.Now()); err != nil {
				t.Fatal(err)
			}
			if r.Stats() != before || before.OutputCount != 1 {
				t.Fatal("replay changed manifests or duplicated record")
			}
		})
	}
}

type outputFailureStore struct {
	scriptedStore
	fail, written bool
}

func (s *outputFailureStore) Save(payload []byte) error {
	if s.fail {
		if !s.written {
			return errors.New("output checkpoint write failed")
		}
		if err := s.scriptedStore.Save(payload); err != nil {
			return err
		}
		return uncertainError{cause: errors.New("output saved, acknowledgement lost")}
	}
	return s.scriptedStore.Save(payload)
}
