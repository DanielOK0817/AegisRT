//go:build !linux

package resourcecontrol

import (
	"errors"
	"os/exec"
	"testing"
)

func TestPlaceCommandInExistingGroupRejectsUnsupportedPlatform(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-placement")
	if err != nil {
		t.Fatal(err)
	}
	closer, err := driver.PlaceCommandInExistingGroup("daemon", exec.Command("unused"))
	if closer != nil || !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("placement = (%v, %v), want unsupported", closer, err)
	}
}

func TestPlaceCommandInAttemptManagerRejectsUnsupportedPlatform(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-placement")
	if err != nil {
		t.Fatal(err)
	}
	closer, err := driver.PlaceCommandInAttemptManager("daemon", exec.Command("unused"))
	if closer != nil || !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("placement = (%v, %v), want unsupported", closer, err)
	}
}
