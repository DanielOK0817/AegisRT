package contextstate

import (
	"context"
	"io/fs"
	"testing"
	"time"
)

func TestDependencyReadUsesDurableOutputWithoutRecipientCheckpoint(t *testing.T) {
	store := &scriptedStore{loadErr: fs.ErrNotExist}
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
	command.Targets = command.Targets[:1]
	command.Artifacts[0].Targets = []string{"producer"}
	if _, err := r.PublishOutput(context.Background(), command, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := string(store.payload)
	for i := 0; i < 2; i++ {
		resolved, err := r.Resolve(context.Background(), dynamicOutputTask(), dynamicOutputAgent(), 64)
		if err != nil || resolved.Text == "" {
			t.Fatalf("resolve=%+v err=%v", resolved, err)
		}
		if string(store.payload) != before {
			t.Fatal("dependency read rewrote durable checkpoint")
		}
		other := dynamicOutputTask()
		other.ID = "unrelated"
		other.Dependencies = nil
		resolved, err = r.Resolve(context.Background(), other, dynamicOutputAgent(), 64)
		if err != nil || resolved.Text != "" {
			t.Fatalf("sibling leaked input: %+v %v", resolved, err)
		}
		r, err = NewPersistent(testConfig(), store, false, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
}
