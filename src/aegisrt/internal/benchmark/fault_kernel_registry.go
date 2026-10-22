package benchmark

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"aegisrt/internal/agentconfig"
	"aegisrt/internal/domain"
)

type faultKernelAgentRegistry struct {
	Agents []faultKernelAgentRecord `json:"agents"`
}

type faultKernelAgentRecord struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Runtime          string                `json:"runtime"`
	Entrypoint       string                `json:"entrypoint"`
	Args             []string              `json:"args"`
	Capabilities     []string              `json:"capabilities"`
	Resources        domain.ResourceVector `json:"resources"`
	MaxConcurrency   int                   `json:"max_concurrency"`
	ExecutionTimeout string                `json:"execution_timeout"`
}

func faultKernelRegistryJSON(benchmarkExecutable, readyDirectory string) ([]byte, error) {
	if !filepath.IsAbs(benchmarkExecutable) || !filepath.IsAbs(readyDirectory) {
		return nil, faultKernelDaemonFailure("registry-configuration")
	}
	info, err := os.Lstat(benchmarkExecutable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, faultKernelDaemonFailure("registry-executable")
	}
	readyInfo, err := os.Stat(readyDirectory)
	if err != nil || !readyInfo.IsDir() {
		return nil, faultKernelDaemonFailure("registry-ready-directory")
	}
	config := fixedFaultKernelRuntimeConfig()
	records := []faultKernelAgentRecord{
		faultKernelAgentRecordFor(faultKernelTargetAgentID, "F4 Target Fixture", benchmarkExecutable,
			[]string{faultKernelFixtureCommand, "--mode=success",
				"--hold=" + (time.Duration(config.TargetHoldMS) * time.Millisecond).String(),
				"--spawn-child", "--ready-dir=" + filepath.Clean(readyDirectory)}, config),
		faultKernelAgentRecordFor(faultKernelSurvivorAgentID, "F4 Survivor Fixture", benchmarkExecutable,
			[]string{faultKernelFixtureCommand, "--mode=success"}, config),
	}
	payload, err := json.MarshalIndent(faultKernelAgentRegistry{Agents: records}, "", "  ")
	if err != nil {
		return nil, faultKernelDaemonFailure("registry-encode")
	}
	return append(payload, '\n'), nil
}

func faultKernelAgentRecordFor(
	id, name, executable string,
	args []string,
	config faultKernelRuntimeConfig,
) faultKernelAgentRecord {
	return faultKernelAgentRecord{
		ID: id, Name: name, Runtime: "process", Entrypoint: executable,
		Args: append([]string(nil), args...), Capabilities: []string{"fault.kernel.fixture"},
		Resources: faultKernelTaskResources.Clone(), MaxConcurrency: 1,
		ExecutionTimeout: (time.Duration(config.AgentTimeoutMS) * time.Millisecond).String(),
	}
}

func validateFaultKernelRegistry(path, executable, readyDirectory string) error {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return faultKernelDaemonFailure("registry-load")
	}
	configuration, err := agentconfig.Load(path, agentconfig.Options{
		AgentRoot: filepath.Dir(resolved), StrictPermissions: true,
	})
	if err != nil {
		return faultKernelDaemonFailure("registry-load")
	}
	agents := configuration.Agents()
	want := faultKernelExpectedAgentSpecs(filepath.Clean(resolved), filepath.Clean(readyDirectory))
	if len(agents) != len(want) {
		return faultKernelDaemonFailure("registry-content")
	}
	for index := range want {
		if !sameFaultKernelAgent(agents[index], want[index]) {
			return faultKernelDaemonFailure("registry-content")
		}
	}
	return nil
}

