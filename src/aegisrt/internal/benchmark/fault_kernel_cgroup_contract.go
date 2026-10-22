package benchmark

import "strconv"

type faultKernelControllerReadbackContract struct {
	cpuMax, memoryMax, pidsMax string
}

func expectedFaultKernelControllerReadback() faultKernelControllerReadbackContract {
	config := fixedFaultKernelRuntimeConfig()
	period := config.CPUPeriodMicros
	quota := faultKernelTaskResources.CPUMilli * period / 1000
	if quota < 1000 {
		quota = 1000
	}
	return faultKernelControllerReadbackContract{
		cpuMax:    strconv.FormatInt(quota, 10) + " " + strconv.FormatInt(period, 10),
		memoryMax: strconv.FormatInt(faultKernelTaskResources.MemoryMiB*1024*1024, 10),
		pidsMax:   strconv.FormatInt(config.PIDsMax, 10),
	}
}

func matchesFaultKernelControllerReadback(value FaultKernelControllerReadback) bool {
	want := expectedFaultKernelControllerReadback()
	return value.CPUMax != nil && *value.CPUMax == want.cpuMax &&
		value.MemoryMax != nil && *value.MemoryMax == want.memoryMax &&
		value.PIDsMax != nil && *value.PIDsMax == want.pidsMax
}
