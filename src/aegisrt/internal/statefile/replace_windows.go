//go:build windows

package statefile

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// Windows readers such as indexers can briefly deny delete sharing. Retry
// only the failed atomic rename, retaining the old checkpoint throughout.
// Persistent denial still returns failure and closes the Runtime safely.
func replaceStateFile(source, destination string) error {
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		if err := validateDestination(destination); err != nil {
			return err
		}
		err := os.Rename(source, destination)
		if err == nil {
			return nil
		}
		transient := errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
		if !transient || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
