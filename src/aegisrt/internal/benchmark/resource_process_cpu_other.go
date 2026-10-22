//go:build !linux

package benchmark

import "time"

func resourceProcessCPUTime() (time.Duration, bool) {
	return 0, false
}
