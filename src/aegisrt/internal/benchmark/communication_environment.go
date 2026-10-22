package benchmark

import "runtime"

func currentCommunicationEnvironment() (Environment, error) {
	if runtime.GOOS == "linux" {
		return collectCommunicationLinuxEnvironment(communicationLinuxPaths{
			OSRelease: "/etc/os-release", KernelRelease: "/proc/sys/kernel/osrelease", MemInfo: "/proc/meminfo",
		}, architecture(), runtime.NumCPU())
	}
	return currentEnvironment()
}
