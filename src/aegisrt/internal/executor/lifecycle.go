package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

func prepareExecution(
	ctx context.Context,
	request Request,
	command *exec.Cmd,
	hooks []PrepareHook,
) ([]Cleanup, error) {
	cleanups := make([]Cleanup, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		cleanup, err := hook(ctx, request, command)
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
		if err != nil {
			cleanupErr := runCleanups(cleanups)
			return nil, errors.Join(fmt.Errorf("%w: %w", ErrPrepareHook, err), wrapCleanup(cleanupErr))
		}
	}
	return cleanups, nil
}

func runCleanups(cleanups []Cleanup) error {
	var result error
	for index := len(cleanups) - 1; index >= 0; index-- {
		if cleanups[index] == nil {
			continue
		}
		result = errors.Join(result, cleanups[index]())
	}
	return result
}

func wrapCleanup(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrCleanup, err)
}

func terminateProcess(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	if command.Cancel != nil && command.Cancel() == nil {
		return
	}
	_ = command.Process.Kill()
}
