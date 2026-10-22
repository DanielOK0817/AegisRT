//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd

package statefile

import (
	"errors"
	"os"
)

func lockFileExclusive(*os.File) error { return errors.New("file locking is unsupported") }
func unlockFile(*os.File) error        { return nil }
