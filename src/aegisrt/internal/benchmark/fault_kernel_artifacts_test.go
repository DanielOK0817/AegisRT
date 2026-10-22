package benchmark

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildFaultKernelFormalEvidenceSeparatesFixedWarmups(t *testing.T) {
	plan, err := NewFaultKernelFormalPlan(FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := buildFaultKernelExperimentEvidence(faultKernelTestBundle(t, plan))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Protocol.WarmupPairs != len(FaultKernelFormalWarmupSeeds()) ||
		evidence.Protocol.MeasuredPairs != len(FaultKernelFormalMeasuredSeeds()) ||
		evidence.RowCount != len(FaultKernelFormalMeasuredSeeds())*2 {
		t.Fatalf("formal counts = protocol %#v, rows %d", evidence.Protocol, evidence.RowCount)
	}
	warmup := findFaultKernelArtifact(t, evidence, "fault-kernel-warmup-traces.jsonl")
	measured := findFaultKernelArtifact(t, evidence, "fault-kernel-trial-traces.jsonl")
	csvArtifact := findFaultKernelArtifact(t, evidence, "fault-kernel.csv")
	if warmup == nil || warmup.Records != len(FaultKernelFormalWarmupSeeds())*2 {
		t.Fatalf("warmup artifact = %#v", warmup)
	}
	if measured == nil || measured.Records != len(FaultKernelFormalMeasuredSeeds())*2 ||
		csvArtifact == nil || csvArtifact.Records != len(FaultKernelFormalMeasuredSeeds())*2 {
		t.Fatalf("measurement artifacts = traces %#v CSV %#v", measured, csvArtifact)
	}
}

func TestBuildFaultKernelEvidenceRequiresCompleteCounterbalancedPairs(t *testing.T) {
	plan, err := NewFaultKernelSmokePlan([]uint32{7, 11}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	bundle := faultKernelTestBundle(t, plan)
	bundle.Trials = bundle.Trials[:len(bundle.Trials)-1]
	if _, err := buildFaultKernelExperimentEvidence(bundle); err == nil {
		t.Fatal("incomplete measured pair was accepted")
	}
	bundle = faultKernelTestBundle(t, plan)
	for index := range bundle.Trials {
		if bundle.Trials[index].Trace.Seed == 11 && bundle.Trials[index].Trace.Role == "baseline" {
			bundle.Trials[index].Trace.TrialOrder = 0
			for sampleIndex := range bundle.Trials[index].Samples {
				bundle.Trials[index].Samples[sampleIndex].TrialOrder = 0
			}
		}
	}
	if _, err := buildFaultKernelExperimentEvidence(bundle); err == nil {
		t.Fatal("counterbalance drift was accepted")
	}
}

func TestBuildFaultKernelEvidenceBindsCanonicalWorkloadRuntimeConfig(t *testing.T) {
	plan, err := NewFaultKernelSmokePlan([]uint32{7}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	bundle := faultKernelTestBundle(t, plan)
	var changed faultKernelWorkload
	if err := json.Unmarshal(bundle.WorkloadJSON, &changed); err != nil {
		t.Fatal(err)
	}
	changed.RuntimeConfig.MaxWorkers++
	bundle.WorkloadJSON, err = canonicalJSON(changed)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Input.WorkloadSHA256 = hashBytes(bundle.WorkloadJSON)
	inputJSON, err := canonicalJSON(bundle.Input)
	if err != nil {
		t.Fatal(err)
	}
	for index := range bundle.Trials {
		bundle.Trials[index].Trace.WorkloadSHA256 = bundle.Input.WorkloadSHA256
		bundle.Trials[index].Trace.InputSHA256 = hashBytes(inputJSON)
	}
	if _, err := buildFaultKernelExperimentEvidence(bundle); err == nil || !strings.Contains(err.Error(), "runtime config") {
		t.Fatalf("runtime config drift result = %v", err)
	}
}
