package resourcecontrol

import (
	"fmt"
	"path/filepath"
)

// KernelCounters exposes the controller counters used by bounded resource
// pressure evidence. Values come directly from one validated attempt cgroup.
type KernelCounters struct {
	CPUUsageMicros     int64
	CPUThrottledEvents int64
	MemoryCurrentBytes int64
	MemoryPeakBytes    int64
	OOMKillEvents      int64
	PIDsCurrent        int64
	PIDLimitEvents     int64
}

func (d *CgroupV2Driver) Counters(attemptID string) (KernelCounters, error) {
	groupPath, err := d.groupPath(attemptID)
	if err != nil {
		return KernelCounters{}, err
	}
	cpu, err := readKeyValues(filepath.Join(groupPath, "cpu.stat"))
	if err != nil {
		return KernelCounters{}, fmt.Errorf("read cpu stats: %w", err)
	}
	memoryCurrent, err := readInt64(filepath.Join(groupPath, "memory.current"))
	if err != nil {
		return KernelCounters{}, fmt.Errorf("read memory current: %w", err)
	}
	memoryPeak, err := readInt64(filepath.Join(groupPath, "memory.peak"))
	if err != nil {
		return KernelCounters{}, fmt.Errorf("read memory peak: %w", err)
	}
	memoryEvents, err := readKeyValues(filepath.Join(groupPath, "memory.events"))
	if err != nil {
		return KernelCounters{}, fmt.Errorf("read memory events: %w", err)
	}
	pidsCurrent, err := readInt64(filepath.Join(groupPath, "pids.current"))
	if err != nil {
		return KernelCounters{}, fmt.Errorf("read pids current: %w", err)
	}
	pidsEvents, err := readKeyValues(filepath.Join(groupPath, "pids.events"))
	if err != nil {
		return KernelCounters{}, fmt.Errorf("read pids events: %w", err)
	}
	usage, exists := cpu["usage_usec"]
	if !exists {
		return KernelCounters{}, fmt.Errorf("read cpu stats: usage_usec is missing")
	}
	return KernelCounters{CPUUsageMicros: usage, CPUThrottledEvents: cpu["nr_throttled"],
		MemoryCurrentBytes: memoryCurrent, MemoryPeakBytes: memoryPeak, OOMKillEvents: memoryEvents["oom_kill"],
		PIDsCurrent: pidsCurrent, PIDLimitEvents: pidsEvents["max"]}, nil
}
