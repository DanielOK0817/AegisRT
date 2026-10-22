package resourcecontrol

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrResidualCgroups = errors.New("residual attempt cgroups remain")

// EnsureNamespaceEmpty fails closed when an attempt-like directory or symlink
// remains after checkpoint recovery. It intentionally does not return entry
// names, which may be derived from application task identifiers.
func (d *CgroupV2Driver) EnsureNamespaceEmpty() error {
	entries, err := os.ReadDir(filepath.Join(d.root, "aegisrt", d.namespace))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Runtime cgroup namespace: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return ErrResidualCgroups
		}
	}
	return nil
}
