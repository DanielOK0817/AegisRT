package main

import (
	"fmt"
	"os"
	"runtime"

	"aegisrt/internal/resourcecontrol"
)

const (
	hostProcCgroupPath = "/proc/self/cgroup"
	hostCgroupV2Mount  = "/sys/fs/cgroup"
)

func prepareRuntimeOptions(options daemonOptions) (daemonOptions, error) {
	if options.cgroupRoot == "auto" && runtime.GOOS != "linux" {
		return daemonOptions{}, fmt.Errorf("automatic delegated cgroup mode is supported only on Linux")
	}
	return prepareRuntimeOptionsWithPaths(
		options, hostProcCgroupPath, hostCgroupV2Mount, os.Getpid(),
	)
}

func prepareRuntimeOptionsWithPaths(
	options daemonOptions,
	procCgroupPath string,
	cgroupMount string,
	managerPID int,
) (daemonOptions, error) {
	if options.cgroupRoot != "auto" {
		return withRuntimeNamespace(options)
	}
	root, err := resourcecontrol.ResolveSelfCgroupV2Root(procCgroupPath, cgroupMount)
	if err != nil {
		return daemonOptions{}, fmt.Errorf("discover delegated cgroup root: %w", err)
	}
	options.cgroupRoot = root
	options, err = withRuntimeNamespace(options)
	if err != nil {
		return daemonOptions{}, fmt.Errorf("prepare Runtime namespace: %w", err)
	}
	if _, err := resourcecontrol.PrepareDelegatedCgroupV2(
		options.cgroupRoot, options.runtimeNamespace, managerPID,
	); err != nil {
		return daemonOptions{}, fmt.Errorf("prepare delegated cgroup hierarchy: %w", err)
	}
	return options, nil
}
