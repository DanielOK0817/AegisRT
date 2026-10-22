//go:build linux

package resourcecontrol

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
)

func TestPreparationErrorDoesNotDestroyUnownedAttempt(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	path, err := driver.groupPath("already-owned")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, limits := range []Limits{{Resources: domain.ResourceVector{CPUMilli: -1}}, {}} {
		cleanup, err := driver.ProcessPrepareHook(limits)(context.Background(), executor.Request{AttemptID: "already-owned"}, exec.Command("/bin/true"))
		if err == nil || cleanup != nil {
			t.Fatalf("unowned preparation cleanup=%v err=%v", cleanup != nil, err)
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed preparation destroyed an existing attempt")
		}
	}
}
