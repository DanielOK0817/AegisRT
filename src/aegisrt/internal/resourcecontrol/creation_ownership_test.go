package resourcecontrol

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"aegisrt/internal/domain"
)

func TestCreateRefusesExistingAttemptWithoutChangingControls(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	path, err := driver.Create("owned", Limits{Resources: domain.ResourceVector{CPUMilli: 250}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = driver.Create("owned", Limits{Resources: domain.ResourceVector{CPUMilli: 900}})
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate creation error=%v", err)
	}
	assertFileContent(t, filepath.Join(path, "cpu.max"), "25000 100000")
}
