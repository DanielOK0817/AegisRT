package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/resourcecontrol"
)

func TestCgroupIsolationBuildsHook(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("clone-into-cgroup is Linux-specific")
	}
	root := t.TempDir()
	driver, err := resourcecontrol.NewCgroupV2Driver(root, "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	isolation := CgroupIsolation{Driver: driver, PIDsMax: 16}
	hook := isolation.PrepareHook(domain.ResourceVector{CPUMilli: 100, MemoryMiB: 32})
	request := executor.Request{AttemptID: "worker-cgroup"}
	cleanup, err := hook(context.Background(), request, exec.Command("/bin/true"))
	if err != nil {
		t.Fatal(err)
	}
	if cleanup == nil {
		t.Fatal("cgroup prepare hook did not return cleanup")
	}
	groupPath := filepath.Join(root, "aegisrt", "runtime-a", "worker-cgroup")
	for _, name := range []string{"cpu.max", "memory.max", "memory.swap.max", "pids.max"} {
		if err := os.Remove(filepath.Join(groupPath, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestCgroupIsolationRejectsMissingDriver(t *testing.T) {
	hook := (CgroupIsolation{}).PrepareHook(domain.ResourceVector{})
	_, err := hook(context.Background(), executor.Request{AttemptID: "worker"}, exec.Command("missing"))
	if err == nil {
		t.Fatal("missing cgroup driver was accepted")
	}
}
