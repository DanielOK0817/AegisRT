//go:build !linux

package benchmark

import "context"

func startFaultKernelDaemon(context.Context, faultKernelDaemonConfig) (*faultKernelDaemon, faultKernelDaemonReceipt, error) {
	return nil, faultKernelDaemonReceipt{}, ErrFaultKernelUnsupported
}

func (*faultKernelDaemon) KillAndWait(context.Context) error { return ErrFaultKernelUnsupported }
func (*faultKernelDaemon) Stop(context.Context) error        { return ErrFaultKernelUnsupported }
func (*faultKernelDaemon) wasKilledBySIGKILL() bool          { return false }
