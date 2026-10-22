//go:build linux

package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessRunnerCancellationKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	request := Request{
		AttemptID: "process-tree",
		Command:   "/bin/sh",
		Args:      []string{"-c", "sleep 300 & echo $! > '" + pidFile + "'; wait"},
		Timeout:   time.Minute,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewProcessRunner(MinimalHostEnvironment()).Run(ctx, request)
		done <- err
	}()
	childPID := waitForChildPID(t, pidFile)
	cancel()
	if err := <-done; !errors.Is(err, ErrCanceled) {
		t.Fatalf("Run error = %v, want ErrCanceled", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(childPID) {
		t.Fatalf("child process %d survived cancellation", childPID)
	}
}

func TestProcessRunnerExitKillsBackgroundProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "background.pid")
	request := Request{
		AttemptID: "background-tree",
		Command:   "/bin/sh",
		Args:      []string{"-c", "sleep 300 & echo $! > '" + pidFile + "'; exit 0"},
		Timeout:   5 * time.Second,
	}
	done := make(chan error, 1)
	go func() {
		_, err := NewProcessRunner(MinimalHostEnvironment()).Run(context.Background(), request)
		done <- err
	}()
	childPID := waitForChildPID(t, pidFile)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not finish after leader exited")
	}
	deadline := time.Now().Add(2 * time.Second)
	for processExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(childPID) {
		t.Fatalf("background process %d survived leader exit", childPID)
	}
}

func waitForChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(content)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child PID was not written")
	return 0
}

func processExists(pid int) bool {
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); errors.Is(err, os.ErrNotExist) {
		return false
	} else if err == nil {
		fields := strings.Fields(string(stat))
		if len(fields) > 2 && fields[2] == "Z" {
			return false
		}
	}
	err := syscall.Kill(pid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}
