//go:build !linux

package resourcecontrol

import (
	"io"
	"os/exec"
)

func (*CgroupV2Driver) PlaceCommandInExistingGroup(string, *exec.Cmd) (io.Closer, error) {
	return nil, ErrUnsupportedPlatform
}

func (*CgroupV2Driver) PlaceCommandInAttemptManager(string, *exec.Cmd) (io.Closer, error) {
	return nil, ErrUnsupportedPlatform
}
