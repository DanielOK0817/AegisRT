//go:build !linux

package benchmark

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestCollectFaultKernelFailsBeforeCreatingEvidenceOffLinux(t *testing.T) {
	output := t.TempDir()
	plan, err := NewFaultKernelSmokePlan([]uint32{104729}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := CollectFaultKernel(context.Background(), FaultKernelCollectOptions{
		OutputDir: output,
		Plan:      plan,
	})
	if summary != (RunSummary{}) || !errors.Is(err, ErrFaultKernelUnsupported) {
		t.Fatalf("CollectFaultKernel = (%+v, %v), want unsupported", summary, err)
	}
	entries, readErr := os.ReadDir(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("unsupported collection created %d output entries", len(entries))
	}
}
