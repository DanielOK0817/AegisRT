//go:build linux

package benchmark

import "errors"

func validateResourceKernelTrialResult(controller string, requested int64, stats resourceKernelStats, waitErr error) error {
	if controller == "memory" && requested > 0 {
		if waitErr == nil || stats.oomKills == 0 {
			return errors.New("memory ceiling was not observed")
		}
		return nil
	}
	if controller == "pids" && requested > 0 && requested < 64 {
		if stats.pidMax == 0 {
			return errors.New("PID ceiling was not observed")
		}
		return nil
	}
	if waitErr != nil {
		return waitErr
	}
	if controller == "cpu" && requested > 0 && stats.throttled == 0 {
		return errors.New("CPU throttling was not observed")
	}
	return nil
}
