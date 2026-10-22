//go:build linux

package resourcecontrol

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
)

func TestProcessPrepareHookCreatesAtomicPlacementAndCleanup(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	hook := driver.ProcessPrepareHook(Limits{
		Resources: domain.ResourceVector{CPUMilli: 500, MemoryMiB: 64},
		PIDsMax:   8,
	})
	command := exec.Command("/bin/true")
	cleanup, err := hook(context.Background(), executor.Request{AttemptID: "attempt-hook"}, command)
	if err != nil {
		t.Fatal(err)
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.UseCgroupFD || command.SysProcAttr.CgroupFD <= 0 {
		t.Fatalf("command was not configured for clone-into-cgroup: %+v", command.SysProcAttr)
	}
	groupPath, err := driver.groupPath("attempt-hook")
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(groupPath, "cpu.max"), "50000 100000")
	assertFileContent(t, filepath.Join(groupPath, "memory.max"), "67108864")
	for _, name := range []string{"cpu.max", "memory.max", "memory.swap.max", "pids.max"} {
		if err := os.Remove(filepath.Join(groupPath, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(groupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup retained cgroup: %v", err)
	}
}

func TestProcessPrepareHookHonorsCancellation(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = driver.ProcessPrepareHook(Limits{})(ctx, executor.Request{AttemptID: "canceled"}, exec.Command("/bin/true"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
