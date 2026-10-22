package contextstate

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/contextdispatch"
	"aegisrt/internal/domain"
)

func TestPublishOutputPersistsEmptyCompletionMarker(t *testing.T) {
	repository, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	command := OutputCommand{
		TaskID: "empty-output", WorkflowID: "workflow-1", ProducerAgentID: "producer",
	}
	result, err := repository.PublishOutput(context.Background(), command, time.Unix(700, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.IdempotentReplay || len(result.Record.Artifacts) != 0 {
		t.Fatalf("unexpected marker result: %+v", result)
	}
	replay, err := repository.PublishOutput(context.Background(), command, time.Unix(701, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay || repository.Stats().OutputCount != 1 {
		t.Fatalf("empty marker replay was not idempotent: result=%+v stats=%+v", replay, repository.Stats())
	}
}

func TestResolveRejectsMissingDependencyOutputMarker(t *testing.T) {
	repository, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "consumer"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Resolve(context.Background(), dynamicOutputTask(), dynamicOutputAgent(), 64); !errors.Is(err, ErrOutputNotFound) {
		t.Fatalf("Resolve error = %v, want ErrOutputNotFound", err)
	}
}

func TestPublishOutputCommitsWholeEnvelopeAtomically(t *testing.T) {
	repository, producerHead, consumerHead := outputRepository(t)
	command := testOutputCommand(producerHead, consumerHead)
	command.Targets[0].TaskTokenBudget = 2
	command.Targets[1].TaskTokenBudget = 2
	command.Artifacts = append(command.Artifacts, ArtifactCommand{
		Name: "oversized", Content: "one two three", Visibility: VisibilityWorkflow,
		Targets: []string{"producer", "consumer"},
	})

	if _, err := repository.PublishOutput(context.Background(), command, time.Now()); !errors.Is(err, ErrPageTokenLimit) {
		t.Fatalf("PublishOutput error = %v, want ErrPageTokenLimit", err)
	}
	assertEmptyOutputManifest(t, repository, "producer", producerHead)
	assertEmptyOutputManifest(t, repository, "consumer", consumerHead)
	if _, err := repository.Output("produce"); !errors.Is(err, ErrOutputNotFound) {
		t.Fatalf("Output error = %v, want ErrOutputNotFound", err)
	}
	if stats := repository.Stats().Store; stats.UniquePages != 0 || stats.PutCalls != 0 {
		t.Fatalf("failed envelope mutated CAS: %+v", stats)
	}
}

func TestResolveRejectsInvalidPrincipalBeforeHydrationCommit(t *testing.T) {
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	config := testConfig()
	repository, err := NewPersistent(config, store, true, time.Unix(600, 0))
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
		Targets: []OutputTarget{{
			AgentID: "producer", ExpectedManifestID: producer.ID,
			TaskTokenBudget: 64, PrincipalPinnedBudget: 64,
		}},
		Artifacts: []ArtifactCommand{{
			Name: "analysis", Content: "must not hydrate invalid request", Visibility: VisibilityWorkflow,
			Targets: []string{"producer"},
		}},
	}
	if _, err := repository.PublishOutput(context.Background(), command, time.Unix(601, 0)); err != nil {
		t.Fatal(err)
	}
	before := repository.Stats().Generation
	wrongAgent := dynamicOutputAgent()
	wrongAgent.ID = "intruder"
	if _, err := repository.Resolve(context.Background(), dynamicOutputTask(), wrongAgent, 64); !errors.Is(err, contextdispatch.ErrTaskAgentMismatch) {
		t.Fatalf("mismatched Resolve error = %v, want ErrTaskAgentMismatch", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.Resolve(canceled, dynamicOutputTask(), dynamicOutputAgent(), 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Resolve error = %v, want context.Canceled", err)
	}
	after := repository.Stats().Generation
	snapshot, err := repository.Inspect("workflow-1", "consumer")
	if err != nil {
		t.Fatal(err)
	}
	if after != before || snapshot.PageCount != 0 {
		t.Fatalf("invalid Resolve mutated Context: generation %d->%d snapshot=%+v", before, after, snapshot)
	}
}

func TestOutputRecordRestoresAndHydratesAfterRestart(t *testing.T) {
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	config := testConfig()
	first, err := NewPersistent(config, store, true, time.Unix(400, 0))
	if err != nil {
		t.Fatal(err)
	}
	producer, _, err := first.EnsureEmptyWithOwnership("workflow-1", "producer")
	if err != nil {
		t.Fatal(err)
	}
	command := OutputCommand{
		TaskID: "produce", WorkflowID: "workflow-1", ProducerAgentID: "producer",
		Targets: []OutputTarget{{
			AgentID: "producer", ExpectedManifestID: producer.ID,
			TaskTokenBudget: 64, PrincipalPinnedBudget: 64,
		}},
		Artifacts: []ArtifactCommand{{
			Name: "analysis", Content: "restart-safe output", Visibility: VisibilityWorkflow,
			Targets: []string{"producer"},
		}},
	}
	if _, err := first.PublishOutput(context.Background(), command, time.Unix(401, 0)); err != nil {
		t.Fatal(err)
	}

	second, err := NewPersistent(config, store, false, time.Unix(402, 0))
	if err != nil {
		t.Fatal(err)
	}
	record, err := second.Output("produce")
	if err != nil || record.Artifacts[0].Name != "analysis" {
		t.Fatalf("restored output = %+v error=%v", record, err)
	}
	if _, _, err := second.EnsureEmptyWithOwnership("workflow-1", "consumer"); err != nil {
		t.Fatal(err)
	}
	resolution, err := second.Resolve(context.Background(), dynamicOutputTask(), dynamicOutputAgent(), 64)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resolution.Text, "restart-safe output") {
		t.Fatalf("restored output was not hydrated: %q", resolution.Text)
	}
}

func TestCheckpointRejectsCorruptOutputProvenance(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OutputRecord)
	}{
		{name: "producer", mutate: func(record *OutputRecord) { record.ProducerAgentID = "intruder" }},
		{name: "page", mutate: func(record *OutputRecord) {
			record.Artifacts[0].PageID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{name: "unreachable delivery target", mutate: func(record *OutputRecord) {
			record.Artifacts[0].Targets = append(record.Artifacts[0].Targets, "ghost")
		}},
		{name: "published time", mutate: func(record *OutputRecord) { record.PublishedAt = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, config := populatedOutputCheckpoint(t)
			stored := decodeStoredCheckpoint(t, store.payload)
			test.mutate(&stored.Outputs[0])
			store.payload = encodeStoredCheckpoint(t, stored)
			if _, err := NewPersistent(config, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
			}
		})
	}
}

func TestResolveHydratesOutputForDynamicDirectDependant(t *testing.T) {
	repository, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	producer, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "producer")
	if err != nil {
		t.Fatal(err)
	}
	command := OutputCommand{
		TaskID: "produce", WorkflowID: "workflow-1", ProducerAgentID: "producer",
		Targets: []OutputTarget{{
			AgentID: "producer", ExpectedManifestID: producer.ID,
			TaskTokenBudget: 64, PrincipalPinnedBudget: 64,
		}},
		Artifacts: []ArtifactCommand{{
			Name: "analysis", Content: "late dependency context", Visibility: VisibilityWorkflow,
			Targets: []string{"producer"},
		}},
	}
	if _, err := repository.PublishOutput(context.Background(), command, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "consumer"); err != nil {
		t.Fatal(err)
	}
	task := dynamicOutputTask()
	agent := dynamicOutputAgent()
	resolution, err := repository.Resolve(context.Background(), task, agent, 64)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resolution.Text, "late dependency context") {
		t.Fatalf("resolved Context omitted output: %q", resolution.Text)
	}
	snapshot, err := repository.Inspect("workflow-1", "consumer")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PageCount != 0 || repository.Stats().Store.PutCalls != 1 {
		t.Fatalf("dependency read copied an already durable page: snapshot=%+v stats=%+v", snapshot, repository.Stats().Store)
	}
}

func TestPublishOutputPersistsProvenanceAndRejectsChangedReplay(t *testing.T) {
	repository, producerHead, consumerHead := outputRepository(t)
	command := testOutputCommand(producerHead, consumerHead)
	result, err := repository.PublishOutput(context.Background(), command, time.Unix(300, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.IdempotentReplay || len(result.Appends) != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	record, err := repository.Output("produce")
	if err != nil {
		t.Fatal(err)
	}
	if record.WorkflowID != "workflow-1" || record.ProducerAgentID != "producer" ||
		len(record.Artifacts) != 1 || record.Artifacts[0].Name != "analysis" ||
		record.Artifacts[0].PageID == "" || record.PublishedAt != time.Unix(300, 0).UTC() {
		t.Fatalf("missing output provenance: %+v", record)
	}

	replayed := command
	replayResult, err := repository.PublishOutput(context.Background(), replayed, time.Unix(301, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !replayResult.IdempotentReplay || len(replayResult.Appends) != 2 ||
		!replayResult.Appends[0].IdempotentReplay || !replayResult.Appends[1].IdempotentReplay {
		t.Fatalf("output replay was not idempotent: %+v", replayResult)
	}

	changed := replayed
	changed.Artifacts = append([]ArtifactCommand(nil), replayed.Artifacts...)
	changed.Artifacts[0].Content = "non deterministic retry"
	if _, err := repository.PublishOutput(context.Background(), changed, time.Unix(302, 0)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestPublishOutputReplayChecksPreconditionForNewDeliveryTarget(t *testing.T) {
	repository, producerHead, consumerHead := outputRepository(t)
	command := testOutputCommand(producerHead, consumerHead)
	command.Targets = command.Targets[:1]
	command.Artifacts[0].Targets = command.Artifacts[0].Targets[:1]
	if _, err := repository.PublishOutput(context.Background(), command, time.Unix(710, 0)); err != nil {
		t.Fatal(err)
	}
	replay := testOutputCommand(producerHead, strings.Repeat("a", 64))
	if _, err := repository.PublishOutput(context.Background(), replay, time.Unix(711, 0)); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("new delivery error = %v, want ErrPreconditionFailed", err)
	}
	snapshot, err := repository.Inspect("workflow-1", "consumer")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ManifestID != consumerHead || snapshot.PageCount != 0 {
		t.Fatalf("failed new delivery mutated consumer: %+v", snapshot)
	}
}

func outputRepository(t *testing.T) (*Repository, string, string) {
	t.Helper()
	repository, err := New(testConfig())
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
	return repository, producer.ID, consumer.ID
}

func populatedOutputCheckpoint(t *testing.T) (*scriptedStore, Config) {
	t.Helper()
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	config := testConfig()
	repository, err := NewPersistent(config, store, true, time.Unix(500, 0))
	if err != nil {
		t.Fatal(err)
	}
	producer, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "producer")
	if err != nil {
		t.Fatal(err)
	}
	command := testOutputCommand(producer.ID, producer.ID)
	command.Targets = command.Targets[:1]
	command.Artifacts[0].Targets = command.Artifacts[0].Targets[:1]
	if _, err := repository.PublishOutput(context.Background(), command, time.Unix(501, 0)); err != nil {
		t.Fatal(err)
	}
	return store, config
}

func testOutputCommand(producerHead, consumerHead string) OutputCommand {
	return OutputCommand{
		TaskID: "produce", WorkflowID: "workflow-1", ProducerAgentID: "producer",
		Targets: []OutputTarget{
			{AgentID: "producer", ExpectedManifestID: producerHead, TaskTokenBudget: 64, PrincipalPinnedBudget: 64},
			{AgentID: "consumer", ExpectedManifestID: consumerHead, TaskTokenBudget: 64, PrincipalPinnedBudget: 64},
		},
		Artifacts: []ArtifactCommand{{
			Name: "analysis", Content: "shared analysis", Visibility: VisibilityWorkflow,
			Targets: []string{"producer", "consumer"},
		}},
	}
}

func assertEmptyOutputManifest(t *testing.T, repository *Repository, agentID, head string) {
	t.Helper()
	snapshot, err := repository.Inspect("workflow-1", agentID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ManifestID != head || snapshot.PageCount != 0 {
		t.Fatalf("manifest %q mutated: %+v", agentID, snapshot)
	}
}

func dynamicOutputTask() domain.TaskSpec {
	return domain.TaskSpec{
		ID: "consume", WorkflowID: "workflow-1", AgentID: "consumer", Goal: "consume output",
		Dependencies: []string{"produce"}, Priority: 1,
		Resources:   domain.ResourceVector{CPUMilli: 100, MemoryMiB: 64, TokenBudget: 64},
		MaxAttempts: 1, Timeout: time.Second, IdempotencyKey: "consume-key",
	}
}

func dynamicOutputAgent() domain.AgentSpec {
	return domain.AgentSpec{
		ID: "consumer", Name: "Consumer", Runtime: "process", Entrypoint: "consumer",
		Resources:      domain.ResourceVector{CPUMilli: 100, MemoryMiB: 64, TokenBudget: 64},
		MaxConcurrency: 1, ExecutionTimeout: time.Second,
	}
}
