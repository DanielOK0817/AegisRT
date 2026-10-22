package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectSchedulerSealsStrictArtifacts(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "operator-alice-workspace")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-0123456789abcdef01234567"
	target := filepath.Join(parent, runID)
	clock := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	deps := collectorDeps{
		now:   func() time.Time { clock = clock.Add(time.Millisecond); return clock },
		runID: func() (string, error) { return runID, nil },
		environment: func() (Environment, error) {
			return testEnvironment(), nil
		},
		software: func(configHash string) (Software, error) {
			return Software{RuntimeCommit: strings.Repeat("a", 40), RuntimeDirty: true, GoVersion: "go1.23.0", BinarySHA256: strings.Repeat("b", 64), ConfigSHA256: configHash}, nil
		},
	}
	summary, err := collectScheduler(context.Background(), CollectOptions{
		OutputDir: parent, Plan: SchedulerPlan{Seeds: []uint32{7}, Repetitions: 1},
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RunID != runID || summary.RowCount != 4 || summary.OutputDir != target {
		t.Fatalf("summary = %#v", summary)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != runID {
		t.Fatalf("published leaves = %#v, want only anonymous run id", entries)
	}
	for _, name := range []string{
		"scheduler.csv", "scheduler-task-traces.jsonl", "workload.json", "scheduler-input.json",
		"events.jsonl", "environment.json", "manifest.json", "SHA256SUMS",
	} {
		path := filepath.Join(target, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), parent) || strings.Contains(string(data), filepath.Base(parent)) {
			t.Fatalf("%s contains the user-selected parent path", name)
		}
	}
	assertSchedulerHeader(t, filepath.Join(target, "scheduler.csv"))
	assertManifest(t, filepath.Join(target, "manifest.json"))
	assertEvents(t, filepath.Join(target, "events.jsonl"), summary.RunID)
	assertSchedulerTaskTraces(t, filepath.Join(target, "scheduler-task-traces.jsonl"), summary.RunID)
	assertCanonicalInputArtifacts(t, target)
	assertChecksums(t, target)
}

func TestCollectSchedulerRefusesExistingTarget(t *testing.T) {
	parent := t.TempDir()
	runID := "run-aaaaaaaaaaaaaaaaaaaaaaaa"
	target := filepath.Join(parent, runID)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	deps := testCollectorDeps(runID)
	_, err := collectScheduler(context.Background(), CollectOptions{
		OutputDir: parent, Plan: SchedulerPlan{Seeds: []uint32{1}, Repetitions: 1},
	}, deps)
	if err == nil {
		t.Fatal("CollectScheduler overwrote an existing target")
	}
	if _, statErr := os.Stat(filepath.Join(parent, "."+runID+".staging")); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected staging after target rejection: %v", statErr)
	}
}

func TestCollectSchedulerRefusesExistingStaging(t *testing.T) {
	parent := t.TempDir()
	runID := "run-bbbbbbbbbbbbbbbbbbbbbbbb"
	staging := filepath.Join(parent, "."+runID+".staging")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := collectScheduler(context.Background(), CollectOptions{
		OutputDir: parent, Plan: SchedulerPlan{Seeds: []uint32{1}, Repetitions: 1},
	}, testCollectorDeps(runID))
	if err == nil {
		t.Fatal("CollectScheduler reused existing staging")
	}
	if _, statErr := os.Stat(filepath.Join(parent, runID)); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected published target: %v", statErr)
	}
}

