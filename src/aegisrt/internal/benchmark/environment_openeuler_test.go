package benchmark

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectOpenEulerEnvironmentRequiresKernelEvidenceBoundary(t *testing.T) {
	root := t.TempDir()
	paths := writeOpenEulerEnvironmentFixture(t, root)

	environment, err := collectOpenEulerEnvironment(paths, "amd64", 8)
	if err != nil {
		t.Fatal(err)
	}
	if environment.EvidenceScope != "openeuler-kernel" || environment.OSFamily != "linux" ||
		environment.DistributionID != "openEuler" || environment.DistributionVersion != "24.03-LTS" ||
		environment.KernelRelease != "6.6.0-test" || environment.Architecture != "amd64" ||
		environment.LogicalCPUCount != 8 || environment.MemoryTotalBytes != 16*1024*1024 {
		t.Fatalf("environment = %#v", environment)
	}
	wantControllers := []string{"cpu", "memory", "pids"}
	if len(environment.Cgroup.Controllers) != len(wantControllers) {
		t.Fatalf("controllers = %#v", environment.Cgroup.Controllers)
	}
	for index := range wantControllers {
		if environment.Cgroup.Controllers[index] != wantControllers[index] {
			t.Fatalf("controllers = %#v", environment.Cgroup.Controllers)
		}
	}
	if environment.Cgroup.Version != "v2" || environment.Cgroup.Delegation != "delegated" ||
		environment.Cgroup.PSI != "available" {
		t.Fatalf("cgroup = %#v", environment.Cgroup)
	}
	if err := validateEnvironmentForScope("openeuler-kernel", environment); err != nil {
		t.Fatalf("valid environment rejected: %v", err)
	}
}

func TestCollectOpenEulerEnvironmentFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, paths openEulerEnvironmentPaths)
	}{
		{
			name: "wrong distribution",
			mutate: func(t *testing.T, paths openEulerEnvironmentPaths) {
				writeFixtureFile(t, paths.OSRelease, "ID=ubuntu\nVERSION_ID=24.04\n")
			},
		},
		{
			name: "controller not enabled",
			mutate: func(t *testing.T, paths openEulerEnvironmentPaths) {
				writeFixtureFile(t, paths.CgroupSubtreeControl, "cpu memory\n")
			},
		},
		{
			name: "psi unavailable",
			mutate: func(t *testing.T, paths openEulerEnvironmentPaths) {
				if err := os.Remove(paths.PSIMemory); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid memory",
			mutate: func(t *testing.T, paths openEulerEnvironmentPaths) {
				writeFixtureFile(t, paths.MemInfo, "MemTotal: unknown kB\n")
			},
		},
		{
			name: "not cgroup v2",
			mutate: func(_ *testing.T, paths openEulerEnvironmentPaths) {
				paths.CgroupV2 = false
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := writeOpenEulerEnvironmentFixture(t, t.TempDir())
			if test.name == "not cgroup v2" {
				paths.CgroupV2 = false
			} else {
				test.mutate(t, paths)
			}
			if _, err := collectOpenEulerEnvironment(paths, "amd64", 8); err == nil {
				t.Fatal("invalid openEuler environment was accepted")
			}
		})
	}
}

func TestValidateEnvironmentForScopeRejectsIncompleteOpenEulerClaim(t *testing.T) {
	valid := Environment{
		EvidenceScope: "openeuler-kernel", OSFamily: "linux", DistributionID: "openEuler",
		DistributionVersion: "24.03-LTS", KernelRelease: "6.6.0-test", Architecture: "arm64",
		LogicalCPUCount: 4, MemoryTotalBytes: 1024,
		Cgroup: CgroupEnvironment{
			Version: "v2", Controllers: []string{"cpu", "memory", "pids"},
			Delegation: "delegated", PSI: "available",
		},
	}
	mutations := []func(*Environment){
		func(value *Environment) { value.DistributionID = "linux" },
		func(value *Environment) { value.Cgroup.Version = "v1" },
		func(value *Environment) { value.Cgroup.Controllers = []string{"cpu", "memory"} },
		func(value *Environment) { value.Cgroup.Delegation = "read-only" },
		func(value *Environment) { value.Cgroup.PSI = "unavailable" },
	}
	for index, mutate := range mutations {
		candidate := valid
		candidate.Cgroup.Controllers = append([]string(nil), valid.Cgroup.Controllers...)
		mutate(&candidate)
		if err := validateEnvironmentForScope("openeuler-kernel", candidate); err == nil {
			t.Fatalf("mutation %d was accepted: %#v", index, candidate)
		}
	}
}

func writeOpenEulerEnvironmentFixture(t *testing.T, root string) openEulerEnvironmentPaths {
	t.Helper()
	paths := openEulerEnvironmentPaths{
		OSRelease: filepath.Join(root, "os-release"), KernelRelease: filepath.Join(root, "kernel-release"),
		MemInfo: filepath.Join(root, "meminfo"), CgroupControllers: filepath.Join(root, "cgroup.controllers"),
		CgroupSubtreeControl: filepath.Join(root, "cgroup.subtree_control"),
		PSICPU:               filepath.Join(root, "cpu.pressure"), PSIMemory: filepath.Join(root, "memory.pressure"),
		PSIIO: filepath.Join(root, "io.pressure"), CgroupV2: true,
	}
	writeFixtureFile(t, paths.OSRelease, "NAME=openEuler\nID=\"openEuler\"\nVERSION_ID=\"24.03-LTS\"\n")
	writeFixtureFile(t, paths.KernelRelease, "6.6.0-test\n")
	writeFixtureFile(t, paths.MemInfo, "MemTotal:       16384 kB\nMemFree: 1 kB\n")
	writeFixtureFile(t, paths.CgroupControllers, "cpu io memory pids\n")
	writeFixtureFile(t, paths.CgroupSubtreeControl, "cpu memory pids\n")
	writeFixtureFile(t, paths.PSICPU, "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	writeFixtureFile(t, paths.PSIMemory, "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	writeFixtureFile(t, paths.PSIIO, "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	return paths
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
