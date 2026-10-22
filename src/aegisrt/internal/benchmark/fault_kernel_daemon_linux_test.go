//go:build linux

package benchmark

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"aegisrt/internal/resourcecontrol"
)

type faultKernelDaemonFileInfo struct {
	mode os.FileMode
	uid  uint32
}

func (i faultKernelDaemonFileInfo) Name() string       { return "aegisrt" }
func (i faultKernelDaemonFileInfo) Size() int64        { return 1 }
func (i faultKernelDaemonFileInfo) Mode() os.FileMode  { return i.mode }
func (i faultKernelDaemonFileInfo) ModTime() time.Time { return time.Time{} }
func (i faultKernelDaemonFileInfo) IsDir() bool        { return false }
func (i faultKernelDaemonFileInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid} }

func TestValidateFaultKernelDaemonExecutableRequiresRootOwnedLockedExecutable(t *testing.T) {
	valid := faultKernelDaemonFileInfo{mode: 0o755, uid: 0}
	if err := validateFaultKernelDaemonExecutable(valid); err != nil {
		t.Fatal(err)
	}
	for name, info := range map[string]os.FileInfo{
		"non-root":       faultKernelDaemonFileInfo{mode: 0o755, uid: 1000},
		"group writable": faultKernelDaemonFileInfo{mode: 0o775, uid: 0},
		"world writable": faultKernelDaemonFileInfo{mode: 0o757, uid: 0},
		"not executable": faultKernelDaemonFileInfo{mode: 0o644, uid: 0},
		"not regular":    faultKernelDaemonFileInfo{mode: os.ModeDir | 0o755, uid: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(validateFaultKernelDaemonExecutable(info), ErrFaultKernelUnavailable) {
				t.Fatalf("validation unexpectedly accepted %+v", info)
			}
		})
	}
}

func TestLocateFaultKernelDaemonRejectsSymlinkWithoutLeakingPath(t *testing.T) {
	directory := t.TempDir()
	benchmarkPath := filepath.Join(directory, "aegisbench")
	target := filepath.Join(directory, "real-daemon")
	if err := os.WriteFile(target, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "aegisrt")); err != nil {
		t.Fatal(err)
	}
	_, err := locateFaultKernelDaemon(benchmarkPath)
	if !errors.Is(err, ErrFaultKernelUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
	if strings.Contains(err.Error(), directory) {
		t.Fatalf("error leaked executable path: %v", err)
	}
}

func TestFaultKernelDaemonKillAndWaitConfirmsSIGKILL(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "exec sleep 30")
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	daemon := recordStartedFaultKernelDaemon(command)
	t.Cleanup(func() { _ = daemon.Stop(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := daemon.KillAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	if !daemon.wasKilledBySIGKILL() {
		t.Fatal("daemon exit was not confirmed as SIGKILL")
	}
}

func TestFaultKernelDaemonStopIsBounded(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "exec sleep 30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	daemon := recordStartedFaultKernelDaemon(command)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if err := daemon.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) >= 5*time.Second {
		t.Fatal("Stop exceeded caller bound")
	}
}

func TestFaultKernelDaemonPreservesDeadlineClassification(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, _, err := startFaultKernelDaemon(ctx, faultKernelDaemonConfig{
		Driver: &resourcecontrol.CgroupV2Driver{}, AttemptID: "daemon",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pre-canceled start error = %v", err)
	}

	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	daemon := &faultKernelDaemon{done: make(chan struct{})}
	_, err = awaitFaultKernelDaemonReceipt(ctx, daemon, reader, time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("receipt deadline error = %v", err)
	}

	daemon.process = &os.Process{}
	if err := daemon.KillAndWait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("process-control deadline error = %v", err)
	}
}
