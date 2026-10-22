package resourcecontrol

import (
	"path/filepath"
	"testing"
)

func TestCgroupV2CountersReadsPressureEvidence(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	path, err := driver.Create("attempt-a", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(path, "cpu.stat"), "usage_usec 120\nnr_throttled 7\n")
	writeFixture(t, filepath.Join(path, "memory.current"), "4096\n")
	writeFixture(t, filepath.Join(path, "memory.peak"), "8192\n")
	writeFixture(t, filepath.Join(path, "memory.events"), "oom_kill 2\n")
	writeFixture(t, filepath.Join(path, "pids.current"), "5\n")
	writeFixture(t, filepath.Join(path, "pids.events"), "max 3\n")
	counters, err := driver.Counters("attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	if counters.CPUThrottledEvents != 7 || counters.MemoryPeakBytes != 8192 || counters.OOMKillEvents != 2 || counters.PIDLimitEvents != 3 {
		t.Fatalf("unexpected counters: %+v", counters)
	}
}
