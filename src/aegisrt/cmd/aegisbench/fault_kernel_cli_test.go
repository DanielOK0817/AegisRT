package main

import (
	"reflect"
	"strings"
	"testing"

	"aegisrt/internal/benchmark"
)

func TestParseFaultKernelSmokeCommand(t *testing.T) {
	options, err := parseArgs([]string{
		"fault-kernel", "--profile", "smoke", "--output", "evidence",
		"--seeds", "104729,104743", "--repetitions", "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.kind != commandFaultKernel || options.profile != benchmark.FaultKernelProfileSmoke ||
		!reflect.DeepEqual(options.faultKernelPlan.Protocol.MeasuredSeeds, []uint32{104729, 104743}) ||
		options.faultKernelPlan.Protocol.Repetitions != 2 {
		t.Fatalf("smoke options = %#v", options)
	}
}

func TestParseFaultKernelFormalCommandIsFixed(t *testing.T) {
	options, err := parseArgs([]string{
		"fault-kernel", "--profile", "formal", "--output", "evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.faultKernelPlan.PlanID != benchmark.FaultKernelFormalPlanID ||
		!reflect.DeepEqual(options.faultKernelPlan.Protocol.WarmupSeeds, benchmark.FaultKernelFormalWarmupSeeds()) ||
		!reflect.DeepEqual(options.faultKernelPlan.Protocol.MeasuredSeeds, benchmark.FaultKernelFormalMeasuredSeeds()) {
		t.Fatalf("formal options drifted: %#v", options.faultKernelPlan)
	}
	for _, override := range [][]string{
		{"--seeds", "104729"},
		{"--repetitions", "1"},
	} {
		args := append([]string{"fault-kernel", "--profile", "formal", "--output", "evidence"}, override...)
		if _, parseErr := parseArgs(args); parseErr == nil {
			t.Fatalf("formal command accepted override %v", override)
		}
	}
}

func TestParseFaultKernelRejectsUnsafeOrAmbiguousInputs(t *testing.T) {
	tests := [][]string{
		{"fault-kernel", "--output", "evidence", "--seeds", "1", "--repetitions", "1"},
		{"fault-kernel", "--profile", "smoke", "--output", "evidence", "--seeds", "1,2,3", "--repetitions", "2"},
		{"fault-kernel", "--profile", "other", "--output", "evidence"},
		{"fault-kernel", "--profile", "smoke", "--output", "evidence", "--seeds", "1", "--repetitions", "1", "--daemon-pid", "7"},
	}
	for _, args := range tests {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("unsafe fault-kernel arguments accepted: %v", args)
		}
	}
	if !strings.Contains(usageText, "fault-kernel") {
		t.Fatal("usage does not expose fault-kernel")
	}
}
