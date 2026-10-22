//go:build !windows

package statefile

import (
	"errors"
	"io/fs"
)

func validateStatePermissions(info fs.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("state file must not be accessible by group or others")
	}
	return nil
}
