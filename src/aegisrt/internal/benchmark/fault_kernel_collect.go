package benchmark

import (
	"context"
	"errors"
)

var (
	ErrFaultKernelUnsupported = errors.New("fault-kernel requires a supported Linux host")
	ErrFaultKernelUnavailable = errors.New("fault-kernel supervisor is unavailable")
)

type FaultKernelCollectOptions struct {
	OutputDir string
	Plan      FaultKernelPlan
}

func CollectFaultKernel(ctx context.Context, options FaultKernelCollectOptions) (RunSummary, error) {
	if ctx == nil {
		return RunSummary{}, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return RunSummary{}, err
	}
	if err := ValidateFaultKernelPlan(options.Plan); err != nil {
		return RunSummary{}, err
	}
	return collectFaultKernelPlatform(ctx, options)
}
