//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Linux /proc identity checks deliberately make the cleanup adapter Linux-only.
func killRegisteredAgent(pid int, binary, dir string) (bool, error) {
	base := filepath.Join("/proc", strconv.Itoa(pid))
	command, err := os.ReadFile(filepath.Join(base, "cmdline"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	parts := strings.Split(string(command), "\x00")
	if len(parts) < 2 || filepath.Clean(parts[0]) != filepath.Clean(binary) || !strings.Contains(string(command), dir) {
		return false, errors.New("refusing to kill an unrecognized experiment child")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	if err := process.Signal(syscall.SIGKILL); err != nil {
		return false, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		stat, err := os.ReadFile(filepath.Join(base, "stat"))
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if err != nil {
			return true, err
		}
		// An orphan zombie has no executing process or descendants. Reaping is
		// owned by the host's init; this adapter does not claim cgroup cleanup.
		close := strings.LastIndexByte(string(stat), ')')
		if close >= 0 && strings.HasPrefix(string(stat)[close+1:], " Z ") {
			return true, nil
		}
		if time.Now().After(deadline) {
			return true, fmt.Errorf("experiment child %d remained after kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
