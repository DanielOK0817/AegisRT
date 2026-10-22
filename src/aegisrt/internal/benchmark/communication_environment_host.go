package benchmark

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

type communicationLinuxPaths struct {
	OSRelease     string
	KernelRelease string
	MemInfo       string
}

var communicationDistributionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// This local host snapshot deliberately makes no cgroup or PSI claim. Kernel
// resource experiments must continue using collectHostOpenEulerEnvironment.
func collectCommunicationLinuxEnvironment(paths communicationLinuxPaths, arch string, cpus int) (Environment, error) {
	if cpus <= 0 || (arch != "amd64" && arch != "arm64" && arch != "other") {
		return Environment{}, fmt.Errorf("invalid local host CPU or architecture identity")
	}
	osRelease, err := readBoundedEnvironmentFile(paths.OSRelease)
	if err != nil {
		return Environment{}, fmt.Errorf("read local operating system identity: %w", err)
	}
	distribution, version, err := parseCommunicationLinuxRelease(osRelease)
	if err != nil {
		return Environment{}, err
	}
	kernel, err := readBoundedEnvironmentFile(paths.KernelRelease)
	if err != nil {
		return Environment{}, fmt.Errorf("read local kernel release: %w", err)
	}
	release := strings.TrimSpace(string(kernel))
	if !kernelReleasePattern.MatchString(release) {
		return Environment{}, fmt.Errorf("local kernel release has an unsafe shape")
	}
	memory, err := readBoundedEnvironmentFile(paths.MemInfo)
	if err != nil {
		return Environment{}, fmt.Errorf("read local memory information: %w", err)
	}
	memoryTotal, err := parseMemoryTotal(memory)
	if err != nil {
		return Environment{}, err
	}
	return Environment{EvidenceScope: "linux-local", OSFamily: "linux", DistributionID: distribution,
		DistributionVersion: version, KernelRelease: release, Architecture: arch, LogicalCPUCount: cpus, MemoryTotalBytes: memoryTotal,
		Cgroup: CgroupEnvironment{Version: "not-measured", Controllers: []string{}, Delegation: "not-requested", PSI: "not-measured"}}, nil
}

func parseCommunicationLinuxRelease(content []byte) (string, string, error) {
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
			return "", "", fmt.Errorf("local operating system identity repeats %s", key)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	if !communicationDistributionPattern.MatchString(values["ID"]) {
		return "", "", fmt.Errorf("local Linux distribution ID is missing or invalid")
	}
	version, exists := values["VERSION_ID"]
	if !exists {
		version = "unspecified"
	}
	if !distributionVersionPattern.MatchString(version) {
		return "", "", fmt.Errorf("local Linux distribution version is invalid")
	}
	return values["ID"], version, nil
}
