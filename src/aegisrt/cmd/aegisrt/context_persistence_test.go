package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/domain"
	"aegisrt/internal/statefile"
)

func TestBuiltDaemonFailsClosedWhenRuntimeTaskHasNoContextHead(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.kernel.ApplyPatch([]domain.TaskSpec{recoveryTestTask()}, time.Now().UTC()); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}

	second, err := buildDaemon(options)
	if second != nil {
		_ = second.close()
	}
	if !errors.Is(err, contextstate.ErrMigrationRequired) {
		t.Fatalf("buildDaemon error = %v, want ErrMigrationRequired", err)
	}
}

func TestBuiltDaemonExplicitlyMigratesLegacyRuntimeAndEmptyContext(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.kernel.ApplyPatch([]domain.TaskSpec{recoveryTestTask()}, time.Now().UTC()); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	downgradeRuntimeCheckpointToV1(t, options.stateFile)
	if err := os.Rename(options.stateFile+".context", options.stateFile+".context.pre-migration"); err != nil {
		t.Fatal(err)
	}

	options.migrateLegacyState = true
	second, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.close(); err != nil {
			t.Errorf("close migrated daemon: %v", err)
		}
	}()
	snapshot, err := second.contexts.Inspect("workflow", "test-agent")
	if err != nil || snapshot.ManifestID == "" || snapshot.PageCount != 0 {
		t.Fatalf("migrated Context = (%+v, %v), want empty durable head", snapshot, err)
	}
	if !second.recovery.Migrated {
		t.Fatalf("recovery report = %+v, want explicit legacy migration", second.recovery)
	}
}

func downgradeRuntimeCheckpointToV1(t *testing.T, statePath string) {
	t.Helper()
	rewriteRuntimeCheckpoint(t, statePath, func(document map[string]json.RawMessage) {
		delete(document, "instance_id")
		delete(document, "legacy_context_migration_pending")
		document["format_version"] = json.RawMessage("1")
	})
}

func rewriteRuntimeCheckpoint(
	t *testing.T,
	statePath string,
	mutate func(map[string]json.RawMessage),
) {
	t.Helper()
	store, err := statefile.OpenExclusive(statePath)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := store.Load()
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	mutate(document)
	payload, err = json.Marshal(document)
	if err == nil {
		err = store.Save(payload)
	}
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrationFlagCannotRecreateContextForNormalV2Runtime(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.kernel.ApplyPatch([]domain.TaskSpec{recoveryTestTask()}, time.Now().UTC()); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(options.stateFile+".context", options.stateFile+".context.unavailable"); err != nil {
		t.Fatal(err)
	}
	options.migrateLegacyState = true
	reopened, err := buildDaemon(options)
	if reopened != nil {
		_ = reopened.close()
	}
	if !errors.Is(err, contextstate.ErrMigrationRequired) {
		t.Fatalf("buildDaemon error = %v, want fail-closed ErrMigrationRequired", err)
	}
}

func TestExistingEmptyV2RuntimeCannotSilentlyRecreateMissingContext(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(options.stateFile+".context", options.stateFile+".context.unavailable"); err != nil {
		t.Fatal(err)
	}
	options.migrateLegacyState = true
	reopened, err := buildDaemon(options)
	if reopened != nil {
		_ = reopened.close()
	}
	if !errors.Is(err, contextstate.ErrMigrationRequired) {
		t.Fatalf("buildDaemon error = %v, want fail-closed ErrMigrationRequired", err)
	}
}

func TestPendingMigrationResumesAfterContextCommitBeforeRuntimeClear(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := first.contexts.EnsureEmptyWithOwnership("workflow", "test-agent")
	if err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if err := first.kernel.ApplyPatch([]domain.TaskSpec{recoveryTestTask()}, time.Now().UTC()); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	rewriteRuntimeCheckpoint(t, options.stateFile, func(document map[string]json.RawMessage) {
		document["legacy_context_migration_pending"] = json.RawMessage("true")
	})

	options.migrateLegacyState = true
	second, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.close(); err != nil {
			t.Errorf("close resumed daemon: %v", err)
		}
	}()
	restored, err := second.contexts.Get("workflow", "test-agent")
	if err != nil || restored.ID != manifest.ID {
		t.Fatalf("restored Context head = (%+v, %v), want %q", restored, err, manifest.ID)
	}
	if second.kernel.LegacyContextMigrationPending() {
		t.Fatal("Runtime migration marker remained pending after durable Context validation")
	}
}

