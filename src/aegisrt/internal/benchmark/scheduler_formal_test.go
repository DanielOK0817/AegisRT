package benchmark

import "testing"

func TestFormalSchedulerPlanProducesCompleteS1S2S3Matrix(t *testing.T) {
	plan := NewSchedulerFormalPlan()
	if plan.Profile != SchedulerProfileFormal || len(plan.Seeds) != 30 || plan.Repetitions != 1 {
		t.Fatalf("formal plan = %#v", plan)
	}
	result, err := SimulateScheduler(plan)
	if err != nil {
		t.Fatal(err)
	}
	caseRows := map[string]int{}
	casePolicies := map[string]map[string]int{}
	for _, row := range result.Rows {
		caseRows[row.CaseID]++
		if casePolicies[row.CaseID] == nil {
			casePolicies[row.CaseID] = map[string]int{}
		}
		casePolicies[row.CaseID][row.Policy]++
		if row.EvidenceScope != "windows-functional" || row.Status != "SUCCEEDED" {
			t.Fatalf("invalid formal row: %#v", row)
		}
	}
	if caseRows["S1"] != 120 || caseRows["S2"] != 60 || caseRows["S3"] != 60 {
		t.Fatalf("case rows = %#v", caseRows)
	}
	if casePolicies["S1"]["fifo"] != 60 || casePolicies["S1"]["priority"] != 30 || casePolicies["S1"]["dag-drcfs"] != 30 {
		t.Fatalf("S1 policies = %#v", casePolicies["S1"])
	}
	if casePolicies["S2"]["fifo"] != 30 || casePolicies["S2"]["dag-drcfs"] != 30 {
		t.Fatalf("S2 policies = %#v", casePolicies["S2"])
	}
	if casePolicies["S3"]["priority"] != 30 || casePolicies["S3"]["dag-drcfs"] != 30 {
		t.Fatalf("S3 policies = %#v", casePolicies["S3"])
	}
}

func TestFormalSchedulerCasesExerciseDeclaredAdvantages(t *testing.T) {
	result, err := SimulateScheduler(NewSchedulerFormalPlan())
	if err != nil {
		t.Fatal(err)
	}
	type metrics struct {
		unlock, critical, fairness float64
		starvation                 int
	}
	byPair := map[string]map[string]metrics{}
	for _, row := range result.Rows {
		if byPair[row.PairID] == nil {
			byPair[row.PairID] = map[string]metrics{}
		}
		byPair[row.PairID][row.Policy] = metrics{
			unlock: row.DependencyUnlockMS, critical: row.CriticalPathMS,
			fairness: row.JainFairness, starvation: row.StarvationCount,
		}
	}
	var s1, s2, s3 int
	for _, policies := range byPair {
		if fifo, ok := policies["fifo"]; ok {
			if dag, exists := policies["dag-drcfs"]; exists {
				if dag.fairness > fifo.fairness {
					s2++
				} else if dag.unlock < fifo.unlock {
					s1++
				}
			}
		}
		if priority, ok := policies["priority"]; ok {
			if dag, exists := policies["dag-drcfs"]; exists && dag.critical < priority.critical && dag.starvation < priority.starvation {
				s3++
			}
		}
	}
	if s1 != 30 || s2 != 30 || s3 != 30 {
		t.Fatalf("improved paired cells S1=%d S2=%d S3=%d", s1, s2, s3)
	}
}

func TestFormalSchedulerPlanRejectsProtocolOverrides(t *testing.T) {
	plan := NewSchedulerFormalPlan()
	plan.Seeds = append([]uint32(nil), plan.Seeds[:29]...)
	if _, err := SimulateScheduler(plan); err == nil {
		t.Fatal("formal plan accepted a seed override")
	}
}
