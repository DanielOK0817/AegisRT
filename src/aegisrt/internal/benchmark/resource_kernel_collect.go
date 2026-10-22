package benchmark

import (
	"context"
	"errors"
)

func CollectResourceKernel(ctx context.Context, options ResourceKernelCollectOptions) (RunSummary, error) {
	if ctx == nil {
		return RunSummary{}, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return RunSummary{}, err
	}
	if err := ValidateResourceKernelPlan(options.Plan); err != nil {
		return RunSummary{}, err
	}
	return collectResourceKernelPlatform(ctx, options)
}