func TestBuiltDaemonRejectsRestoredPinnedContextAboveTaskBudget(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := first.contexts.EnsureEmptyWithOwnership("workflow", "test-agent")
	if err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if _, err := first.contexts.Append(context.Background(), contextstate.AppendCommand{
		TaskID: "orphan", WorkflowID: "workflow", AgentID: "test-agent",
		ExpectedManifestID: manifest.ID, IdempotencyKey: "pinned-before-task",
		Content: "one two", Visibility: contextstate.VisibilityWorkflow, Pin: true,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	}, time.Now().UTC()); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	task := recoveryTestTask()
	task.Resources.TokenBudget = 1
	if err := first.kernel.ApplyPatch([]domain.TaskSpec{task}, time.Now().UTC()); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}

	second, err := buildDaemon(options)
	if second != nil {
		_ = second.close()
	}
	if !errors.Is(err, contextstate.ErrInvalidCheckpoint) {
		t.Fatalf("buildDaemon error = %v, want ErrInvalidCheckpoint", err)
	}
}

func provisionRecoveryContext(t *testing.T, app *daemonApp) {
	t.Helper()
	if _, _, err := app.contexts.EnsureEmptyWithOwnership("workflow", "test-agent"); err != nil {
		_ = app.close()
		t.Fatal(err)
	}
}

func TestContextFingerprintBindsRuntimeInstanceAndLimits(t *testing.T) {
	agents := []domain.AgentSpec{{
		ID: "test-agent", Name: "Test Agent", Runtime: "process", Entrypoint: "agent",
		MaxConcurrency: 1, ExecutionTimeout: time.Second,
	}}
	limits := contextstate.DefaultLimits()
	first, err := contextConfigFingerprint(agents, strings.Repeat("a", 64), limits, contextCounterIdentity, contextSummarizerIdentity)
	if err != nil {
		t.Fatal(err)
	}
	otherRuntime, err := contextConfigFingerprint(agents, strings.Repeat("b", 64), limits, contextCounterIdentity, contextSummarizerIdentity)
	if err != nil {
		t.Fatal(err)
	}
	changedLimits := limits
	changedLimits.MaxManifestPages--
	otherLimits, err := contextConfigFingerprint(agents, strings.Repeat("a", 64), changedLimits, contextCounterIdentity, contextSummarizerIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if first == otherRuntime || first == otherLimits {
		t.Fatal("Context fingerprint did not bind Runtime instance and all limits")
	}
}

func TestBuiltDaemonRejectsContextCheckpointFromAnotherRuntime(t *testing.T) {
	parse := func(directory string) daemonOptions {
		t.Helper()
		var stderr bytes.Buffer
		options, err := parseOptions(daemonArgsInDirectory(t, "success", "127.0.0.1:0", directory), &stderr)
		if err != nil {
			t.Fatal(err)
		}
		return options
	}
	firstOptions := parse(t.TempDir())
	secondOptions := parse(t.TempDir())
	first, err := buildDaemon(firstOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	second, err := buildDaemon(secondOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.close(); err != nil {
		t.Fatal(err)
	}
	foreignContext, err := os.ReadFile(firstOptions.stateFile + ".context")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondOptions.stateFile+".context", foreignContext, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := buildDaemon(secondOptions)
	if reopened != nil {
		_ = reopened.close()
	}
	if !errors.Is(err, contextstate.ErrInvalidCheckpoint) {
		t.Fatalf("buildDaemon error = %v, want ErrInvalidCheckpoint", err)
	}
}

func recoveryTestTask() domain.TaskSpec {
	return domain.TaskSpec{
		ID: "orphan", WorkflowID: "workflow", AgentID: "test-agent",
		Goal: "verify fail-closed recovery", Priority: 1,
		Resources:   domain.ResourceVector{CPUMilli: 100, MemoryMiB: 64},
		MaxAttempts: 2, Timeout: time.Second, IdempotencyKey: "orphan-recovery",
	}
}
