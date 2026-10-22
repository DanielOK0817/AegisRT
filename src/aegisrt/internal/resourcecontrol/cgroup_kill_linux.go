//go:build linux

package resourcecontrol

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

func terminateCgroup(groupPath string) error {
	killPath := filepath.Join(groupPath, "cgroup.kill")
	if _, statErr := os.Stat(killPath); statErr == nil {
		if err := os.WriteFile(killPath, []byte("1"), 0o644); err != nil {
			return fmt.Errorf("kill cgroup processes: %w", err)
		}
		return nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect cgroup.kill: %w", statErr)
	}
	processesPath := filepath.Join(groupPath, "cgroup.procs")
	for attempt := 0; attempt < 20; attempt++ {
		content, err := os.ReadFile(processesPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read cgroup processes: %w", err)
		}
		fields := strings.Fields(string(content))
		if len(fields) == 0 {
			return nil
		}
		for _, field := range fields {
			pid, parseErr := strconv.Atoi(field)
			if parseErr != nil || pid <= 0 {
				return fmt.Errorf("invalid cgroup pid %q", field)
			}
			if killErr := syscall.Kill(pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
				return fmt.Errorf("kill cgroup pid %d: %w", pid, killErr)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("cgroup processes did not exit before cleanup deadline")
}
