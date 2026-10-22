package resourcecontrol

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	fixedCgroupRemovalAttempts = 10
	fixedCgroupRemovalBackoff  = 10 * time.Millisecond
)

var (
	ErrUnsafeCgroupHierarchy    = errors.New("unsafe cgroup hierarchy")
	ErrCgroupHierarchyPopulated = errors.New("cgroup hierarchy remains populated")
)

type fixedAttemptHierarchy struct {
	attempt          string
	manager          string
	runtimeRoot      string
	runtimeNamespace string
}

type fixedCgroupLevel struct {
	path     string
	allowed  map[string]bool
	required bool
}

// ReclaimAttemptHierarchy removes only the fixed nested hierarchy created for
// a benchmark daemon. It validates the complete hierarchy before mutation.
func (d *CgroupV2Driver) ReclaimAttemptHierarchy(attemptID, runtimeNamespace string) error {
	return d.reclaimAttemptHierarchy(attemptID, runtimeNamespace, os.Remove)
}

func (d *CgroupV2Driver) reclaimAttemptHierarchy(
	attemptID string,
	runtimeNamespace string,
	removeDirectory func(string) error,
) error {
	paths, err := d.fixedAttemptHierarchy(attemptID, runtimeNamespace)
	if err != nil {
		return err
	}
	if removeDirectory == nil {
		return ErrUnsafeCgroupHierarchy
	}
	exists, err := realDirectoryExists(paths.attempt)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	levels := []fixedCgroupLevel{
		{path: paths.attempt, allowed: allowedDirectories("manager", "aegisrt"), required: true},
		{path: paths.manager},
		{path: paths.runtimeRoot, allowed: allowedDirectories(runtimeNamespace)},
		{path: paths.runtimeNamespace},
	}
	existing := make(map[string]bool, len(levels))
	for _, level := range levels {
		present, inspectErr := inspectFixedCgroupLevel(level)
		if inspectErr != nil {
			return inspectErr
		}
		existing[level.path] = present
	}

	for _, path := range []string{paths.runtimeNamespace, paths.runtimeRoot, paths.manager, paths.attempt} {
		if !existing[path] {
			continue
		}
		if err := retryFixedCgroupRemoval(path, removeDirectory); err != nil {
			return ErrUnsafeCgroupHierarchy
		}
	}
	return nil
}

func retryFixedCgroupRemoval(path string, removeDirectory func(string) error) error {
	var err error
	for attempt := 0; attempt < fixedCgroupRemovalAttempts; attempt++ {
		err = removeDirectory(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if !errors.Is(err, syscall.EBUSY) && !errors.Is(err, syscall.ENOTEMPTY) {
			return err
		}
		if attempt+1 < fixedCgroupRemovalAttempts {
			time.Sleep(fixedCgroupRemovalBackoff)
		}
	}
	return err
}

func (d *CgroupV2Driver) fixedAttemptHierarchy(
	attemptID string,
	runtimeNamespace string,
) (fixedAttemptHierarchy, error) {
	if d == nil {
		return fixedAttemptHierarchy{}, ErrUnsafeCgroupHierarchy
	}
	if !attemptIDPattern.MatchString(runtimeNamespace) {
		return fixedAttemptHierarchy{}, ErrInvalidNamespace
	}
	if err := d.validateFixedBase(); err != nil {
		return fixedAttemptHierarchy{}, err
	}
	attempt, err := d.groupPath(attemptID)
	if err != nil {
		return fixedAttemptHierarchy{}, err
	}
	return fixedAttemptHierarchy{
		attempt:          attempt,
		manager:          filepath.Join(attempt, "manager"),
		runtimeRoot:      filepath.Join(attempt, "aegisrt"),
		runtimeNamespace: filepath.Join(attempt, "aegisrt", runtimeNamespace),
	}, nil
}

func (d *CgroupV2Driver) validateFixedBase() error {
	resolvedRoot, err := filepath.EvalSymlinks(d.root)
	if err != nil || filepath.Clean(resolvedRoot) != filepath.Clean(d.root) {
		return ErrUnsafeCgroupHierarchy
	}
	for _, path := range []string{
		d.root,
		filepath.Join(d.root, "aegisrt"),
		filepath.Join(d.root, "aegisrt", d.namespace),
	} {
		exists, err := realDirectoryExists(path)
		if err != nil || !exists {
			return ErrUnsafeCgroupHierarchy
		}
	}
	return nil
}

func inspectFixedCgroupLevel(level fixedCgroupLevel) (bool, error) {
	exists, err := inspectFixedCgroupDirectory(level)
	if err != nil || !exists {
		return exists, err
	}
	if err := ensureFixedCgroupEmpty(level.path); err != nil {
		return false, err
	}
	return true, nil
}

func inspectFixedCgroupDirectory(level fixedCgroupLevel) (bool, error) {
	exists, err := realDirectoryExists(level.path)
	if err != nil {
		return false, err
	}
	if !exists {
		if level.required {
			return false, ErrUnsafeCgroupHierarchy
		}
		return false, nil
	}
	entries, err := os.ReadDir(level.path)
	if err != nil {
		return false, ErrUnsafeCgroupHierarchy
	}
	for _, entry := range entries {
		entryType := entry.Type()
		if entryType&os.ModeSymlink != 0 {
			return false, ErrUnsafeCgroupHierarchy
		}
		if entry.IsDir() {
			if level.allowed == nil || !level.allowed[entry.Name()] {
				return false, ErrUnsafeCgroupHierarchy
			}
			continue
		}
		if !entryType.IsRegular() {
			return false, ErrUnsafeCgroupHierarchy
		}
	}
	return true, nil
}

func ensureFixedCgroupEmpty(path string) error {
	eventsPath := filepath.Join(path, "cgroup.events")
	eventsExist := true
	populated, err := readCgroupPopulated(eventsPath)
	if errors.Is(err, os.ErrNotExist) {
		eventsExist = false
	} else if err != nil {
		return ErrUnsafeCgroupHierarchy
	} else if populated {
		return ErrCgroupHierarchyPopulated
	}
	processes, processErr := os.ReadFile(filepath.Join(path, "cgroup.procs"))
	if processErr == nil && strings.TrimSpace(string(processes)) != "" {
		return ErrCgroupHierarchyPopulated
	}
	if processErr != nil && !errors.Is(processErr, os.ErrNotExist) {
		return ErrUnsafeCgroupHierarchy
	}
	if !eventsExist || errors.Is(processErr, os.ErrNotExist) {
		isCgroupV2, fsErr := isCgroupV2Filesystem(path)
		if fsErr != nil || isCgroupV2 {
			return ErrUnsafeCgroupHierarchy
		}
	}
	return nil
}

func realDirectoryExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, ErrUnsafeCgroupHierarchy
	}
	return true, nil
}

func allowedDirectories(names ...string) map[string]bool {
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	return allowed
}