func faultKernelExpectedAgentSpecs(executable, readyDirectory string) []domain.AgentSpec {
	config := fixedFaultKernelRuntimeConfig()
	records := []faultKernelAgentRecord{
		faultKernelAgentRecordFor(faultKernelTargetAgentID, "F4 Target Fixture", executable,
			[]string{faultKernelFixtureCommand, "--mode=success",
				"--hold=" + (time.Duration(config.TargetHoldMS) * time.Millisecond).String(),
				"--spawn-child", "--ready-dir=" + readyDirectory}, config),
		faultKernelAgentRecordFor(faultKernelSurvivorAgentID, "F4 Survivor Fixture", executable,
			[]string{faultKernelFixtureCommand, "--mode=success"}, config),
	}
	result := make([]domain.AgentSpec, len(records))
	for index, record := range records {
		result[index] = domain.AgentSpec{ID: record.ID, Name: record.Name, Runtime: record.Runtime,
			Entrypoint: record.Entrypoint, Args: append([]string(nil), record.Args...),
			Capabilities: append([]string(nil), record.Capabilities...), Resources: record.Resources.Clone(),
			MaxConcurrency:   record.MaxConcurrency,
			ExecutionTimeout: time.Duration(config.AgentTimeoutMS) * time.Millisecond}
	}
	return result
}

func sameFaultKernelAgent(got, want domain.AgentSpec) bool {
	return got.ID == want.ID && got.Name == want.Name && got.Runtime == want.Runtime &&
		got.Entrypoint == want.Entrypoint && slices.Equal(got.Args, want.Args) &&
		slices.Equal(got.Capabilities, want.Capabilities) && sameFaultKernelResources(got.Resources, want.Resources) &&
		got.MaxConcurrency == want.MaxConcurrency && got.ExecutionTimeout == want.ExecutionTimeout
}

func sameFaultKernelResources(got, want domain.ResourceVector) bool {
	return got.CPUMilli == want.CPUMilli && got.MemoryMiB == want.MemoryMiB &&
		got.LLMSlots == want.LLMSlots && got.TokenBudget == want.TokenBudget &&
		maps.Equal(got.ToolSlots, want.ToolSlots)
}

func writeFaultKernelPrivateFile(path string, payload []byte) error {
	if !filepath.IsAbs(path) || len(payload) == 0 {
		return faultKernelDaemonFailure("private-file-configuration")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return faultKernelDaemonFailure("private-file-create")
	}
	writeErr := error(nil)
	if _, writeErr = file.Write(payload); writeErr == nil {
		writeErr = file.Sync()
	}
	writeErr = errors.Join(writeErr, file.Close())
	if writeErr != nil {
		return faultKernelDaemonFailure("private-file-write")
	}
	return nil
}

func faultKernelDaemonArguments(paths faultKernelTrialPaths, root string) []string {
	config := fixedFaultKernelRuntimeConfig()
	return []string{
		"-listen", "127.0.0.1:0", "-agents-file", paths.registry,
		"-auth-token-file", paths.token, "-state-file", paths.state,
		"-scheduler-policy", config.SchedulerPolicy,
		"-cpu-milli", strconv.FormatInt(config.Capacity.CPUMilli, 10),
		"-memory-mib", strconv.FormatInt(config.Capacity.MemoryMiB, 10),
		"-llm-slots", strconv.FormatInt(config.Capacity.LLMSlots, 10),
		"-token-budget", strconv.FormatInt(config.Capacity.TokenBudget, 10),
		"-max-workers", strconv.Itoa(config.MaxWorkers),
		"-poll-interval", (time.Duration(config.PollIntervalMS) * time.Millisecond).String(),
		"-event-capacity", strconv.Itoa(config.EventCapacity),
		"-cgroup-root", root, "-pids-max", strconv.FormatInt(config.PIDsMax, 10),
		"-cpu-period-micros", strconv.FormatInt(config.CPUPeriodMicros, 10),
		"-shutdown-timeout", (time.Duration(config.ShutdownMS) * time.Millisecond).String(),
	}
}

type faultKernelTrialPaths struct {
	registry string
	token    string
	state    string
	ready    string
}
