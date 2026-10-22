//go:build !linux

package resourcecontrol

func terminateCgroup(string) error { return nil }
