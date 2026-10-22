package benchmark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommunicationPersistentDeliveryAndMeasurement(t *testing.T) {
	for _, operation := range []string{"output_publish", "context_load", "context_hydrate"} {
		t.Run(operation, func(t *testing.T) {
			c := CommunicationCase{Operation: operation, PayloadBytes: 256, Recipients: 3, Concurrency: 2}
			trial, err := runCommunicationTrial(context.Background(), t.TempDir(), c, 4, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !trial.ReplayStable || !trial.ReloadVerified || !trial.IsolationVerified || len(trial.Samples) != 4 {
				t.Fatalf("missing correctness evidence: %+v", trial)
			}
			for _, sample := range trial.Samples {
				if sample.LatencyNS < 0 || sample.DeliveredBytes != 768*trial.Case.ReadPasses {
					t.Fatalf("invalid sample: %+v", sample)
				}
			}
			wantSaves := uint64(4)
			if operation == "context_load" {
				wantSaves = 0
			}
			if operation == "context_hydrate" {
				wantSaves = 0
			}
			if trial.CheckpointSaveCalls != wantSaves {
				t.Fatalf("save calls = %d, want %d", trial.CheckpointSaveCalls, wantSaves)
			}
			if operation == "context_load" && trial.CheckpointBeforeBytes != trial.CheckpointAfterBytes {
				t.Fatal("reading a delivered payload grew the checkpoint")
			}
			if operation == "output_publish" && trial.PhysicalAfterBytes-trial.PhysicalBeforeBytes != 256 {
				t.Fatal("identical payloads were not physically deduplicated")
			}
		})
	}
}

func TestCommunicationRejectsInvalidPlans(t *testing.T) {
	valid := NewCommunicationSmokePlan()
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*CommunicationPlan){
		func(p *CommunicationPlan) { p.PayloadBytes = nil },
		func(p *CommunicationPlan) { p.PayloadBytes = []int{0} },
		func(p *CommunicationPlan) { p.PayloadBytes = []int{1, 1} },
		func(p *CommunicationPlan) { p.PayloadBytes = []int{32769} },
		func(p *CommunicationPlan) { p.Recipients = []int{9} },
		func(p *CommunicationPlan) { p.Concurrency = []int{5} },
		func(p *CommunicationPlan) { p.Operations = 33 },
		func(p *CommunicationPlan) { p.Repetitions = 11 },
	} {
		plan := NewCommunicationSmokePlan()
		mutate(&plan)
		if plan.validate() == nil {
			t.Fatalf("invalid plan accepted: %+v", plan)
		}
	}
}

func TestCommunicationRequiresBuildSourceIdentity(t *testing.T) {
	_, err := CollectCommunication(context.Background(), CommunicationOptions{
		OutputDir: t.TempDir(), Plan: NewCommunicationSmokePlan(),
	})
	if err == nil {
		t.Fatal("missing build source identity accepted")
	}
}

func TestCommunicationReportKeepsRawTrialsAndBuildIdentity(t *testing.T) {
	identity := strings.Repeat("b", 64)
	summary, err := CollectCommunication(context.Background(), CommunicationOptions{OutputDir: t.TempDir(), SourceSHA256: identity,
		Plan: CommunicationPlan{PayloadBytes: []int{16}, Recipients: []int{1}, Concurrency: []int{1}, Operations: 1, Repetitions: 1, ReadPasses: 32}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(summary.OutputDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report CommunicationReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "complete" || report.SourceSHA256 != identity || len(report.Trials) != 3 || summary.RowCount != 3 {
		t.Fatalf("invalid report: %+v", report)
	}
	if report.Software.BinarySHA256 == "" || report.Environment.OSFamily == "" || len(report.Scope) == 0 {
		t.Fatal("missing environment or scope evidence")
	}
}
