package benchmark

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCommunicationEnvironmentWithoutKernelDelegation(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("local communication currently supports Windows and Linux")
	}
	environment, err := currentCommunicationEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if environment.OSFamily != runtime.GOOS || environment.LogicalCPUCount != runtime.NumCPU() ||
		environment.MemoryTotalBytes == 0 || environment.DistributionID == "" || environment.DistributionVersion == "" || environment.KernelRelease == "" {
		t.Fatalf("incomplete or incorrect local host identity: %+v", environment)
	}
	if err := validateEnvironmentForScope("openeuler-kernel", environment); err == nil {
		t.Fatal("local communication environment was accepted as delegated kernel evidence")
	}
	if runtime.GOOS == "linux" && (environment.EvidenceScope != "linux-local" || environment.Cgroup.Delegation != "not-requested") {
		t.Fatalf("local Linux measurement made a kernel delegation claim: %+v", environment)
	}
}

func TestCommunicationLinuxSnapshotNeedsOnlyReadableHostMetadata(t *testing.T) {
	for _, release := range []struct{ text, id, version string }{
		{"ID=openEuler\nVERSION_ID=24.03\n", "openEuler", "24.03"},
		{"ID=ubuntu\nVERSION_ID=\"24.04\"\n", "ubuntu", "24.04"},
		{"ID=arch\n", "arch", "unspecified"},
	} {
		t.Run(release.id, func(t *testing.T) {
			root := t.TempDir()
			paths := communicationLinuxPaths{OSRelease: filepath.Join(root, "os-release"), KernelRelease: filepath.Join(root, "kernel-release"), MemInfo: filepath.Join(root, "meminfo")}
			for path, content := range map[string]string{paths.OSRelease: release.text, paths.KernelRelease: "6.6.0-local\n", paths.MemInfo: "MemTotal: 2048 kB\n"} {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			environment, err := collectCommunicationLinuxEnvironment(paths, "arm64", 4)
			if err != nil {
				t.Fatal(err)
			}
			if environment.EvidenceScope != "linux-local" || environment.DistributionID != release.id || environment.DistributionVersion != release.version ||
				environment.KernelRelease != "6.6.0-local" || environment.MemoryTotalBytes != 2048*1024 || environment.Architecture != "arm64" || environment.LogicalCPUCount != 4 {
				t.Fatalf("local host facts were not preserved: %+v", environment)
			}
			if environment.Cgroup.Version != "not-measured" || environment.Cgroup.Delegation != "not-requested" || environment.Cgroup.PSI != "not-measured" || len(environment.Cgroup.Controllers) != 0 {
				t.Fatalf("local snapshot made unsupported resource claims: %+v", environment.Cgroup)
			}
			if validateEnvironmentForScope("openeuler-kernel", environment) == nil {
				t.Fatal("local snapshot accepted as kernel evidence")
			}
			if err := os.Remove(paths.MemInfo); err != nil {
				t.Fatal(err)
			}
			if _, err := collectCommunicationLinuxEnvironment(paths, "arm64", 4); err == nil {
				t.Fatal("missing host metadata was fabricated")
			}
		})
	}
}

func TestCommunicationLinuxIdentityRejectsAmbiguousMetadata(t *testing.T) {
	for _, release := range []string{
		"VERSION_ID=24.04\n", "ID=ubuntu\nID=openEuler\n", "ID=unsafe value\n", "ID=ubuntu\nVERSION_ID=\n",
		"ID=ubuntu\nVERSION_ID=24.04\nVERSION_ID=26.04\n",
	} {
		if _, _, err := parseCommunicationLinuxRelease([]byte(release)); err == nil {
			t.Fatalf("invalid Linux identity accepted: %q", release)
		}
	}
}
