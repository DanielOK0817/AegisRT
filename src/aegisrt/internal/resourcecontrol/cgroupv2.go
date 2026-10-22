package resourcecontrol

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aegisrt/internal/domain"
)

var (
	ErrInvalidAttemptID    = errors.New("invalid attempt id")
	ErrInvalidNamespace    = errors.New("invalid runtime namespace")
	ErrInvalidLimits       = errors.New("invalid resource limits")
	ErrUnsupportedPlatform = errors.New("cgroup process placement is supported only on Linux")
)

const (
	defaultCPUPeriodMicros    int64 = 100_000
	defaultCgroupEmptyTimeout       = 2 * time.Second
	defaultCgroupPollInterval       = 10 * time.Millisecond
)

var attemptIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Limits maps an Agent attempt's Runtime resources onto Linux cgroup v2.
// AI-specific resources such as LLM slots remain enforced by the scheduler.
type Limits struct {
	Resources       domain.ResourceVector
	PIDsMax         int64
	CPUPeriodMicros int64
	DisableSwap     bool
}

func (l Limits) Validate() error {
	if err := l.Resources.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLimits, err)
	}
	if l.PIDsMax < 0 || l.CPUPeriodMicros < 0 {
		return fmt.Errorf("%w: pids_max and cpu_period_micros must be non-negative", ErrInvalidLimits)
	}
	if l.Resources.MemoryMiB > math.MaxInt64/(1024*1024) {
		return fmt.Errorf("%w: memory_mib overflows bytes", ErrInvalidLimits)
	}
	return nil
}

type CgroupStats struct {
	CPUUsageMicros     int64            `json:"cpu_usage_micros"`
	CPUUserMicros      int64            `json:"cpu_user_micros"`
	CPUSystemMicros    int64            `json:"cpu_system_micros"`
	MemoryCurrentBytes int64            `json:"memory_current_bytes"`
	MemoryEvents       map[string]int64 `json:"memory_events"`
}

// CgroupV2Driver manages only the AegisRT subtree beneath root.
// In production root is normally /sys/fs/cgroup; tests use a temporary tree.
type CgroupV2Driver struct {
	root      string
	namespace string
}

func NewCgroupV2Driver(root, namespace string) (*CgroupV2Driver, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("cgroup root is required")
	}
	if !attemptIDPattern.MatchString(namespace) {
		return nil, ErrInvalidNamespace
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve cgroup root: %w", err)
	}
	return &CgroupV2Driver{root: filepath.Clean(absRoot), namespace: namespace}, nil
}

func (d *CgroupV2Driver) Create(attemptID string, limits Limits) (_ string, createErr error) {
	groupPath, err := d.groupPath(attemptID)
	if err != nil {
		return "", err
	}
	if err := limits.Validate(); err != nil {
		return "", err
	}
	controls, err := controlValues(limits)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(groupPath), 0o755); err != nil {
		return "", fmt.Errorf("create cgroup parent: %w", err)
	}
	// Exclusive creation establishes cleanup ownership. A repeated attempt ID
	// must not change limits or kill processes belonging to an existing group.
	if err := os.Mkdir(groupPath, 0o755); err != nil {
		return "", fmt.Errorf("create cgroup: %w", err)
	}
	defer func() {
		if createErr != nil {
			createErr = errors.Join(createErr, d.Destroy(attemptID))
		}
	}()
	for name, value := range controls {
		if err := os.WriteFile(filepath.Join(groupPath, name), []byte(value), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}
	return groupPath, nil
}

func (d *CgroupV2Driver) Attach(attemptID string, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("pid must be positive")
	}
	groupPath, err := d.groupPath(attemptID)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(groupPath, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return fmt.Errorf("attach pid: %w", err)
	}
	return nil
}

// Destroy kills remaining processes when cgroup.kill is available and removes
// the attempt cgroup. Cleanup never recursively removes filesystem paths.
func (d *CgroupV2Driver) Destroy(attemptID string) error {
	return d.destroy(attemptID, defaultCgroupEmptyTimeout, defaultCgroupPollInterval)
}

func (d *CgroupV2Driver) destroy(attemptID string, emptyTimeout, pollInterval time.Duration) error {
	groupPath, err := d.groupPath(attemptID)
	if err != nil {
		return err
	}
	if err := terminateCgroup(groupPath); err != nil {
		return err
	}
	if err := waitCgroupEmpty(groupPath, emptyTimeout, pollInterval); err != nil {
		return err
	}
	var removeErr error
	for attempt := 0; attempt < 20; attempt++ {
		removeErr = os.Remove(groupPath)
		if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
			return nil
		}
		if !errors.Is(removeErr, syscall.EBUSY) && !errors.Is(removeErr, syscall.ENOTEMPTY) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("remove cgroup: %w", removeErr)
}

