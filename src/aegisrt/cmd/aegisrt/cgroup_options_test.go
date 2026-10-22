package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareRuntimeOptionsResolvesAndBootstrapsDelegatedCgroup(t *testing.T) {
	mountRoot := t.TempDir()
	serviceRoot := filepath.Join(mountRoot, "system.slice", "aegisrt.service")
	namespace := "runtime-test"
	prepareCgroupFixture(t, serviceRoot, namespace)
	procPath := filepath.Join(t.TempDir(), "cgroup")
	if err := os.WriteFile(procPath, []byte("0::/system.slice/aegisrt.service\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	options, err := prepareRuntimeOptionsWithPaths(daemonOptions{
		cgroupRoot: "auto", runtimeNamespace: namespace,
	}, procPath, mountRoot, 4242)
	if err != nil {
		t.Fatal(err)
	}
	if options.cgroupRoot != serviceRoot {
		t.Fatalf("resolved cgroup root = %q, want %q", options.cgroupRoot, serviceRoot)
	}
	managerPID, err := os.ReadFile(filepath.Join(serviceRoot, "manager", "cgroup.procs"))
	if err != nil {
		t.Fatal(err)
	}
	if string(managerPID) != "4242" {
		t.Fatalf("manager cgroup PID = %q", managerPID)
	}
	for _, path := range []string{
		filepath.Join(serviceRoot, "cgroup.subtree_control"),
		filepath.Join(serviceRoot, "aegisrt", "cgroup.subtree_control"),
		filepath.Join(serviceRoot, "aegisrt", namespace, "cgroup.subtree_control"),
	} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(content) != "+cpu +memory +pids" {
			t.Fatalf("controllers in %s = %q", path, content)
		}
	}
}

func TestPrepareRuntimeOptionsLeavesExplicitCgroupRootForAdministrator(t *testing.T) {
	root := t.TempDir()
	options, err := prepareRuntimeOptionsWithPaths(daemonOptions{
		cgroupRoot: root, runtimeNamespace: "runtime-test",
	}, "unused", "unused", 4242)
	if err != nil {
		t.Fatal(err)
	}
	if options.cgroupRoot != root {
		t.Fatalf("explicit cgroup root changed to %q", options.cgroupRoot)
	}
	if _, err := os.Stat(filepath.Join(root, "manager")); !os.IsNotExist(err) {
		t.Fatalf("explicit pre-prepared root was mutated: %v", err)
	}
}

func prepareCgroupFixture(t *testing.T, root, namespace string) {
	t.Helper()
	for _, path := range []string{root, filepath.Join(root, "manager"), filepath.Join(root, "aegisrt"), filepath.Join(root, "aegisrt", namespace)} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join(root, "manager") {
			if err := os.WriteFile(filepath.Join(path, "cgroup.controllers"), []byte("cpu memory pids"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "cgroup.subtree_control"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manager", "cgroup.procs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
