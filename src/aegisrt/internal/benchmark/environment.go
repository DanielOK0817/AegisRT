package benchmark

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func defaultCollectorDeps() collectorDeps {
	return collectorDeps{now: time.Now, runID: randomRunID, environment: currentEnvironment, software: collectSoftware}
}

func collectSoftware(configHash string) (Software, error) {
	executable, err := os.Executable()
	if err != nil {
		return Software{}, fmt.Errorf("resolve executable: %w", err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return Software{}, fmt.Errorf("read executable: %w", err)
	}
	binarySum := sha256.Sum256(binary)
	commit, dirty, ok := revisionFromBuild(executable)
	if !ok {
		commit, dirty, err = revisionFromGit()
		if err != nil {
			return Software{}, err
		}
	}
	if len(commit) != 40 || !isLowerHex(commit) {
		return Software{}, fmt.Errorf("runtime commit is not a full lowercase Git hash")
	}
	return Software{
		RuntimeCommit: commit, RuntimeDirty: dirty, GoVersion: runtime.Version(),
		BinarySHA256: hex.EncodeToString(binarySum[:]), ConfigSHA256: configHash,
	}, nil
}

func revisionFromBuild(executable string) (string, bool, bool) {
	info, err := buildinfo.ReadFile(executable)
	if err != nil {
		return "", false, false
	}
	var revision string
	var dirty bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	return revision, dirty, len(revision) == 40
}

func revisionFromGit() (string, bool, error) {
	revisionOutput, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false, fmt.Errorf("read Git revision: %w", err)
	}
	statusOutput, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return "", false, fmt.Errorf("read Git status: %w", err)
	}
	return strings.TrimSpace(string(revisionOutput)), len(statusOutput) != 0, nil
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func architecture() string {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH
	default:
		return "other"
	}
}
