//go:build !linux

package benchmark

import "context"

func collectFaultKernelPlatform(context.Context, FaultKernelCollectOptions) (RunSummary, error) {
	return RunSummary{}, ErrFaultKernelUnsupported
}
