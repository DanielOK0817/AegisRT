//go:build linux

package resourcecontrol

import "syscall"

const cgroupV2SuperMagic = 0x63677270

func isCgroupV2Filesystem(path string) (bool, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false, err
	}
	return stat.Type == cgroupV2SuperMagic, nil
}
