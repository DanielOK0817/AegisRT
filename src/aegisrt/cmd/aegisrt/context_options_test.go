package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/domain"
)

func TestLocalContextOptionsRejectUnknownImplementations(t *testing.T) {
	for _, args := range [][]string{
		{"-context-counter", "remote"}, {"-context-compression", "remote"},
	} {
		args = append(args, "-ephemeral", "-agents-file", "agents.json", "-auth-token-file", "token")
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("unsupported options accepted: %v", args)
		}
	}
}

func TestDaemonPersistentLocalCompressionAndCounterIdentity(t *testing.T) {
	options := daemonOptions{stateFile: filepath.Join(t.TempDir(), "runtime-state"), contextCounter: "unicode-segment-v1", contextCompression: "deterministic-extractive-v1"}
	instanceID := strings.Repeat("b", 64)
	repository, store, err := buildContextRepository(options, nil, nil, instanceID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	head, _, err := repository.EnsureEmptyWithOwnership("workflow", "agent")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.Append(context.Background(), contextstate.AppendCommand{
		TaskID: "task", WorkflowID: "workflow", AgentID: "agent", ExpectedManifestID: head.ID,
		IdempotencyKey: "input", Content: "中文预算来源", Visibility: contextstate.VisibilityAgent,
		Cold: true, TaskTokenBudget: 32, PrincipalPinnedBudget: 32,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	task := domain.TaskSpec{ID: "task", WorkflowID: "workflow", AgentID: "agent", Goal: "local test", MaxAttempts: 1, Timeout: time.Second, IdempotencyKey: "task-key", Resources: domain.ResourceVector{TokenBudget: 2}}
	agent := domain.AgentSpec{ID: "agent", Name: "agent", Runtime: "process", Entrypoint: "unused", MaxConcurrency: 1, ExecutionTimeout: time.Second}
	resolved, err := repository.Resolve(context.Background(), task, agent, 2)
	if err != nil || resolved.Text != "中文" || repository.Stats().SummaryCount != 1 {
		t.Fatalf("resolution=%+v stats=%+v error=%v", resolved, repository.Stats(), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restored, reopened, err := buildContextRepository(options, nil, nil, instanceID, false, false)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restored.Resolve(context.Background(), task, agent, 2)
	if err != nil || replayed.Text != resolved.Text || restored.Stats().CounterIdentity != "unicode-segment-v1" {
		t.Fatalf("restore resolution=%+v error=%v", replayed, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	options.contextCounter = "whitespace-v1"
	if _, wrongStore, err := buildContextRepository(options, nil, nil, instanceID, false, false); err == nil {
		_ = wrongStore.Close()
		t.Fatal("counter identity change accepted persisted counts")
	}
}
