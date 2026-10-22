package benchmark

import (
	"encoding/json"
	"math"
	"testing"
)

func TestSimulateSchedulerProducesPairedS1Rows(t *testing.T) {
	result, err := SimulateScheduler(SchedulerPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(result.Rows))
	}
	counts := map[string]int{}
	unlock := map[string]float64{}
	critical := map[string]float64{}
	for _, row := range result.Rows {
		counts[row.Policy]++
		unlock[row.Policy] = row.DependencyUnlockMS
		critical[row.Policy] = row.CriticalPathMS
		if row.Status != "SUCCEEDED" || row.EvidenceScope != "windows-functional" {
			t.Fatalf("unexpected evidence row: %#v", row)
		}
		if row.WorkloadSHA256 != result.WorkloadSHA256 {
			t.Fatalf("row workload hash %q != result %q", row.WorkloadSHA256, result.WorkloadSHA256)
		}
		if row.DecisionCount != 4 || row.TaskCount != 4 || row.WorkflowCount != 1 {
			t.Fatalf("unexpected counts: %#v", row)
		}
		if row.AdmissionRejections != 0 {
			t.Fatalf("temporary slot contention was counted as admission rejection: %#v", row)
		}
	}
	if counts["fifo"] != 2 || counts["priority"] != 1 || counts["dag-drcfs"] != 1 {
		t.Fatalf("policy counts = %#v", counts)
	}
	if !(unlock["priority"] < unlock["fifo"] && unlock["dag-drcfs"] < unlock["fifo"]) {
		t.Fatalf("dependency unlock fifo=%v priority=%v dag=%v", unlock["fifo"], unlock["priority"], unlock["dag-drcfs"])
	}
	if !(critical["priority"] < critical["fifo"] && critical["dag-drcfs"] < critical["fifo"]) {
		t.Fatalf("critical segment fifo=%v priority=%v dag=%v", critical["fifo"], critical["priority"], critical["dag-drcfs"])
	}
	for _, pairID := range []string{"s1-smoke-7-0-priority", "s1-smoke-7-0-dag-drcfs"} {
		roles := map[string]int{}
		orders := map[int]int{}
		for _, row := range result.Rows {
			if row.PairID == pairID {
				roles[row.Role]++
				orders[row.TrialOrder]++
			}
		}
		if roles["baseline"] != 1 || roles["treatment"] != 1 || orders[0] != 1 || orders[1] != 1 {
			t.Fatalf("invalid pair %s: roles=%v orders=%v", pairID, roles, orders)
		}
	}
}

func TestSchedulerWorkloadDeclaresTrueCriticalBranchAndCanonicalBytes(t *testing.T) {
	plan := SchedulerPlan{Seeds: []uint32{7}, Repetitions: 1}
	evidence, err := SimulateSchedulerEvidence(plan)
	if err != nil {
		t.Fatal(err)
	}
	data := evidence.CanonicalWorkloadJSON()
	if !json.Valid(data) || hashBytes(data) != evidence.Result.WorkloadSHA256 {
		t.Fatal("canonical workload bytes do not reproduce workload hash")
	}
	var bundle workloadBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	workload := bundle.Workloads[0]
	durations := map[string]int64{}
	for _, task := range workload.Tasks {
		durations[task.Spec.ID] = task.ServiceMS
	}
	if durations["beta-critical"] <= durations["alpha-aux"] {
		t.Fatalf("beta duration %d is not longer than auxiliary %d", durations["beta-critical"], durations["alpha-aux"])
	}
	if got := durations["root"] + durations["beta-critical"] + durations["join"]; got <= durations["root"]+durations["alpha-aux"]+durations["join"] {
		t.Fatalf("declared critical branch service = %d, want strictly longest", got)
	}
	data[0] ^= 0xff
	if hashBytes(evidence.CanonicalWorkloadJSON()) != evidence.Result.WorkloadSHA256 {
		t.Fatal("canonical workload accessor exposed mutable evidence")
	}
}

func TestSchedulerInputHashCommitsFullProtocol(t *testing.T) {
	plan := SchedulerPlan{Seeds: []uint32{7}, Repetitions: 2}
	profile, err := resolveSchedulerProfile(plan)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := SimulateSchedulerEvidence(plan)
	if err != nil {
		t.Fatal(err)
	}
	contract := buildSchedulerInput(plan, profile, evidence.Result.WorkloadSHA256)
	if contract.PlanID != schedulerPlanID || contract.BundleVersion != workflowShape || contract.Profile != SchedulerProfileSmoke ||
		contract.TrialTimeoutMS != trialTimeoutMS || contract.WorkloadSHA256 != evidence.Result.WorkloadSHA256 ||
		len(contract.Policies) != 3 || contract.Policies[1].AgingIntervalMS != policyAgingMS {
		t.Fatalf("input contract omits protocol fields: %#v", contract)
	}
	want, err := canonicalHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Result.InputSHA256 != want {
		t.Fatalf("input hash = %s, want %s", evidence.Result.InputSHA256, want)
	}
	if hashBytes(evidence.CanonicalInputJSON()) != want {
		t.Fatal("canonical input bytes do not reproduce input hash")
	}
	mutated := contract
	mutated.Policies = append([]schedulerPolicyInput(nil), contract.Policies...)
	mutated.Policies[1].AgingIntervalMS++
	changed, err := canonicalHash(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if changed == want {
		t.Fatal("policy parameter change did not change input hash")
	}
}

func TestSimulateSchedulerRejectsInvalidPlan(t *testing.T) {
	tests := []SchedulerPlan{
		{},
		{Seeds: []uint32{1}, Repetitions: 0},
		{Seeds: []uint32{1, 1}, Repetitions: 1},
		{Seeds: []uint32{1, 2}, Repetitions: 65},
	}
	for _, plan := range tests {
		if _, err := SimulateScheduler(plan); err == nil {
			t.Fatalf("SimulateScheduler(%#v) succeeded", plan)
		}
	}
}

func TestValidateSchedulerRowsRejectsInvalidEvidence(t *testing.T) {
	result, err := SimulateScheduler(SchedulerPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	valid := append([]SchedulerRow(nil), result.Rows...)
	for index := range valid {
		valid[index].RunID = "run-valid"
	}
	if err := validateSchedulerRows(valid); err != nil {
		t.Fatalf("valid rows rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func([]SchedulerRow) []SchedulerRow
	}{
		{name: "incomplete pair", mutate: func(rows []SchedulerRow) []SchedulerRow { return rows[:len(rows)-1] }},
		{name: "non-finite metric", mutate: func(rows []SchedulerRow) []SchedulerRow { rows[0].MakespanMS = math.NaN(); return rows }},
		{name: "negative count", mutate: func(rows []SchedulerRow) []SchedulerRow { rows[0].AdmissionRejections = -1; return rows }},
		{name: "invalid hash", mutate: func(rows []SchedulerRow) []SchedulerRow { rows[0].WorkloadSHA256 = "bad"; return rows }},
		{name: "duplicate order", mutate: func(rows []SchedulerRow) []SchedulerRow { rows[1].TrialOrder = rows[0].TrialOrder; return rows }},
		{name: "controlled input drift", mutate: func(rows []SchedulerRow) []SchedulerRow { rows[1].CapacityTokens++; return rows }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := append([]SchedulerRow(nil), valid...)
			if err := validateSchedulerRows(test.mutate(rows)); err == nil {
				t.Fatal("invalid evidence was accepted")
			}
		})
	}
}
