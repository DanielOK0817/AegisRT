//go:build windows

package executor

import "os/exec"

func configureProcessTree(*exec.Cmd) {}

func waitProcess(command *exec.Cmd) error { return command.Wait() }
