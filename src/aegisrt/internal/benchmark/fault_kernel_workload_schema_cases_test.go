package benchmark

import (
	"encoding/json"
	"testing"
)

func TestFaultKernelGeneratedWorkloadsMatchPublishedSchema(t *testing.T) {
	validator := loadFaultKernelWorkloadSchema(t)
	smoke, err := NewFaultKernelSmokePlan([]uint32{0, ^uint32(0)}, 2, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	formal, err := NewFaultKernelFormalPlan(FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range []FaultKernelPlan{smoke, formal} {
		workload, _, buildErr := newFaultKernelWorkload(plan)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		if validateErr := validator.validate(workloadSchemaValue(t, workload)); validateErr != nil {
			t.Fatalf("generated %s workload failed its schema: %v", plan.Profile, validateErr)
		}
	}
}

func TestFaultKernelWorkloadSchemaRejectsSemanticDrift(t *testing.T) {
	validator := loadFaultKernelWorkloadSchema(t)
	smokePlan, err := NewFaultKernelSmokePlan([]uint32{0}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	smoke, _, err := newFaultKernelWorkload(smokePlan)
	if err != nil {
		t.Fatal(err)
	}
	smokeValue := workloadSchemaValue(t, smoke)
	mutations := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"profile plan", func(root map[string]any) { root["plan_id"] = "f4-daemon-sigkill-cgroup-v1" }},
		{"profile case", func(root map[string]any) { root["case_id"] = "F4" }},
		{"runtime capacity", func(root map[string]any) {
			objectField(objectField(root, "runtime_config"), "capacity")["cpu_milli"] = json.Number("999")
		}},
		{"agent order", func(root map[string]any) {
			agents := arrayField(root, "agents")
			agents[0], agents[1] = agents[1], agents[0]
		}},
		{"agent role", func(root map[string]any) {
			arrayField(root, "agents")[0].(map[string]any)["fixture_role"] = "survivor"
		}},
		{"agent resources", func(root map[string]any) {
			agent := arrayField(root, "agents")[0].(map[string]any)
			objectField(agent, "resources")["memory_mib"] = json.Number("65")
		}},
		{"target agent", func(root map[string]any) { firstTask(root, "target")["agent_id"] = "f4-survivor-agent" }},
		{"target priority", func(root map[string]any) { firstTask(root, "target")["priority"] = json.Number("10") }},
		{"target resources", func(root map[string]any) {
			objectField(firstTask(root, "target"), "resources")["cpu_milli"] = json.Number("101")
		}},
		{"target attempts", func(root map[string]any) { firstTask(root, "target")["max_attempts"] = json.Number("1") }},
		{"target dependencies", func(root map[string]any) { firstTask(root, "target")["dependencies"] = []any{"other"} }},
		{"survivor priority", func(root map[string]any) { firstTask(root, "survivor")["priority"] = json.Number("1") }},
		{"smoke warmup", func(root map[string]any) { firstCell(root)["phase"] = "warmup" }},
		{"phase id mismatch", func(root map[string]any) { firstCell(root)["pair_id"] = "f4-w-0-0" }},
		{"seed overflow", func(root map[string]any) { firstCell(root)["seed"] = json.Number("4294967296") }},
		{"extra cell", func(root map[string]any) {
			cells := arrayField(root, "cells")
			root["cells"] = append(cells, cells[0], cells[0], cells[0], cells[0])
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := cloneWorkloadSchemaValue(t, smokeValue)
			mutation.mutate(changed)
			if err := validator.validate(changed); err == nil {
				t.Fatal("mutated workload passed the published schema")
			}
		})
	}

	formalPlan, err := NewFaultKernelFormalPlan(FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	formal, _, err := newFaultKernelWorkload(formalPlan)
	if err != nil {
		t.Fatal(err)
	}
	formalValue := workloadSchemaValue(t, formal)
	formalMutations := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing cell", func(root map[string]any) {
			cells := arrayField(root, "cells")
			root["cells"] = cells[:len(cells)-1]
		}},
		{"warmup seed", func(root map[string]any) { firstCell(root)["seed"] = json.Number("104702") }},
		{"warmup phase", func(root map[string]any) { firstCell(root)["phase"] = "measurement" }},
		{"measurement repetition", func(root map[string]any) {
			arrayField(root, "cells")[3].(map[string]any)["repetition"] = json.Number("1")
		}},
	}
	for _, mutation := range formalMutations {
		t.Run("formal "+mutation.name, func(t *testing.T) {
			changed := cloneWorkloadSchemaValue(t, formalValue)
			mutation.mutate(changed)
			if err := validator.validate(changed); err == nil {
				t.Fatal("mutated formal matrix passed the published schema")
			}
		})
	}
}
