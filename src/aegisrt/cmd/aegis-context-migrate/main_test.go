package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aegisrt/internal/agentconfig"
	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/statefile"
)

func TestOfflineMigrationPreservesSourcesAndRefusesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	runtimePath, source, output := filepath.Join(dir, "runtime.state"), filepath.Join(dir, "old.context"), filepath.Join(dir, "new.context")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(executable)
	agentsPath := filepath.Join(dir, "agents.json")
	agents := map[string]any{"agents": []map[string]any{{"id": "agent", "name": "Agent", "runtime": "process", "entrypoint": executable,
		"resources": map[string]int{"cpu_milli": 100, "memory_mib": 64}, "max_concurrency": 1, "execution_timeout": "2s"}}}
	writeJSON(t, agentsPath, agents)
	registry, err := agentconfig.Load(agentsPath, agentconfig.Options{AgentRoot: root, StrictPermissions: true})
	if err != nil {
		t.Fatal(err)
	}
	capacity := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 256}
	store, err := statefile.OpenExclusive(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := scheduler.NewDAGDRCFS(capacity, time.Second)
	kernel, _, err := runtimecore.NewPersistent(policy, capacity, "migration-test", store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	task := domain.TaskSpec{ID: "task", WorkflowID: "flow", AgentID: "agent", Goal: "read input", Resources: registry.Agents()[0].Resources, MaxAttempts: 1, Timeout: time.Second, IdempotencyKey: "task"}
	if err := kernel.ApplyPatch([]domain.TaskSpec{task}, time.Now()); err != nil {
		t.Fatal(err)
	}
	runtimePayload, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint runtimecore.Checkpoint
	if err := json.Unmarshal(runtimePayload, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := contextstate.ConfigFingerprint(registry.Agents(), checkpoint.InstanceID, contextstate.DefaultLimits(), "whitespace-v1", "none-v1")
	if err != nil {
		t.Fatal(err)
	}
	config := contextstate.Config{Fingerprint: fingerprint, RuntimeInstanceID: checkpoint.InstanceID, Counter: contextvm.WhitespaceCounter{}, Limits: contextstate.DefaultLimits()}
	sourceStore, err := statefile.OpenExclusive(source)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := contextstate.NewPersistent(config, sourceStore, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	head, _, err := repository.EnsureEmptyWithOwnership("flow", "agent")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.Append(context.Background(), contextstate.AppendCommand{TaskID: "task", WorkflowID: "flow", AgentID: "agent", ExpectedManifestID: head.ID,
		IdempotencyKey: "input", Content: "private migration material", Visibility: contextstate.VisibilityAgent, TaskTokenBudget: 64, PrincipalPinnedBudget: 64}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := sourceStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(payload, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["format_version"] = 1
	delete(legacy, "task_inputs")
	delete(legacy, "counter_identity")
	delete(legacy, "summarizer_identity")
	payload, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := sourceStore.Save(payload); err != nil {
		t.Fatal(err)
	}
	if err := sourceStore.Close(); err != nil {
		t.Fatal(err)
	}
	beforeSource, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuntime, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-source", source, "-runtime-state", runtimePath, "-output", output, "-agents-file", agentsPath, "-agent-root", root}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	for path, before := range map[string][]byte{source: beforeSource, runtimePath: beforeRuntime} {
		after, err := os.ReadFile(path)
		if err != nil || string(before) != string(after) {
			t.Fatalf("source changed: %s: %v", path, err)
		}
	}
	destination, err := statefile.OpenExclusive(output)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := contextstate.NewPersistent(config, destination, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	result, err := restored.Resolve(context.Background(), task, registry.Agents()[0], 64)
	if err != nil || result.Text != "private migration material" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	beforeOutput, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(args); err == nil {
		t.Fatal("existing destination accepted")
	}
	afterOutput, err := os.ReadFile(output)
	if err != nil || string(beforeOutput) != string(afterOutput) {
		t.Fatal("existing output changed")
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
}
