package resourcecontrol

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureNamespaceEmptyAcceptsMissingOrEmptyNamespace(t *testing.T) {
	root := t.TempDir()
	driver, err := NewCgroupV2Driver(root, "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.EnsureNamespaceEmpty(); err != nil {
		t.Fatalf("missing namespace: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "aegisrt", "runtime-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := driver.EnsureNamespaceEmpty(); err != nil {
		t.Fatalf("empty namespace: %v", err)
	}
}

func TestEnsureNamespaceEmptyRejectsResidualAttemptGroup(t *testing.T) {
	root := t.TempDir()
	driver, err := NewCgroupV2Driver(root, "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "aegisrt", "runtime-a", "unknown-attempt"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = driver.EnsureNamespaceEmpty()
	if !errors.Is(err, ErrResidualCgroups) {
		t.Fatalf("residual namespace error = %v, want ErrResidualCgroups", err)
	}
	if err != ErrResidualCgroups {
		t.Fatalf("residual error leaked a group identity: %v", err)
	}
}

func TestEnsureNamespaceEmptyRejectsUnexpectedSymlink(t *testing.T) {
	root := t.TempDir()
	namespace := filepath.Join(root, "aegisrt", "runtime-a")
	if err := os.MkdirAll(namespace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(namespace, "unexpected")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	driver, err := NewCgroupV2Driver(root, "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.EnsureNamespaceEmpty(); !errors.Is(err, ErrResidualCgroups) {
		t.Fatalf("unexpected namespace entry error = %v, want ErrResidualCgroups", err)
	}
}
