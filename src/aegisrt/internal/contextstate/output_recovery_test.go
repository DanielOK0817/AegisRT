package contextstate

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"testing"
	"time"
)

func TestConcurrentResolveReadsOutputWithoutCheckpointWrites(t *testing.T) {
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	repository, err := NewPersistent(testConfig(), store, true, time.Unix(720, 0))
	if err != nil {
		t.Fatal(err)
	}
	producer, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "producer")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "consumer"); err != nil {
		t.Fatal(err)
	}
	command := OutputCommand{
		TaskID: "produce", WorkflowID: "workflow-1", ProducerAgentID: "producer",
		Targets: []OutputTarget{{AgentID: "producer", ExpectedManifestID: producer.ID,
			TaskTokenBudget: 64, PrincipalPinnedBudget: 64}},
		Artifacts: []ArtifactCommand{{Name: "analysis", Content: "hydrate once",
			Visibility: VisibilityWorkflow, Targets: []string{"producer"}}},
	}
	if _, err := repository.PublishOutput(context.Background(), command, time.Unix(721, 0)); err != nil {
		t.Fatal(err)
	}
	before := repository.Stats().Generation
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, resolveErr := repository.Resolve(context.Background(), dynamicOutputTask(), dynamicOutputAgent(), 64)
			errorsSeen <- resolveErr
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for resolveErr := range errorsSeen {
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
	}
	after := repository.Stats().Generation
	if after != before {
		t.Fatalf("concurrent dependency reads changed generation = %d -> %d", before, after)
	}
}

func TestDependencyReadDoesNotRequireAnotherCheckpointSave(t *testing.T) {
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	config := testConfig()
	repository, err := NewPersistent(config, store, true, time.Unix(730, 0))
	if err != nil {
		t.Fatal(err)
	}
	producer, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "producer")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "consumer"); err != nil {
		t.Fatal(err)
	}
	command := OutputCommand{
		TaskID: "produce", WorkflowID: "workflow-1", ProducerAgentID: "producer",
		Targets: []OutputTarget{{AgentID: "producer", ExpectedManifestID: producer.ID,
			TaskTokenBudget: 64, PrincipalPinnedBudget: 64}},
		Artifacts: []ArtifactCommand{{Name: "analysis", Content: "must remain atomic",
			Visibility: VisibilityWorkflow, Targets: []string{"producer"}}},
	}
	if _, err := repository.PublishOutput(context.Background(), command, time.Unix(731, 0)); err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("disk unavailable")
	if result, err := repository.Resolve(context.Background(), dynamicOutputTask(), dynamicOutputAgent(), 64); err != nil || result.Text != "must remain atomic" {
		t.Fatalf("durable dependency read = %+v, error=%v", result, err)
	}
	store.saveErr = nil
	restored, err := NewPersistent(config, store, false, time.Unix(732, 0))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := restored.Inspect("workflow-1", "consumer")
	if err != nil {
		t.Fatal(err)
	}
	record, err := restored.Output("produce")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PageCount != 0 || len(record.Artifacts[0].Targets) != 1 {
		t.Fatalf("failed hydration was published: snapshot=%+v record=%+v", snapshot, record)
	}
}

func TestOutputCapacityStillAllowsExistingMarkerReplay(t *testing.T) {
	config := testConfig()
	config.Limits.MaxLedgerEntries = 1
	repository, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	first := OutputCommand{TaskID: "first", WorkflowID: "workflow-1", ProducerAgentID: "producer"}
	if _, err := repository.PublishOutput(context.Background(), first, time.Unix(740, 0)); err != nil {
		t.Fatal(err)
	}
	second := OutputCommand{TaskID: "second", WorkflowID: "workflow-1", ProducerAgentID: "producer"}
	if _, err := repository.PublishOutput(context.Background(), second, time.Unix(741, 0)); !errors.Is(err, ErrOutputCapacity) {
		t.Fatalf("second marker error = %v, want ErrOutputCapacity", err)
	}
	if _, err := repository.PublishOutput(context.Background(), first, time.Unix(742, 0)); err != nil {
		t.Fatalf("existing marker replay failed at capacity: %v", err)
	}
}
