package benchmark

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"aegisrt/internal/controlplane"
)

func newFaultReadyDirectory() (string, error) {
	directory, err := os.MkdirTemp("", "aegisrt-fault-ready-")
	if err != nil {
		return "", fmt.Errorf("create fault readiness directory: %w", err)
	}
	return filepath.Clean(directory), nil
}

func waitFaultAgentReady(ctx context.Context, directory, taskID string, attempt int) error {
	path := faultReadyPath(directory, taskID, attempt)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var lastReadErr error
	for {
		data, err := os.ReadFile(path)
		switch {
		case err == nil && string(data) == "READY\n":
			return nil
		case err == nil:
			lastReadErr = fmt.Errorf("fault readiness marker is invalid")
		case !os.IsNotExist(err):
			lastReadErr = fmt.Errorf("read fault readiness marker: %w", err)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastReadErr)
		case <-deadline.C:
			return errors.Join(fmt.Errorf("fault readiness marker deadline exceeded"), lastReadErr)
		case <-ticker.C:
		}
	}
}

func cleanupFaultReadyDirectory(directory string, workload faultCaseWorkload) error {
	if directory == "" {
		return nil
	}
	var result error
	for attempt := 1; attempt <= workload.Target.MaxAttempts; attempt++ {
		path := faultReadyPath(directory, workload.Target.TaskID, attempt)
		result = errors.Join(result, removeIfPresent(path), removeIfPresent(path+".tmp"))
	}
	survivorPath := faultReadyPath(directory, workload.Unaffected.TaskID, 1)
	result = errors.Join(result, removeIfPresent(survivorPath), removeIfPresent(survivorPath+".tmp"))
	if err := os.Remove(directory); err != nil {
		result = errors.Join(result, err)
	}
	return result
}

func faultReadyPath(directory, taskID string, attempt int) string {
	return filepath.Join(directory, controlplane.AttemptID(taskID, attempt)+".ready")
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
