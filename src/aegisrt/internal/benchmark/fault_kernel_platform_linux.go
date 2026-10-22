//go:build linux

package benchmark

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"aegisrt/internal/resourcecontrol"
)

const (
	faultKernelProcRoot            = "/proc"
	faultKernelCgroupMount         = "/sys/fs/cgroup"
	faultKernelSupervisorNamespace = "f4-supervisor-v1"
	faultKernelMaximumProcEntries  = 262144
)

type faultKernelPlatform struct {
	cgroupRoot          string
	runtimeDirectory    string
	benchmarkExecutable string
	daemonExecutable    string
	environment         Environment
	software            Software
	supervisorDriver    *resourcecontrol.CgroupV2Driver
}

func prepareFaultKernelPlatform(requireClean bool) (*faultKernelPlatform, error) {
	if !validFaultKernelInvocationID(os.Getenv("INVOCATION_ID")) {
		return nil, faultKernelDaemonFailure("systemd-service")
	}
	runtimeDirectory, err := validateFaultKernelRuntimeDirectory(
		os.Getenv("RUNTIME_DIRECTORY"), uint32(os.Geteuid()),
	)
	if err != nil {
		return nil, err
	}
	root, err := resourcecontrol.ResolveSelfCgroupV2Root(
		filepath.Join(faultKernelProcRoot, "self", "cgroup"), faultKernelCgroupMount,
	)
	if err != nil || !strings.HasSuffix(filepath.Base(root), ".service") {
		return nil, faultKernelDaemonFailure("delegated-service-root")
	}
	benchmarkExecutable, daemonExecutable, err := faultKernelSiblingExecutables()
	if err != nil {
		return nil, err
	}
	configHash, err := faultKernelRuntimeConfigHash(fixedFaultKernelRuntimeConfig())
	if err != nil {
		return nil, faultKernelDaemonFailure("runtime-config-identity")
	}
	software, err := collectSoftware(configHash)
	if err != nil {
		return nil, faultKernelDaemonFailure("software-identity")
	}
	if requireClean && software.RuntimeDirty {
		return nil, faultKernelDaemonFailure("dirty-runtime")
	}
	supervisor, err := resourcecontrol.PrepareDelegatedCgroupV2(
		root, faultKernelSupervisorNamespace, os.Getpid(),
	)
	if err != nil {
		return nil, faultKernelDaemonFailure("supervisor-delegation")
	}
	if err := supervisor.EnsureNamespaceEmpty(); err != nil {
		return nil, faultKernelDaemonFailure("supervisor-namespace-not-empty")
	}
	environment, err := collectHostOpenEulerEnvironment(root)
	if err != nil {
		return nil, err
	}
	return &faultKernelPlatform{
		cgroupRoot: root, runtimeDirectory: runtimeDirectory,
		benchmarkExecutable: benchmarkExecutable, daemonExecutable: daemonExecutable,
		environment: environment, software: software, supervisorDriver: supervisor,
	}, nil
}

func (platform *faultKernelPlatform) cleanup() error {
	if platform == nil {
		return nil
	}
	return platform.supervisorDriver.EnsureNamespaceEmpty()
}

func faultKernelSiblingExecutables() (string, string, error) {
	benchmarkExecutable, err := os.Executable()
	if err != nil {
		return "", "", faultKernelDaemonFailure("executable-location")
	}
	benchmarkExecutable, err = filepath.Abs(benchmarkExecutable)
	if err != nil {
		return "", "", faultKernelDaemonFailure("executable-validation")
	}
	info, err := os.Lstat(benchmarkExecutable)
	if err != nil || validateFaultKernelDaemonExecutable(info) != nil {
		return "", "", faultKernelDaemonFailure("executable-validation")
	}
	daemonExecutable, err := locateFaultKernelDaemon(benchmarkExecutable)
	if err != nil || filepath.Dir(daemonExecutable) != filepath.Dir(benchmarkExecutable) {
		return "", "", faultKernelDaemonFailure("daemon-sibling")
	}
	return benchmarkExecutable, daemonExecutable, nil
}

func validFaultKernelInvocationID(value string) bool {
	return len(value) == 32 && isLowerHex(value)
}

func validateFaultKernelRuntimeDirectory(path string, uid uint32) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.Contains(path, ":") {
		return "", faultKernelDaemonFailure("runtime-directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", faultKernelDaemonFailure("runtime-directory")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o700 != 0o700 {
		return "", faultKernelDaemonFailure("runtime-directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid {
		return "", faultKernelDaemonFailure("runtime-directory-owner")
	}
	return resolved, nil
}

func createFaultKernelSession(root, runID string) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	path := filepath.Join(root, runID)
	if err := os.Mkdir(path, 0o700); err != nil {
		return "", faultKernelDaemonFailure("session-directory")
	}
	return path, nil
}

func createFaultKernelTrialPaths(session string, index int) (faultKernelTrialPaths, error) {
	directory := filepath.Join(session, fmt.Sprintf("trial-%03d", index))
	ready := filepath.Join(directory, "ready")
	if index < 0 || index > 999 || os.Mkdir(directory, 0o700) != nil || os.Mkdir(ready, 0o700) != nil {
		return faultKernelTrialPaths{}, faultKernelDaemonFailure("trial-directory")
	}
	return faultKernelTrialPaths{
		registry: filepath.Join(directory, "agents.json"), token: filepath.Join(directory, "token"),
		state: filepath.Join(directory, "runtime.state"), ready: ready,
	}, nil
}

func newFaultKernelToken() ([]byte, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return nil, faultKernelDaemonFailure("token-generation")
	}
	token := make([]byte, hex.EncodedLen(len(random)))
	hex.Encode(token, random)
	for index := range random {
		random[index] = 0
	}
	return token, nil
}

func faultKernelMarkerReady(directory, attemptID string) (bool, error) {
	path := filepath.Join(directory, attemptID+".ready")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != 6 {
		return false, faultKernelDaemonFailure("readiness-marker")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "READY\n" {
		return false, faultKernelDaemonFailure("readiness-marker")
	}
	return true, nil
}
