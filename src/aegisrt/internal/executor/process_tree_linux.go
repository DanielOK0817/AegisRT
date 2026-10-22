//go:build linux

package executor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func configureProcessTree(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = time.Second
}

func waitProcess(command *exec.Cmd) error {
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- command.Wait()
	}()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-waitDone:
			return err
		case <-ticker.C:
			if command.Process == nil {
				continue
			}
			_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(command.Process.Pid)))
			if errors.Is(err, os.ErrNotExist) {
				// Wait has reaped the leader but can still be draining output
				// pipes inherited by background descendants. Kill the fixed
				// process group so those descriptors close promptly.
				terminateProcess(command)
				return <-waitDone
			}
		}
	}
}
