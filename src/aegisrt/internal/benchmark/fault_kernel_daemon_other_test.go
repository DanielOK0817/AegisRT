//go:build !linux

package benchmark

import (
	"context"
	"errors"
	"testing"
)

func TestStartFaultKernelDaemonIsUnsupportedOffLinux(t *testing.T) {
	daemon, receipt, err := startFaultKernelDaemon(context.Background(), faultKernelDaemonConfig{})
	if daemon != nil || receipt != (faultKernelDaemonReceipt{}) || !errors.Is(err, ErrFaultKernelUnsupported) {
		t.Fatalf("startFaultKernelDaemon = (%v, %+v, %v), want unsupported", daemon, receipt, err)
	}
}