func waitCgroupEmpty(groupPath string, timeout, pollInterval time.Duration) error {
	eventsPath := filepath.Join(groupPath, "cgroup.events")
	if _, err := os.Stat(eventsPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect cgroup.events: %w", err)
		}
		if _, groupErr := os.Stat(groupPath); errors.Is(groupErr, os.ErrNotExist) {
			return nil
		} else if groupErr != nil {
			return fmt.Errorf("inspect cgroup: %w", groupErr)
		}
		isCgroupV2, fsErr := isCgroupV2Filesystem(groupPath)
		if fsErr != nil {
			return fmt.Errorf("inspect cgroup filesystem: %w", fsErr)
		}
		if isCgroupV2 {
			return errors.New("inspect cgroup.events: file is missing on cgroup v2 filesystem")
		}
		return nil
	}
	return waitCgroupEmptyWithReader(eventsPath, timeout, pollInterval, readCgroupPopulated)
}

func waitCgroupEmptyWithReader(
	eventsPath string,
	timeout time.Duration,
	pollInterval time.Duration,
	readPopulated func(string) (bool, error),
) error {
	if timeout <= 0 {
		return errors.New("cgroup empty timeout must be positive")
	}
	if pollInterval <= 0 {
		return errors.New("cgroup poll interval must be positive")
	}
	if readPopulated == nil {
		return errors.New("cgroup populated reader is required")
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		populated, err := readPopulated(eventsPath)
		if err != nil {
			return fmt.Errorf("read cgroup.events: %w", err)
		}
		if !populated {
			return nil
		}
		select {
		case <-deadline.C:
			return errors.New("cgroup remained populated before cleanup deadline")
		case <-ticker.C:
		}
	}
}

func readCgroupPopulated(path string) (bool, error) {
	events, err := readKeyValues(path)
	if err != nil {
		return false, err
	}
	populated, exists := events["populated"]
	if !exists {
		return false, errors.New("populated is missing")
	}
	switch populated {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("invalid populated value %d", populated)
	}
}

func (d *CgroupV2Driver) Stats(attemptID string) (CgroupStats, error) {
	groupPath, err := d.groupPath(attemptID)
	if err != nil {
		return CgroupStats{}, err
	}
	cpu, err := readKeyValues(filepath.Join(groupPath, "cpu.stat"))
	if err != nil {
		return CgroupStats{}, fmt.Errorf("read cpu stats: %w", err)
	}
	usage, exists := cpu["usage_usec"]
	if !exists {
		return CgroupStats{}, errors.New("read cpu stats: usage_usec is missing")
	}
	memoryCurrent, err := readInt64(filepath.Join(groupPath, "memory.current"))
	if err != nil {
		return CgroupStats{}, fmt.Errorf("read memory current: %w", err)
	}
	memoryEvents, err := readKeyValues(filepath.Join(groupPath, "memory.events"))
	if err != nil {
		return CgroupStats{}, fmt.Errorf("read memory events: %w", err)
	}
	return CgroupStats{
		CPUUsageMicros:     usage,
		CPUUserMicros:      cpu["user_usec"],
		CPUSystemMicros:    cpu["system_usec"],
		MemoryCurrentBytes: memoryCurrent,
		MemoryEvents:       memoryEvents,
	}, nil
}

func (d *CgroupV2Driver) groupPath(attemptID string) (string, error) {
	if !attemptIDPattern.MatchString(attemptID) {
		return "", ErrInvalidAttemptID
	}
	base := filepath.Join(d.root, "aegisrt", d.namespace)
	target := filepath.Join(base, attemptID)
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrInvalidAttemptID
	}
	return target, nil
}

func controlValues(limits Limits) (map[string]string, error) {
	period := limits.CPUPeriodMicros
	if period == 0 {
		period = defaultCPUPeriodMicros
	}
	cpuMax := "max " + strconv.FormatInt(period, 10)
	if limits.Resources.CPUMilli > 0 {
		if limits.Resources.CPUMilli > math.MaxInt64/period {
			return nil, fmt.Errorf("%w: cpu quota overflows", ErrInvalidLimits)
		}
		quota := limits.Resources.CPUMilli * period / 1000
		if quota < 1000 {
			quota = 1000
		}
		cpuMax = strconv.FormatInt(quota, 10) + " " + strconv.FormatInt(period, 10)
	}
	memoryMax := "max"
	memorySwapMax := "max"
	if limits.Resources.MemoryMiB > 0 {
		memoryMax = strconv.FormatInt(limits.Resources.MemoryMiB*1024*1024, 10)
	}
	if limits.DisableSwap {
		memorySwapMax = "0"
	}
	pidsMax := "max"
	if limits.PIDsMax > 0 {
		pidsMax = strconv.FormatInt(limits.PIDsMax, 10)
	}
	return map[string]string{
		"cpu.max":         cpuMax,
		"memory.max":      memoryMax,
		"memory.swap.max": memorySwapMax,
		"pids.max":        pidsMax,
	}, nil
}

func readInt64(path string) (int64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse integer: %w", err)
	}
	return value, nil
}

func readKeyValues(path string) (map[string]int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make(map[string]int64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid line %q", scanner.Text())
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", fields[0], err)
		}
		result[fields[0]] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
