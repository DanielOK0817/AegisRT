package benchmark

import (
	"reflect"
	"testing"
)

func TestFormalSchedulerAnalysisIsPairedDeterministicAndPositive(t *testing.T) {
	result, err := SimulateScheduler(NewSchedulerFormalPlan())
	if err != nil {
		t.Fatal(err)
	}
	first, err := analyzeSchedulerRows(schedulerFormalPlanID, result.Rows)
	if err != nil {
		t.Fatal(err)
	}
	second, err := analyzeSchedulerRows(schedulerFormalPlanID, result.Rows)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("formal analysis is not deterministic")
	}
	wantPositive := map[string]bool{
		"S1/dag-drcfs/dependency_unlock_ms": false,
		"S2/dag-drcfs/jain_fairness":        false,
		"S3/dag-drcfs/critical_path_ms":     false,
		"S3/dag-drcfs/starvation_count":     false,
	}
	for _, cell := range first.Cells {
		for _, metric := range cell.Metrics {
			key := cell.CaseID + "/" + cell.Treatment + "/" + metric.Name
			if _, exists := wantPositive[key]; exists {
				if metric.PairedN != 30 || metric.ImprovementMedian <= 0 || metric.ImprovementCILower <= 0 || metric.RankBiserial <= 0 {
					t.Fatalf("%s lacks positive paired evidence: %#v", key, metric)
				}
				wantPositive[key] = true
			}
		}
	}
	for key, found := range wantPositive {
		if !found {
			t.Fatalf("analysis metric %s is missing", key)
		}
	}
}

func TestSchedulerAnalysisRejectsIncompletePair(t *testing.T) {
	result, err := SimulateScheduler(NewSchedulerFormalPlan())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := analyzeSchedulerRows(schedulerFormalPlanID, result.Rows[:len(result.Rows)-1]); err == nil {
		t.Fatal("analysis accepted an incomplete pair")
	}
}
