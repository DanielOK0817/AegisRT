//go:build windows

package statefile

// Windows does not expose a portable directory fsync operation. The temporary
// file itself is flushed before the atomic replacement.
func syncDirectory(string) error {
	return nil
}
