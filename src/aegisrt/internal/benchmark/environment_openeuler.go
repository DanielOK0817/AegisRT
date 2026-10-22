package benchmark

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const maxEnvironmentFileBytes = 64 << 10

var (
	distributionVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._ -]{1,64}$`)
	kernelReleasePattern       = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)
)

type openEulerEnvironmentPaths struct {
	OSRelease            string
	KernelRelease        string
	MemInfo              string
	CgroupControllers    string
	CgroupSubtreeControl string
	PSICPU               string
	PSIMemory            string
	PSIIO                string
	CgroupV2             bool
}

func collectOpenEulerEnvironment(
	paths openEulerEnvironmentPaths,
	architectureName string,
	logicalCPUCount int,
) (Environment, error) {
	if !paths.CgroupV2 {
		return Environment{}, errors.New("openEuler kernel evidence requires cgroup v2")
	}
	if architectureName != "amd64" && architectureName != "arm64" && architectureName != "other" {
		return Environment{}, errors.New("unsupported architecture identity")
	}
	if logicalCPUCount <= 0 {
		return Environment{}, errors.New("logical CPU count must be positive")
	}
	osRelease, err := readBoundedEnvironmentFile(paths.OSRelease)
	if err != nil {
		return Environment{}, fmt.Errorf("read operating system identity: %w", err)
	}
	distributionID, distributionVersion, err := parseOpenEulerRelease(osRelease)
	if err != nil {
		return Environment{}, err
	}
	kernelData, err := readBoundedEnvironmentFile(paths.KernelRelease)
	if err != nil {
		return Environment{}, fmt.Errorf("read kernel release: %w", err)
	}
	kernelRelease := strings.TrimSpace(string(kernelData))
	if !kernelReleasePattern.MatchString(kernelRelease) {
		return Environment{}, errors.New("kernel release has an unsafe shape")
	}
	memoryData, err := readBoundedEnvironmentFile(paths.MemInfo)
	if err != nil {
		return Environment{}, fmt.Errorf("read memory information: %w", err)
	}
	memoryTotal, err := parseMemoryTotal(memoryData)
	if err != nil {
		return Environment{}, err
	}
	controllers, err := collectDelegatedControllers(paths)
	if err != nil {
		return Environment{}, err
	}
	for _, path := range []string{paths.PSICPU, paths.PSIMemory, paths.PSIIO} {
		content, readErr := readBoundedEnvironmentFile(path)
		if readErr != nil || !hasPressureSample(content) {
			return Environment{}, errors.New("openEuler kernel evidence requires readable PSI samples")
		}
	}
	result := Environment{
		EvidenceScope: "openeuler-kernel", OSFamily: "linux",
		DistributionID: distributionID, DistributionVersion: distributionVersion,
		KernelRelease: kernelRelease, Architecture: architectureName,
		LogicalCPUCount: logicalCPUCount, MemoryTotalBytes: memoryTotal,
		Cgroup: CgroupEnvironment{
			Version: "v2", Controllers: controllers, Delegation: "delegated", PSI: "available",
		},
	}
	if err := validateEnvironmentForScope(result.EvidenceScope, result); err != nil {
		return Environment{}, err
	}
	return result, nil
}

func readBoundedEnvironmentFile(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("environment source path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxEnvironmentFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) == 0 || len(content) > maxEnvironmentFileBytes || strings.IndexByte(string(content), 0) >= 0 {
		return nil, errors.New("environment source is empty, oversized, or contains NUL")
	}
	return content, nil
}

func parseOpenEulerRelease(content []byte) (string, string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || (key != "ID" && key != "VERSION_ID") {
			continue
		}
		if _, exists := values[key]; exists {
			return "", "", fmt.Errorf("operating system identity repeats %s", key)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' ||
			value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	if !strings.EqualFold(values["ID"], "openEuler") {
		return "", "", errors.New("operating system is not openEuler")
	}
	if !distributionVersionPattern.MatchString(values["VERSION_ID"]) {
		return "", "", errors.New("openEuler version has an unsafe shape")
	}
	return "openEuler", values["VERSION_ID"], nil
}

func parseMemoryTotal(content []byte) (uint64, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "MemTotal:" {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return 0, errors.New("MemTotal has an invalid shape")
		}
		kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kilobytes == 0 || kilobytes > math.MaxUint64/1024 {
			return 0, errors.New("MemTotal is invalid")
		}
		return kilobytes * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("MemTotal is missing")
}

func collectDelegatedControllers(paths openEulerEnvironmentPaths) ([]string, error) {
	availableData, err := readBoundedEnvironmentFile(paths.CgroupControllers)
	if err != nil {
		return nil, fmt.Errorf("read cgroup controllers: %w", err)
	}
	enabledData, err := readBoundedEnvironmentFile(paths.CgroupSubtreeControl)
	if err != nil {
		return nil, fmt.Errorf("read delegated controllers: %w", err)
	}
	available := controllerSet(string(availableData))
	enabled := controllerSet(string(enabledData))
	for _, required := range []string{"cpu", "memory", "pids"} {
		if !available[required] || !enabled[required] {
			return nil, fmt.Errorf("required delegated controller %s is unavailable", required)
		}
	}
	controllers := make([]string, 0, 4)
	for _, name := range []string{"cpu", "io", "memory", "pids"} {
		if available[name] && enabled[name] {
			controllers = append(controllers, name)
		}
	}
	sort.Strings(controllers)
	return controllers, nil
}

func controllerSet(content string) map[string]bool {
	result := make(map[string]bool)
	for _, field := range strings.Fields(content) {
		name := strings.TrimPrefix(field, "+")
		if name == "cpu" || name == "memory" || name == "pids" || name == "io" {
			result[name] = true
		}
	}
	return result
}

func hasPressureSample(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "some ") {
			return true
		}
	}
	return false
}
