package benchmark

import (
	"bytes"
	"strings"
	"testing"
)

func TestResourceKernelSmokePlanIsFixed(t *testing.T) {
	plan := NewResourceKernelSmokePlan()
	if err := ValidateResourceKernelPlan(plan); err != nil {
		t.Fatal(err)
	}
	if plan.Profile != ResourceKernelProfileSmoke || len(plan.CaseIDs) != 3 || len(plan.Seeds) != 1 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	changed := plan
	changed.TrialTimeoutMS++
	if err := ValidateResourceKernelPlan(changed); err == nil {
		t.Fatal("changed fixed plan was accepted")
	}
}

func TestWriteResourceKernelCSVUsesContractHeader(t *testing.T) {
	row := ResourceKernelRow{SchemaVersion: ResourceKernelCSVSchemaV2, RunID: "run-0123456789abcdef01234567",
		Status: "SUCCEEDED", EvidenceScope: ResourceKernelEvidenceScope, CaseID: "R2", PairID: "pair-r2-104729-000",
		Seed: 104729, Treatment: "cpu-quota", Role: "treatment", WorkloadSHA256: strings.Repeat("a", 64),
		Backend: "cgroup-v2", Controller: "cpu", LimitKind: "cpu.max", RequestedLimit: 250,
		ObservedValue: 250, ObservationStatistic: "mean_over_trial", Unit: "millicpu",
		ThrottleEvents: 1, ContainmentResult: "PASSED", TimeoutMS: 5000,
		ReasonCode: "NONE"}
	var output bytes.Buffer
	if err := writeResourceKernelCSV(&output, []ResourceKernelRow{row}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.String(), resourceKernelCSVHeader+"\n") {
		t.Fatalf("unexpected CSV header: %s", output.String())
	}
}

func TestResourceKernelCSVRejectsUnknownStatistic(t *testing.T) {
	row := ResourceKernelRow{SchemaVersion: ResourceKernelCSVSchemaV2, RunID: "run-0123456789abcdef01234567",
		Status: "SUCCEEDED", EvidenceScope: ResourceKernelEvidenceScope, CaseID: "R2", PairID: "pair-r2-104729-000",
		Seed: 104729, Treatment: "cpu.max", Role: "treatment", WorkloadSHA256: strings.Repeat("a", 64),
		Backend: "cgroup-v2", Controller: "cpu", LimitKind: "cpu.max", RequestedLimit: 250,
		ObservedValue: 250, ObservationStatistic: "instantaneous", Unit: "millicpu",
		ThrottleEvents: 1, ContainmentResult: "PASSED", TimeoutMS: 5000, ReasonCode: "NONE"}
	if err := writeResourceKernelCSV(&bytes.Buffer{}, []ResourceKernelRow{row}); err == nil {
		t.Fatal("unknown observation statistic was accepted")
	}
}
