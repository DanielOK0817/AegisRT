package main

import (
	"strings"
	"testing"

	"aegisrt/internal/benchmark"
)

func TestParseResourceKernelSmoke(t *testing.T) {
	options, err := parseArgs([]string{"resource-kernel", "--profile", "smoke", "--output", "evidence"})
	if err != nil {
		t.Fatal(err)
	}
	if options.kind != commandResourceKernel || options.resourceKernelPlan.Profile != benchmark.ResourceKernelProfileSmoke {
		t.Fatalf("unexpected resource-kernel options: %+v", options)
	}
}

func TestParseResourceKernelRejectsOverrides(t *testing.T) {
	for _, args := range [][]string{
		{"resource-kernel", "--output", "evidence"},
		{"resource-kernel", "--profile", "formal", "--output", "evidence"},
		{"resource-kernel", "--profile", "smoke", "--output", "evidence", "--seeds", "1"},
		{"resource-kernel", "--profile", "smoke", "--output", "evidence", "--repetitions", "1"},
	} {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("unsafe resource-kernel arguments accepted: %v", args)
		}
	}
	if !strings.Contains(usageText, "resource-kernel") {
		t.Fatal("usage does not expose resource-kernel")
	}
}
