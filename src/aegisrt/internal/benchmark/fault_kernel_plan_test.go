package benchmark

import (
	"reflect"
	"strings"
	"testing"
)

func TestFaultKernelPlansEnforceSmokeAndFormalProfiles(t *testing.T) {
	smoke, err := NewFaultKernelSmokePlan([]uint32{7, 11}, 2, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	if smoke.PlanID != FaultKernelSmokePlanID || smoke.CaseID != FaultKernelSmokeCaseID ||
		len(smoke.Protocol.WarmupSeeds) != 0 {
		t.Fatalf("smoke identity = %#v", smoke)
	}
	if _, err := NewFaultKernelSmokePlan([]uint32{1, 2, 3}, 2, FaultKernelDefaultTrialTimeoutMS); err == nil {
		t.Fatal("smoke plan accepted more than four seed/repetition cells")
	}
	if _, err := NewFaultKernelSmokePlan([]uint32{7, 7}, 1, FaultKernelDefaultTrialTimeoutMS); err == nil {
		t.Fatal("smoke plan accepted duplicate seeds")
	}
	if _, err := NewFaultKernelSmokePlan([]uint32{7}, 1, faultKernelMinimumTrialTimeoutMS-1); err == nil {
		t.Fatal("smoke plan accepted a timeout shorter than the fixed process-tree protocol")
	}

	formal, err := NewFaultKernelFormalPlan(FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	if formal.PlanID != FaultKernelFormalPlanID || formal.CaseID != FaultKernelFormalCaseID ||
		formal.Protocol.Repetitions != 1 ||
		!reflect.DeepEqual(formal.Protocol.WarmupSeeds, FaultKernelFormalWarmupSeeds()) ||
		!reflect.DeepEqual(formal.Protocol.MeasuredSeeds, FaultKernelFormalMeasuredSeeds()) {
		t.Fatalf("formal plan is not fixed: %#v", formal)
	}
	mutated := cloneFaultKernelPlan(formal)
	mutated.Protocol.MeasuredSeeds[0]++
	if err := ValidateFaultKernelPlan(mutated); err == nil {
		t.Fatal("formal plan accepted measured-seed drift")
	}
	mutated = cloneFaultKernelPlan(formal)
	mutated.Protocol.Repetitions = 2
	if err := ValidateFaultKernelPlan(mutated); err == nil {
		t.Fatal("formal plan accepted repetition drift")
	}
}

func TestFaultKernelInputLocksProtocolAndAnonymousHashes(t *testing.T) {
	plan, err := NewFaultKernelSmokePlan([]uint32{7}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewFaultKernelInput(plan, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateFaultKernelInput(input); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*FaultKernelInput)
	}{
		{name: "plan", mutate: func(value *FaultKernelInput) { value.PlanID = FaultKernelFormalPlanID }},
		{name: "protocol", mutate: func(value *FaultKernelInput) { value.ClockMode = "wall-clock" }},
		{name: "mode order", mutate: func(value *FaultKernelInput) { value.Modes[0], value.Modes[1] = value.Modes[1], value.Modes[0] }},
		{name: "hash", mutate: func(value *FaultKernelInput) { value.RuntimeConfigSHA256 = "secret-path" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := input
			copy.Modes = append([]FaultKernelMode(nil), input.Modes...)
			test.mutate(&copy)
			if err := ValidateFaultKernelInput(copy); err == nil {
				t.Fatal("mutated input was accepted")
			}
		})
	}
}

func TestFaultKernelComponentsRequireExactlyOneOfEachRole(t *testing.T) {
	manifest := validFaultKernelComponents()
	if err := ValidateFaultKernelComponentManifest(manifest); err != nil {
		t.Fatalf("valid components rejected: %v", err)
	}
	duplicate := manifest
	duplicate.Components = append([]FaultKernelComponent(nil), manifest.Components...)
	duplicate.Components[2].Role = FaultKernelComponentHarness
	if err := ValidateFaultKernelComponentManifest(duplicate); err == nil {
		t.Fatal("duplicate component role was accepted")
	}
	invalid := manifest
	invalid.Components = append([]FaultKernelComponent(nil), manifest.Components...)
	invalid.Components[0].Bytes = 0
	if err := ValidateFaultKernelComponentManifest(invalid); err == nil {
		t.Fatal("empty component artifact was accepted")
	}
	unbound := manifest
	unbound.Components = append([]FaultKernelComponent(nil), manifest.Components...)
	unbound.Components[2].SHA256 = strings.Repeat("3", 64)
	if err := ValidateFaultKernelComponentManifest(unbound); err == nil {
		t.Fatal("fixture not bound to harness executable was accepted")
	}
}

func cloneFaultKernelPlan(plan FaultKernelPlan) FaultKernelPlan {
	plan.Protocol.WarmupSeeds = append([]uint32(nil), plan.Protocol.WarmupSeeds...)
	plan.Protocol.MeasuredSeeds = append([]uint32(nil), plan.Protocol.MeasuredSeeds...)
	return plan
}

func validFaultKernelComponents() FaultKernelComponentManifest {
	revision := strings.Repeat("d", 40)
	return FaultKernelComponentManifest{
		SchemaVersion: FaultKernelComponentManifestSchemaV1,
		Components: []FaultKernelComponent{
			{Role: FaultKernelComponentHarness, ArtifactKind: "executable", Bytes: 100, SHA256: strings.Repeat("1", 64), SourceRevision: &revision},
			{Role: FaultKernelComponentRuntimeDaemon, ArtifactKind: "executable", Bytes: 200, SHA256: strings.Repeat("2", 64), SourceRevision: &revision},
			{Role: FaultKernelComponentAgentFixture, ArtifactKind: "embedded-fixture", Bytes: 100, SHA256: strings.Repeat("1", 64), SourceRevision: &revision},
		},
	}
}
