package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCollectContextPublishesRecomputableAnonymousEvidence(t *testing.T) {
	parent := t.TempDir()
	runID := "run-1234567890abcdef12345678"
	summary, err := collectContext(context.Background(), ContextCollectOptions{
		OutputDir: parent, Plan: ContextPlan{Seeds: []uint32{7}, Repetitions: 1},
	}, testCollectorDeps(runID))
	if err != nil {
		t.Fatal(err)
	}
	if summary.RunID != runID || summary.RowCount != 6 || summary.OutputDir != filepath.Join(parent, runID) {
		t.Fatalf("context summary = %#v", summary)
	}
	wantFiles := []string{
		"context.csv", "context-trial-traces.jsonl", "workload.json", "context-input.json",
		"events.jsonl", "environment.json", "manifest.json", "SHA256SUMS",
	}
	entries, err := os.ReadDir(summary.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(wantFiles) {
		t.Fatalf("published files = %d, want %d", len(entries), len(wantFiles))
	}
	for _, name := range wantFiles {
		data, readErr := os.ReadFile(filepath.Join(summary.OutputDir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if strings.Contains(string(data), parent) || strings.Contains(string(data), "shared deterministic context") ||
			strings.Contains(string(data), "large optional context") {
			t.Fatalf("%s contains a path or raw Context body", name)
		}
	}
	assertContextManifest(t, summary.OutputDir, runID)
	assertContextTraceFile(t, summary.OutputDir, runID)
	assertContextChecksums(t, summary.OutputDir)
}

func TestCollectContextCancellationCreatesNoOutput(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	deps := testCollectorDeps("run-aaaaaaaaaaaaaaaaaaaaaaaa")
	clockCalls := 0
	deps.now = func() time.Time {
		clockCalls++
		if clockCalls == 2 {
			cancel()
		}
		return time.Date(2026, 7, 20, 1, 2, clockCalls, 0, time.UTC)
	}
	_, err := collectContext(ctx, ContextCollectOptions{
		OutputDir: parent, Plan: ContextPlan{Seeds: []uint32{7}, Repetitions: 1},
	}, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collection error = %v, want context canceled", err)
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("canceled Context collection created output: %v / %#v", readErr, entries)
	}
}

func TestCollectContextRetainsStagingWhenPublicationFails(t *testing.T) {
	parent := t.TempDir()
	runID := "run-bbbbbbbbbbbbbbbbbbbbbbbb"
	deps := testCollectorDeps(runID)
	deps.publish = func(_, _ string) error { return errors.New("publish failed") }
	_, err := collectContext(context.Background(), ContextCollectOptions{
		OutputDir: parent, Plan: ContextPlan{Seeds: []uint32{7}, Repetitions: 1},
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "staging retained") {
		t.Fatalf("publication error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(parent, "."+runID+".staging")); statErr != nil {
		t.Fatalf("Context staging not retained: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(parent, runID)); !os.IsNotExist(statErr) {
		t.Fatalf("failed Context staging was published: %v", statErr)
	}
}

func TestContextHeaderMatchesExperimentContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "experiments", "csv", "context-v2.header.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != contextCSVHeader {
		t.Fatal("embedded context header drifted from experiments contract")
	}
}

func assertContextManifest(t *testing.T, dir, runID string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.RunID != runID || manifest.Experiment.Kind != "context" || manifest.Experiment.PlanID != contextPlanID ||
		len(manifest.Experiment.CaseIDs) != 3 || manifest.Experiment.CaseIDs[0] != "C1-smoke" || manifest.Experiment.CaseIDs[1] != "C2-smoke" ||
		manifest.Experiment.CaseIDs[2] != "C3-smoke" || manifest.Protocol.WarmupPairs != 0 || manifest.Protocol.MeasuredPairs != 3 || len(manifest.Artifacts) != 6 {
		t.Fatalf("context manifest = %#v", manifest)
	}
	for name, want := range map[string]string{
		"workload.json":      manifest.Experiment.WorkloadSHA256,
		"context-input.json": manifest.Experiment.InputSHA256,
	} {
		content, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil || hashBytes(content) != want || !json.Valid(content) {
			t.Fatalf("canonical artifact %s does not reproduce hash: %v", name, readErr)
		}
	}
}

func assertContextTraceFile(t *testing.T, dir, runID string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "context-trial-traces.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 6 {
		t.Fatalf("context trace rows = %d, want 6", len(lines))
	}
	traces := make([]ContextTrialTrace, 0, len(lines))
	rows := make([]ContextRow, 0, len(lines))
	for _, line := range lines {
		var trace ContextTrialTrace
		if err := json.Unmarshal([]byte(line), &trace); err != nil {
			t.Fatal(err)
		}
		if trace.RunID != runID {
			t.Fatalf("trace run id = %q", trace.RunID)
		}
		row, deriveErr := contextRowFromTrace(trace)
		if deriveErr != nil {
			t.Fatal(deriveErr)
		}
		traces, rows = append(traces, trace), append(rows, row)
	}
	if err := validateContextEvidencePairs(rows, traces); err != nil {
		t.Fatalf("recompute context evidence: %v", err)
	}
	file, err := os.Open(filepath.Join(dir, "context.csv"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(file).ReadAll()
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read context CSV: %v / %v", err, closeErr)
	}
	if len(records) != len(rows)+1 || strings.Join(records[0], ",") != contextCSVHeader {
		t.Fatalf("context CSV shape/header is invalid")
	}
	for index, row := range rows {
		if !reflect.DeepEqual(records[index+1], row.contextCSVRecord()) {
			t.Fatalf("context CSV row %d cannot be reproduced from trace", index)
		}
	}
}

func assertContextChecksums(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 7 {
		t.Fatalf("checksum rows = %d, want 7", len(lines))
	}
	for _, line := range lines {
		parts := strings.SplitN(line, "  ", 2)
		content, readErr := os.ReadFile(filepath.Join(dir, parts[1]))
		if readErr != nil {
			t.Fatal(readErr)
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != parts[0] {
			t.Fatalf("checksum mismatch for %s", parts[1])
		}
	}
}
