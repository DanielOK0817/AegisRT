package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCollectFaultPublishesRecomputableAnonymousEvidence(t *testing.T) {
	parent := t.TempDir()
	runID := "run-fedcba0987654321fedcba09"
	summary, err := collectFault(context.Background(), FaultCollectOptions{
		OutputDir: parent,
		Plan:      FaultPlan{Seeds: []uint32{7}, Repetitions: 1},
		Agent:     testFaultAgentCommand(),
	}, testCollectorDeps(runID))
	if err != nil {
		t.Fatal(err)
	}
	if summary.RunID != runID || summary.RowCount != 6 || summary.OutputDir != filepath.Join(parent, runID) {
		t.Fatalf("fault summary = %#v", summary)
	}
	wantFiles := []string{
		"fault.csv", "fault-trial-traces.jsonl", "workload.json", "fault-input.json",
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
		if strings.Contains(string(data), parent) || strings.Contains(string(data), testFaultAgentCommand().Entrypoint) ||
			strings.Contains(string(data), "deterministic fault fixture result") {
			t.Fatalf("%s contains a path or raw Agent output", name)
		}
	}
	assertFaultManifest(t, summary.OutputDir, runID)
	assertFaultTraceFile(t, summary.OutputDir, runID)
	assertFaultChecksums(t, summary.OutputDir)
}

func TestFaultHeaderMatchesExperimentContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "experiments", "csv", "fault-v1.header.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != faultCSVHeader {
		t.Fatal("embedded fault header drifted from experiments contract")
	}
}

func assertFaultManifest(t *testing.T, dir, runID string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.RunID != runID || manifest.Experiment.Kind != "fault" || manifest.Experiment.PlanID != faultPlanID ||
		len(manifest.Experiment.CaseIDs) != 3 || manifest.Protocol.MeasuredPairs != 3 || len(manifest.Artifacts) != 6 {
		t.Fatalf("fault manifest = %#v", manifest)
	}
	for name, want := range map[string]string{
		"workload.json":    manifest.Experiment.WorkloadSHA256,
		"fault-input.json": manifest.Experiment.InputSHA256,
	} {
		content, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil || hashBytes(content) != want || !json.Valid(content) {
			t.Fatalf("canonical artifact %s does not reproduce hash: %v", name, readErr)
		}
	}
}

func assertFaultTraceFile(t *testing.T, dir, runID string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "fault-trial-traces.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 6 {
		t.Fatalf("fault trace rows = %d, want 6", len(lines))
	}
	traces := make([]FaultTrialTrace, 0, len(lines))
	rows := make([]FaultRow, 0, len(lines))
	for _, line := range lines {
		var trace FaultTrialTrace
		if err := json.Unmarshal([]byte(line), &trace); err != nil {
			t.Fatal(err)
		}
		if trace.RunID != runID {
			t.Fatalf("trace run id = %q", trace.RunID)
		}
		row, deriveErr := faultRowFromTrace(trace)
		if deriveErr != nil {
			t.Fatal(deriveErr)
		}
		traces, rows = append(traces, trace), append(rows, row)
	}
	if err := validateFaultEvidencePairs(rows, traces); err != nil {
		t.Fatalf("recompute fault evidence: %v", err)
	}
	file, err := os.Open(filepath.Join(dir, "fault.csv"))
	if err != nil {
		t.Fatal(err)
	}
	records, readErr := csv.NewReader(file).ReadAll()
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read fault CSV: %v / %v", readErr, closeErr)
	}
	if len(records) != len(rows)+1 || strings.Join(records[0], ",") != faultCSVHeader {
		t.Fatalf("fault CSV shape/header is invalid")
	}
	for index, row := range rows {
		if !reflect.DeepEqual(records[index+1], row.faultCSVRecord()) {
			t.Fatalf("fault CSV row %d cannot be reproduced from trace", index)
		}
	}
}

func assertFaultChecksums(t *testing.T, dir string) {
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
