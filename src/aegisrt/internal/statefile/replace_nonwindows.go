//go:build !windows

package statefile

import "os"

func replaceStateFile(source, destination string) error {
	return os.Rename(source, destination)
}
