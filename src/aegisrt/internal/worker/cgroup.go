package worker

import (
	"context"
	"errors"
	"os/exec"

	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/resourcecontrol"
)

type CgroupIsolation struct {
	Driver          *resourcecontrol.CgroupV2Driver
	PIDsMax         int64
	CPUPeriodMicros int64
}

func (c CgroupIsolation) PrepareHook(resources domain.ResourceVector) executor.PrepareHook {
	if c.Driver == nil {
		return func(context.Context, executor.Request, *exec.Cmd) (executor.Cleanup, error) {
			return nil, errors.New("cgroup v2 driver is not configured")
		}
	}
	return c.Driver.ProcessPrepareHook(resourcecontrol.Limits{
		Resources:       resources.Clone(),
		PIDsMax:         c.PIDsMax,
		CPUPeriodMicros: c.CPUPeriodMicros,
	})
}
