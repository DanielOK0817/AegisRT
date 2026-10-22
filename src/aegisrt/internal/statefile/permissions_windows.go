//go:build windows

package statefile

import "io/fs"

func validateStatePermissions(fs.FileInfo) error { return nil }
