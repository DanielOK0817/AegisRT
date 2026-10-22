package resourcecontrol

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxCgroupObservationFileBytes = 64 << 10

// CgroupControllerReadback contains only the three configured controller
// values needed by F4 evidence. Nil values mean that the cgroup did not exist.
type CgroupControllerReadback struct {
	CPUMax    *string `json:"cpu_max"`
	MemoryMax *string `json:"memory_max"`
	PIDsMax   *string `json:"pids_max"`
}

// CgroupObservation is an anonymous, read-only cgroup observation. It never
// contains process identifiers or filesystem paths. When Exists is false all
// pointer fields are nil; an existing but incomplete group returns an error.
type CgroupObservation struct {
	Exists             bool                     `json:"exists"`
	Populated          *bool                    `json:"populated"`
	MemberCount        *int                     `json:"member_count"`
	ControllerReadback CgroupControllerReadback `json:"controller_readback"`
}

// Observe reads one validated attempt cgroup without mutating it.
func (d *CgroupV2Driver) Observe(attemptID string) (CgroupObservation, error) {
	if d == nil {
		return CgroupObservation{}, errors.New("cgroup driver is required")
	}
	groupPath, err := d.groupPath(attemptID)
	if err != nil {
		return CgroupObservation{}, err
	}
	info, err := os.Lstat(groupPath)
	if errors.Is(err, os.ErrNotExist) {
		return CgroupObservation{}, nil
	}
	if err != nil {
		return CgroupObservation{}, errors.New("inspect attempt cgroup failed")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return CgroupObservation{}, errors.New("attempt cgroup is not a directory")
	}

	populated, err := observePopulated(filepath.Join(groupPath, "cgroup.events"))
	if err != nil {
		return CgroupObservation{}, fmt.Errorf("observe cgroup events: %w", err)
	}
	members, err := observeMemberCount(filepath.Join(groupPath, "cgroup.procs"))
	if err != nil {
		return CgroupObservation{}, fmt.Errorf("observe cgroup members: %w", err)
	}
	cpuMax, err := observeCPUController(filepath.Join(groupPath, "cpu.max"))
	if err != nil {
		return CgroupObservation{}, fmt.Errorf("observe cpu controller: %w", err)
	}
	memoryMax, err := observeScalarController(filepath.Join(groupPath, "memory.max"))
	if err != nil {
		return CgroupObservation{}, fmt.Errorf("observe memory controller: %w", err)
	}
	pidsMax, err := observeScalarController(filepath.Join(groupPath, "pids.max"))
	if err != nil {
		return CgroupObservation{}, fmt.Errorf("observe pids controller: %w", err)
	}
	return CgroupObservation{
		Exists: true, Populated: &populated, MemberCount: &members,
		ControllerReadback: CgroupControllerReadback{
			CPUMax: &cpuMax, MemoryMax: &memoryMax, PIDsMax: &pidsMax,
		},
	}, nil
}

func observePopulated(path string) (bool, error) {
	content, err := readObservationFile(path)
	if err != nil {
		return false, err
	}
	values := make(map[string]uint64)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return false, errors.New("cgroup.events contains an invalid line")
		}
		if _, exists := values[fields[0]]; exists {
			return false, errors.New("cgroup.events contains a duplicate key")
		}
		value, parseErr := parseKernelUint(fields[1], true)
		if parseErr != nil {
			return false, errors.New("cgroup.events contains an invalid value")
		}
		values[fields[0]] = value
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	value, exists := values["populated"]
	if !exists || value > 1 {
		return false, errors.New("cgroup.events has no valid populated value")
	}
	return value == 1, nil
}

func observeMemberCount(path string) (int, error) {
	content, err := readObservationFileAllowEmpty(path)
	if err != nil {
		return 0, err
	}
	seen := make(map[uint64]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		value, parseErr := parseKernelUint(strings.TrimSpace(scanner.Text()), false)
		if parseErr != nil {
			return 0, errors.New("cgroup.procs contains an invalid member")
		}
		if _, exists := seen[value]; exists {
			return 0, errors.New("cgroup.procs contains a duplicate member")
		}
		seen[value] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return len(seen), nil
}

func observeCPUController(path string) (string, error) {
	content, err := readObservationFile(path)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(content))
	if len(fields) != 2 {
		return "", errors.New("cpu.max has an invalid shape")
	}
	quota := fields[0]
	if quota != "max" {
		if _, err := parseKernelUint(quota, false); err != nil {
			return "", errors.New("cpu.max has an invalid quota")
		}
	}
	if _, err := parseKernelUint(fields[1], false); err != nil {
		return "", errors.New("cpu.max has an invalid period")
	}
	return quota + " " + fields[1], nil
}

func observeScalarController(path string) (string, error) {
	content, err := readObservationFile(path)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(content))
	if len(fields) != 1 {
		return "", errors.New("controller value has an invalid shape")
	}
	if fields[0] != "max" {
		if _, err := parseKernelUint(fields[0], true); err != nil {
			return "", errors.New("controller value is invalid")
		}
	}
	return fields[0], nil
}

func parseKernelUint(value string, allowZero bool) (uint64, error) {
	if value == "" {
		return 0, errors.New("integer is empty")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, errors.New("integer is not decimal")
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || !allowZero && parsed == 0 || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("integer is outside the canonical range")
	}
	return parsed, nil
}

func readObservationFile(path string) ([]byte, error) {
	content, err := readObservationFileAllowEmpty(path)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, errors.New("kernel file is empty")
	}
	return content, nil
}

func readObservationFileAllowEmpty(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("kernel file is unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("kernel file is not regular")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("kernel file is unavailable")
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxCgroupObservationFileBytes+1))
	if err != nil {
		return nil, errors.New("kernel file could not be read")
	}
	if len(content) > maxCgroupObservationFileBytes || bytes.IndexByte(content, 0) >= 0 {
		return nil, errors.New("kernel file is oversized or contains NUL")
	}
	return content, nil
}
