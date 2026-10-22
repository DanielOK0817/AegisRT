//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func agentIdentity(pid int) (string, string, bool, error) {
	script := fmt.Sprintf("$p = Get-CimInstance Win32_Process -Filter 'ProcessId=%d'; if ($null -eq $p) { 'null' } else { $p | Select-Object ExecutablePath,CommandLine | ConvertTo-Json -Compress }", pid)
	data, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return "", "", false, err
	}
	var process *struct {
		ExecutablePath string
		CommandLine    string
	}
	if err := json.Unmarshal(data, &process); err != nil {
		return "", "", false, err
	}
	if process == nil {
		return "", "", false, nil
	}
	return process.ExecutablePath, process.CommandLine, true, nil
}

func killRegisteredAgent(pid int, binary, dir string) (bool, error) {
	path, command, alive, err := agentIdentity(pid)
	if err != nil || !alive {
		return false, err
	}
	if !strings.EqualFold(filepath.Clean(path), filepath.Clean(binary)) || !strings.Contains(command, dir) || !strings.Contains(command, "-mode agent") {
		return false, errors.New("refusing to kill an unrecognized experiment child")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	defer process.Release()
	if err := process.Kill(); err != nil {
		return false, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, alive, err = agentIdentity(pid)
		if err != nil {
			return true, err
		}
		if !alive {
			return true, nil
		}
		if time.Now().After(deadline) {
			return true, errors.New("experiment child remained after kill")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
