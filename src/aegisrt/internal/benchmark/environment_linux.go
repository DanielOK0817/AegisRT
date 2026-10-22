//go:build linux

package benchmark

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

const cgroupV2SuperMagic = 0x63677270

func currentEnvironment() (Environment, error) {
	return Environment{}, fmt.Errorf("Linux kernel evidence requires an explicit delegated cgroup root")
}

func collectHostOpenEulerEnvironment(cgroupRoot string) (Environment, error) {
	resolvedRoot, err := filepath.Abs(cgroupRoot)
	if err != nil {
		return Environment{}, fmt.Errorf("resolve delegated cgroup root: %w", err)
	}
	resolvedRoot, err = filepath.EvalSymlinks(resolvedRoot)
	if err != nil {
		return Environment{}, fmt.Errorf("resolve delegated cgroup root links: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil || !info.IsDir() {
		return Environment{}, fmt.Errorf("delegated cgroup root is not a directory")
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(resolvedRoot, &filesystem); err != nil {
		return Environment{}, fmt.Errorf("inspect delegated cgroup filesystem: %w", err)
	}
	return collectOpenEulerEnvironment(openEulerEnvironmentPaths{
		OSRelease: "/etc/os-release", KernelRelease: "/proc/sys/kernel/osrelease",
		MemInfo: "/proc/meminfo", CgroupControllers: filepath.Join(resolvedRoot, "cgroup.controllers"),
		CgroupSubtreeControl: filepath.Join(resolvedRoot, "cgroup.subtree_control"),
		PSICPU:               "/proc/pressure/cpu", PSIMemory: "/proc/pressure/memory", PSIIO: "/proc/pressure/io",
		CgroupV2: filesystem.Type == cgroupV2SuperMagic,
	}, architecture(), runtime.NumCPU())
}
