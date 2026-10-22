//go:build !linux

package resourcecontrol

func isCgroupV2Filesystem(string) (bool, error) { return false, nil }