func TestCollectSchedulerRejectsNonAnonymousRunID(t *testing.T) {
	parent := t.TempDir()
	for _, runID := range []string{"named-run", "run-ABCDEF0123456789abcdef01", "run-../0123456789abcdef012345"} {
		_, err := collectScheduler(context.Background(), CollectOptions{
			OutputDir: parent, Plan: SchedulerPlan{Seeds: []uint32{1}, Repetitions: 1},
		}, testCollectorDeps(runID))
		if err == nil {
			t.Fatalf("accepted unsafe run id %q", runID)
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid run ids created entries: %#v", entries)
	}
}

func TestCollectSchedulerRetainsStagingWhenPublicationFails(t *testing.T) {
	parent := t.TempDir()
	runID := "run-cccccccccccccccccccccccc"
	deps := testCollectorDeps(runID)
	deps.publish = func(_, _ string) error { return context.Canceled }
	_, err := collectScheduler(context.Background(), CollectOptions{
		OutputDir: parent, Plan: SchedulerPlan{Seeds: []uint32{1}, Repetitions: 1},
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "staging retained") {
		t.Fatalf("publication error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(parent, "."+runID+".staging")); statErr != nil {
		t.Fatalf("staging not retained: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(parent, runID)); !os.IsNotExist(statErr) {
		t.Fatalf("failed staging was published: %v", statErr)
	}
}

func TestPublishedArtifactMutationIsDetectedByRetainedChecksum(t *testing.T) {
	parent := t.TempDir()
	runID := "run-dddddddddddddddddddddddd"
	summary, err := collectScheduler(context.Background(), CollectOptions{
		OutputDir: parent, Plan: SchedulerPlan{Seeds: []uint32{1}, Repetitions: 1},
	}, testCollectorDeps(runID))
	if err != nil {
		t.Fatal(err)
	}
	checksumData, err := os.ReadFile(filepath.Join(summary.OutputDir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	var baseline string
	for _, line := range strings.Split(strings.TrimSpace(string(checksumData)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) == 2 && parts[1] == "scheduler.csv" {
			baseline = parts[0]
		}
	}
	if baseline == "" {
		t.Fatal("scheduler.csv checksum is missing")
	}
	artifactPath := filepath.Join(summary.OutputDir, "scheduler.csv")
	if err := os.WriteFile(artifactPath, []byte("owner-modified\n"), 0o600); err != nil {
		t.Fatalf("published artifact should remain owner-writable: %v", err)
	}
	modified, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(modified)
	if hex.EncodeToString(sum[:]) == baseline {
		t.Fatal("artifact mutation was not detectable against retained checksum")
	}
}

func TestSchedulerHeaderMatchesExperimentContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "experiments", "csv", "scheduler-v1.header.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != schedulerCSVHeader {
		t.Fatal("embedded scheduler header drifted from experiments contract")
	}
}

func assertSchedulerHeader(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if first != schedulerCSVHeader {
		t.Fatalf("header mismatch\n got: %s\nwant: %s", first, schedulerCSVHeader)
	}
}

func assertManifest(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != "aegisrt.experiment.manifest/v1" || manifest.Status != "COMPLETED" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	if manifest.Experiment.PlanID != schedulerPlanID || len(manifest.Experiment.CaseIDs) != 1 ||
		manifest.Experiment.CaseIDs[0] != "S1-smoke" || manifest.Protocol.WarmupPairs != 0 {
		t.Fatalf("manifest must remain explicitly smoke-only: %#v", manifest)
	}
	if manifest.Environment.EvidenceScope != "windows-functional" || manifest.Environment.OSFamily != "windows" {
		t.Fatalf("manifest environment = %#v", manifest.Environment)
	}
	if len(manifest.Artifacts) != 6 || manifest.Anonymization.ScanResult != "PASSED" {
		t.Fatalf("manifest artifacts/anonymization = %#v", manifest)
	}
	if manifest.Integrity.DirectoryPolicy != "create-only-atomic-publish" ||
		!manifest.Integrity.TamperEvident || manifest.Integrity.WORMStorage {
		t.Fatalf("manifest integrity overclaims guarantees: %#v", manifest.Integrity)
	}
	for _, artifact := range manifest.Artifacts {
		content, err := os.ReadFile(filepath.Join(filepath.Dir(path), artifact.Path))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		if artifact.Bytes != int64(len(content)) || artifact.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("manifest artifact mismatch: %#v", artifact)
		}
	}
}

func assertSchedulerTaskTraces(t *testing.T, path, runID string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 16 {
		t.Fatalf("task trace records = %d, want 16", len(lines))
	}
	groups := make(map[string][]SchedulerTaskTrace)
	for _, line := range lines {
		var record SchedulerTaskTrace
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.RunID != runID {
			t.Fatalf("task trace run_id = %q, want %q", record.RunID, runID)
		}
		key := record.PairID + "/" + record.Policy
		groups[key] = append(groups[key], record)
	}
	if len(groups) != 4 {
		t.Fatalf("task trace trial groups = %d, want 4", len(groups))
	}
	for key, records := range groups {
		if _, err := AggregateSchedulerTaskTraces(records); err != nil {
			t.Fatalf("recompute %s: %v", key, err)
		}
	}
}

func assertCanonicalInputArtifacts(t *testing.T, dir string) {
	t.Helper()
	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"workload.json":        manifest.Experiment.WorkloadSHA256,
		"scheduler-input.json": manifest.Experiment.InputSHA256,
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(data) || hashBytes(data) != want {
			t.Fatalf("%s does not reproduce manifest hash", name)
		}
	}
}

func assertEvents(t *testing.T, path, runID string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var previousMonotonic int64
	for index, line := range lines {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.SchemaVersion != eventSchemaV1 || event.RunID != runID || event.Sequence != index || event.RecordedAt.IsZero() || event.MonotonicNS < 0 {
			t.Fatalf("invalid event %d: %#v", index, event)
		}
		if index > 0 && event.MonotonicNS < previousMonotonic {
			t.Fatalf("event %d monotonic_ns=%d precedes %d", index, event.MonotonicNS, previousMonotonic)
		}
		previousMonotonic = event.MonotonicNS
	}
	if len(lines) != 11 || lines[0] == "" {
		t.Fatalf("event records = %d, want 11", len(lines))
	}
}

func assertChecksums(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if want := len(manifest.Artifacts) + 1; len(lines) != want {
		t.Fatalf("checksum lines = %d, want %d", len(lines), want)
	}
	for _, line := range lines {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid checksum line %q", line)
		}
		content, err := os.ReadFile(filepath.Join(dir, parts[1]))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != parts[0] {
			t.Fatalf("checksum mismatch for %s", parts[1])
		}
	}
}

func testEnvironment() Environment {
	return Environment{
		EvidenceScope: "windows-functional", OSFamily: "windows", DistributionID: "windows",
		DistributionVersion: "test", KernelRelease: "test", Architecture: "amd64",
		LogicalCPUCount: 4, MemoryTotalBytes: 8 << 30,
		Cgroup: CgroupEnvironment{Version: "none", Controllers: []string{}, Delegation: "none", PSI: "unavailable"},
	}
}

func testCollectorDeps(runID string) collectorDeps {
	clock := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	return collectorDeps{
		now:   func() time.Time { clock = clock.Add(time.Millisecond); return clock },
		runID: func() (string, error) { return runID, nil },
		environment: func() (Environment, error) {
			return testEnvironment(), nil
		},
		software: func(configHash string) (Software, error) {
			return Software{RuntimeCommit: strings.Repeat("a", 40), RuntimeDirty: true, GoVersion: "go1.23.0", BinarySHA256: strings.Repeat("b", 64), ConfigSHA256: configHash}, nil
		},
	}
}
