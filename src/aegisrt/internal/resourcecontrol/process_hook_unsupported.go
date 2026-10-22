//go:build !linux

package resourcecontrol

import (
	"context"
	"os/exec"

	"aegisrt/internal/executor"
)

func (*CgroupV2Driver) ProcessPrepareHook(Limits) executor.PrepareHook {
	return func(context.Context, executor.Request, *exec.Cmd) (executor.Cleanup, error) {
		return nil, ErrUnsupportedPlatform
	}
}
