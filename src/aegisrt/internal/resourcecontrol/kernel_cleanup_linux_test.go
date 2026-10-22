//go:build linux

package resourcecontrol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
)

// This test is deliberately opt-in and accepts only a dedicated transient
// systemd unit. The ordinary suite never mutates the host cgroup hierarchy.
func TestKernelAttemptCleanup(t *testing.T) {
	if os.Getenv("AEGISRT_KERNEL_M4") != "1" {
		t.Skip("run scripts/openeuler/verify-resource-isolated.sh in an isolated transient unit")
	}
	root, err := ResolveSelfCgroupV2Root("/proc/self/cgroup", "/sys/fs/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	if name := filepath.Base(root); !strings.HasPrefix(name, "aegisrt-m4-") || !strings.HasSuffix(name, ".service") {
		t.Fatal("kernel verification requires its own aegisrt-m4 transient service")
	}
	driver, err := PrepareDelegatedCgroupV2(root, "kernel-verification", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Fatal("setsid is required for detached child verification")
	}
	for _, mode := range []string{"success", "exit-failure", "cancel", "timeout", "start-hook-failure", "start-failure"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			tail := "wait"
			if mode == "success" {
				tail = "exit 0"
			}
			if mode == "exit-failure" {
				tail = "exit 7"
			}
			request := executor.Request{
				AttemptID: "kernel-" + mode, Command: "/bin/sh",
				Args: []string{"-c", "setsid /bin/sh -c 'echo $$ > \"$1\"; exec sleep 60' sh \"$1\" >/dev/null 2>&1 & " +
					"while [ ! -s \"$1\" ]; do sleep 0.01; done; " + tail, "sh", pidFile},
				Timeout: 5 * time.Second,
			}
			if mode == "start-failure" {
				request.Command = "/aegisrt-m4-missing-executable"
			}
			if mode == "timeout" {
				request.Timeout = time.Second
			}
			if mode == "start-hook-failure" {
				request.OnStarted = func(context.Context, executor.Request, int) error {
					if _, err := kernelChildPID(pidFile); err != nil {
						return err
					}
					return errors.New("injected start hook failure")
				}
			}
			runner := executor.NewProcessRunnerWithPrepare(executor.MinimalHostEnvironment(), driver.ProcessPrepareHook(Limits{
				Resources: domain.ResourceVector{CPUMilli: 250, MemoryMiB: 64}, PIDsMax: 16,
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				result executor.Result
				err    error
			}
			done := make(chan outcome, 1)
			go func() { result, err := runner.Run(ctx, request); done <- outcome{result, err} }()
			var child int
			if mode != "start-failure" {
				child, err = kernelChildPID(pidFile)
				if err != nil {
					cancel()
					<-done
					t.Fatal(err)
				}
			}
			if mode == "cancel" {
				cancel()
			}
			result := <-done
			want := map[string]error{"exit-failure": executor.ErrExit, "cancel": executor.ErrCanceled, "timeout": executor.ErrTimeout,
				"start-hook-failure": executor.ErrStartHook, "start-failure": executor.ErrStart}[mode]
			if (want == nil && result.err != nil) || (want != nil && !errors.Is(result.err, want)) {
				t.Fatalf("result=%+v err=%v want=%v", result.result, result.err, want)
			}
			if errors.Is(result.err, executor.ErrCleanup) {
				t.Fatal(result.err)
			}
			if child != 0 && kernelChildLive(child) {
				t.Fatalf("detached child %d survived cleanup", child)
			}
			if err := driver.EnsureNamespaceEmpty(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func kernelChildPID(path string) (int, error) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(content))); err == nil && pid > 0 {
				return pid, nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0, errors.New("child PID was not written before deadline")
}

func kernelChildLive(pid int) bool {
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	_, tail, found := strings.Cut(string(content), ") ")
	return !found || !strings.HasPrefix(tail, "Z ")
}
