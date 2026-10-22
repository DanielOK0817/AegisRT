//go:build linux

package resourcecontrol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"aegisrt/internal/executor"
)

// ProcessPrepareHook creates the attempt cgroup before process start and asks
// clone3 to place the child into it atomically. Cleanup closes the directory
// descriptor, kills survivors, and removes the group on every terminal path.
func (d *CgroupV2Driver) ProcessPrepareHook(limits Limits) executor.PrepareHook {
	return func(ctx context.Context, request executor.Request, command *exec.Cmd) (executor.Cleanup, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		groupPath, err := d.Create(request.AttemptID, limits)
		if err != nil {
			// Create reclaims only a group it created. Validation or duplicate-ID
			// errors confer no right to destroy an existing attempt.
			return nil, fmt.Errorf("prepare attempt cgroup: %w", err)
		}
		group, err := os.Open(groupPath)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("open attempt cgroup: %w", err), d.Destroy(request.AttemptID))
		}
		if command.SysProcAttr == nil {
			command.SysProcAttr = &syscall.SysProcAttr{}
		}
		command.SysProcAttr.UseCgroupFD = true
		command.SysProcAttr.CgroupFD = int(group.Fd())
		return func() error {
			return errors.Join(group.Close(), d.Destroy(request.AttemptID))
		}, nil
	}
}
