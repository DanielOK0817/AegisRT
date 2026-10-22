package main

import (
	"testing"

	"aegisrt/internal/domain"
)

func TestRuntimeConfigFingerprintIncludesCgroupRoot(t *testing.T) {
	capacity := domain.ResourceVector{CPUMilli: 1000}
	base := daemonOptions{
		maxWorkers: 1, pidsMax: 10, cpuPeriod: 100_000,
		runtimeNamespace: "rt-test",
	}
	first := base
	first.cgroupRoot = "first-cgroup-root"
	second := base
	second.cgroupRoot = "second-cgroup-root"
	firstHash, err := runtimeConfigFingerprint("dag-drcfs", capacity, nil, first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := runtimeConfigFingerprint("dag-drcfs", capacity, nil, second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash {
		t.Fatal("different cgroup roots produced the same configuration fingerprint")
	}
	third := first
	third.runtimeNamespace = "rt-other"
	thirdHash, err := runtimeConfigFingerprint("dag-drcfs", capacity, nil, third)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == thirdHash {
		t.Fatal("different Runtime namespaces produced the same configuration fingerprint")
	}
}

func TestRuntimeNamespaceIsStablePerDurableStateIdentity(t *testing.T) {
	base := daemonOptions{cgroupRoot: t.TempDir(), stateFile: t.TempDir() + "/runtime.state"}
	first, err := withRuntimeNamespace(base)
	if err != nil {
		t.Fatal(err)
	}
	again, err := withRuntimeNamespace(base)
	if err != nil {
		t.Fatal(err)
	}
	secondBase := base
	secondBase.stateFile = t.TempDir() + "/runtime.state"
	second, err := withRuntimeNamespace(secondBase)
	if err != nil {
		t.Fatal(err)
	}
	if first.runtimeNamespace == "" || first.runtimeNamespace != again.runtimeNamespace {
		t.Fatalf("durable namespace is not stable: %q / %q", first.runtimeNamespace, again.runtimeNamespace)
	}
	if first.runtimeNamespace == second.runtimeNamespace {
		t.Fatalf("distinct state identities share namespace %q", first.runtimeNamespace)
	}
}
