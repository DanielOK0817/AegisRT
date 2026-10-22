//go:build !linux

package benchmark

import "context"

func collectResourceKernelPlatform(context.Context, ResourceKernelCollectOptions) (RunSummary, error) {
	return RunSummary{}, ErrResourceKernelUnsupported
}
