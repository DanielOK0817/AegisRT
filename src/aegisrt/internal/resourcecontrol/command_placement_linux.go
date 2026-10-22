//go:build linux

package resourcecontrol

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// PlaceCommandInExistingGroup configures clone3 placement for a caller-owned
// direct child. The caller must keep the returned descriptor open through
// command.Start and then close it. This method does not destroy the cgroup.
func (d *CgroupV2Driver) PlaceCommandInExistingGroup(attemptID string, command *exec.Cmd) (io.Closer, error) {
	if d == nil || command == nil {
		return nil, errors.New("cgroup driver and command are required")
	}
	if command.Process != nil {
		return nil, errors.New("command has already started")
	}
	groupPath, err := d.groupPath(attemptID)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Open(
		groupPath,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, errors.New("open placement cgroup failed")
	}
	group := os.NewFile(uintptr(fd), "cgroup-placement")
	if group == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open placement cgroup failed")
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	} else if command.SysProcAttr.UseCgroupFD {
		_ = group.Close()
		return nil, errors.New("command already has cgroup placement")
	}
	command.SysProcAttr.UseCgroupFD = true
	command.SysProcAttr.CgroupFD = int(group.Fd())
	return group, nil
}

// PlaceCommandInAttemptManager configures clone3 placement in the fixed
// manager child of a validated attempt. It never accepts a path or child name.
func (d *CgroupV2Driver) PlaceCommandInAttemptManager(attemptID string, command *exec.Cmd) (io.Closer, error) {
	if d == nil || command == nil {
		return nil, errors.New("cgroup driver and command are required")
	}
	if command.Process != nil {
		return nil, errors.New("command has already started")
	}
	if command.SysProcAttr != nil && command.SysProcAttr.UseCgroupFD {
		return nil, errors.New("command already has cgroup placement")
	}
	if err := d.validateFixedBase(); err != nil {
		return nil, err
	}
	attemptPath, err := d.groupPath(attemptID)
	if err != nil {
		return nil, err
	}
	attemptExists, err := realDirectoryExists(attemptPath)
	if err != nil || !attemptExists {
		return nil, ErrUnsafeCgroupHierarchy
	}
	if _, err := inspectFixedCgroupDirectory(fixedCgroupLevel{
		path: attemptPath, allowed: allowedDirectories("manager", "aegisrt"), required: true,
	}); err != nil {
		return nil, err
	}
	managerPath := filepath.Join(attemptPath, "manager")
	if err := os.Mkdir(managerPath, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, ErrUnsafeCgroupHierarchy
	}
	if _, err := inspectFixedCgroupLevel(fixedCgroupLevel{path: managerPath, required: true}); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(managerPath, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrUnsafeCgroupHierarchy
	}
	group := os.NewFile(uintptr(fd), "cgroup-manager")
	if group == nil {
		_ = syscall.Close(fd)
		return nil, ErrUnsafeCgroupHierarchy
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.UseCgroupFD = true
	command.SysProcAttr.CgroupFD = fd
	return group, nil
}
