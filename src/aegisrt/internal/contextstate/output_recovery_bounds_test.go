package contextstate

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"math"
	"strings"
	"testing"
	"time"
)

func TestOutputPreflightRejectsCheckpointCapacityWithoutWriting(t *testing.T) {
	config := testConfig()
	config.Limits.MaxCheckpointBytes = 2500
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(config, store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "producer")
	if err != nil {
		t.Fatal(err)
	}
	command := testOutputCommand(manifest.ID, manifest.ID)
	command.AttemptNumber = 1
	command.Targets = command.Targets[:1]
	command.Artifacts[0].Targets = command.Artifacts[0].Targets[:1]
	command.Artifacts[0].Content = strings.Repeat("x", 3000)
	before := repository.Stats()
	payloadBefore, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ValidateOutput(context.Background(), command); !errors.Is(err, ErrCheckpointCapacity) {
		t.Fatalf("preflight error = %v, want ErrCheckpointCapacity", err)
	}
	if _, err := repository.PublishOutput(context.Background(), command, time.Now()); !errors.Is(err, ErrCheckpointCapacity) {
		t.Fatalf("publication error = %v, want ErrCheckpointCapacity", err)
	}
	payloadAfter, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if repository.Stats() != before || !bytes.Equal(payloadBefore, payloadAfter) || !repository.Healthy() {
		t.Fatal("capacity rejection changed durable state or repository health")
	}
}

func TestOutputPreflightPreservesPersistentGenerationAndNeverWrites(t *testing.T) {
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(testConfig(), store, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	command := OutputCommand{TaskID: "task", WorkflowID: "workflow-1", ProducerAgentID: "producer", AttemptNumber: 1}
	before := repository.Stats()
	payloadBefore, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("preflight must not write the real checkpoint")
	if err := repository.ValidateOutput(context.Background(), command); err != nil {
		t.Fatalf("valid preflight failed: %v", err)
	}
	if repository.Stats() != before || !bytes.Equal(payloadBefore, store.payload) {
		t.Fatal("successful preflight changed durable state")
	}
	repository.generation = math.MaxUint64
	before = repository.Stats()
	if err := repository.ValidateOutput(context.Background(), command); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("exhausted generation preflight error = %v, want ErrUnavailable", err)
	}
	if repository.Stats() != before || !repository.Healthy() || !bytes.Equal(payloadBefore, store.payload) {
		t.Fatal("generation rejection changed the original repository")
	}
}

func TestOutputWithProducerRecipientSurvivesPersistentRestore(t *testing.T) {
	for _, withConsumer := range []bool{false, true} {
		t.Run(map[bool]string{false: "producer-only", true: "producer-and-consumer"}[withConsumer], func(t *testing.T) {
			store := &scriptedStore{loadErr: fs.ErrNotExist}
			config := testConfig()
			repository, err := NewPersistent(config, store, true, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			producer, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "producer")
			if err != nil {
				t.Fatal(err)
			}
			consumer, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "consumer")
			if err != nil {
				t.Fatal(err)
			}
			command := testOutputCommand(producer.ID, consumer.ID)
			command.AttemptNumber = 1
			command.Artifacts[0].Recipients = []string{"producer", "consumer"}
			if !withConsumer {
				command.Targets = command.Targets[:1]
				command.Artifacts[0].Targets = command.Artifacts[0].Targets[:1]
				command.Artifacts[0].Recipients = command.Artifacts[0].Recipients[:1]
			}
			if err := repository.ValidateOutput(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.PublishOutput(context.Background(), command, time.Now()); err != nil {
				t.Fatal(err)
			}
			restored, err := NewPersistent(config, store, false, time.Now())
			if err != nil {
				t.Fatalf("restore accepted output: %v", err)
			}
			before := restored.Stats()
			result, err := restored.PublishOutput(context.Background(), command, time.Now())
			if err != nil || !result.IdempotentReplay || result.Record.AttemptNumber != 1 {
				t.Fatalf("restored replay = %+v, error = %v", result, err)
			}
			if restored.Stats() != before {
				t.Fatal("restored replay changed repository")
			}
		})
	}
}
