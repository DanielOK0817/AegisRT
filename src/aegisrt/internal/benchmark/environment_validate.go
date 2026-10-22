package benchmark

import (
	"fmt"
	"strings"
)

func validateEnvironmentForScope(scope string, environment Environment) error {
	if environment.EvidenceScope != scope {
		return fmt.Errorf("collector requires %s evidence", scope)
	}
	if environment.LogicalCPUCount <= 0 || environment.MemoryTotalBytes == 0 ||
		environment.DistributionVersion == "" || environment.KernelRelease == "" {
		return fmt.Errorf("evidence environment is incomplete")
	}
	switch scope {
	case "windows-functional":
		if environment.OSFamily != "windows" || environment.DistributionID != "windows" ||
			environment.Cgroup.Version != "none" || environment.Cgroup.Delegation != "none" {
			return fmt.Errorf("windows-functional evidence requires a Windows host without cgroup claims")
		}
	case "openeuler-kernel":
		if environment.OSFamily != "linux" || !strings.EqualFold(environment.DistributionID, "openEuler") ||
			environment.Cgroup.Version != "v2" || environment.Cgroup.Delegation != "delegated" ||
			environment.Cgroup.PSI != "available" {
			return fmt.Errorf("openeuler-kernel evidence requires openEuler with delegated cgroup v2 and PSI")
		}
		controllers := make(map[string]bool, len(environment.Cgroup.Controllers))
		for _, controller := range environment.Cgroup.Controllers {
			if controllers[controller] {
				return fmt.Errorf("cgroup controller list contains duplicates")
			}
			controllers[controller] = true
		}
		for _, required := range []string{"cpu", "memory", "pids"} {
			if !controllers[required] {
				return fmt.Errorf("openeuler-kernel evidence is missing %s controller", required)
			}
		}
	default:
		return fmt.Errorf("unsupported evidence scope %q", scope)
	}
	return nil
}
