package benchmark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectFormalSchedulerPublishesWarmupsMeasurementsAndAnalysis(t *testing.T) {
	parent := t.TempDir()
	runID := "run-999999999999999999999999"
	summary, err := collectScheduler(context.Background(), CollectOptions{
		OutputDir: parent, Plan: NewSchedulerFormalPlan(),
	}, testCollectorDeps(runID))
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowCount != 240 {
		t.Fatalf("formal row count = %d, want 240", summary.RowCount)
	}
	manifestData, err := os.ReadFile(filepath.Join(summary.OutputDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Experiment.PlanID != schedulerFormalPlanID ||
		manifest.Protocol.WarmupPairs != 12 || manifest.Protocol.MeasuredPairs != 120 ||
		len(manifest.Experiment.CaseIDs) != 3 {
		t.Fatalf("formal manifest = %#v", manifest)
	}
	analysisData, err := os.ReadFile(filepath.Join(summary.OutputDir, "scheduler-analysis.json"))
	if err != nil {
		t.Fatal(err)
	}
	var analysis SchedulerAnalysis
	if err := json.Unmarshal(analysisData, &analysis); err != nil {
		t.Fatal(err)
	}
	if err := validateFormalSchedulerAnalysis(analysis); err != nil {
		t.Fatal(err)
	}
	eventData, err := os.ReadFile(filepath.Join(summary.OutputDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var warmupStarted, measurementStarted int
	for _, line := range splitNonemptyLines(eventData) {
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event.EventType == "trial.started" && event.Phase == "warmup" {
			warmupStarted++
		}
		if event.EventType == "trial.started" && event.Phase == "measurement" {
			measurementStarted++
		}
	}
	if warmupStarted != 24 || measurementStarted != 240 {
		t.Fatalf("trial starts warmup=%d measurement=%d", warmupStarted, measurementStarted)
	}
	assertChecksums(t, summary.OutputDir)
}

func splitNonemptyLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for index, value := range data {
		if value != '\n' {
			continue
		}
		if index > start {
			lines = append(lines, data[start:index])
		}
		start = index + 1
	}
	return lines
}
