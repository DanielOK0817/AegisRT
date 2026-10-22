package resourcecontrol

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	ErrInvalidCgroupV2Path = errors.New("invalid cgroup v2 path")
	ErrCgroupV2RootBusy    = errors.New("delegated cgroup v2 root contains processes")
	ErrCgroupV2Controllers = errors.New("required cgroup v2 controllers are unavailable")
)

var delegatedControllers = []string{"cpu", "memory", "pids"}

// ResolveSelfCgroupV2Root resolves the process's unified cgroup beneath a
// cgroup v2 mount. It rejects the mount root and paths that can escape it.
func ResolveSelfCgroupV2Root(procCgroupPath, mountRoot string) (string, error) {
	if strings.TrimSpace(procCgroupPath) == "" || strings.TrimSpace(mountRoot) == "" {
		return "", ErrInvalidCgroupV2Path
	}
	file, err := os.Open(procCgroupPath)
	if err != nil {
		return "", fmt.Errorf("%w: read process cgroup: %v", ErrInvalidCgroupV2Path, err)
	}
	defer file.Close()

	var unifiedPath string
	matched := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 3)
		if len(fields) != 3 || fields[0] != "0" || fields[1] != "" {
			continue
		}
		if matched {
			return "", fmt.Errorf("%w: multiple unified hierarchies", ErrInvalidCgroupV2Path)
		}
		matched = true
		unifiedPath = fields[2]
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("%w: scan process cgroup: %v", ErrInvalidCgroupV2Path, err)
	}
	if !matched || !strings.HasPrefix(unifiedPath, "/") || unifiedPath == "/" {
		return "", ErrInvalidCgroupV2Path
	}
	for _, component := range strings.Split(unifiedPath, "/") {
		if component == "." || component == ".." || strings.ContainsRune(component, '\x00') {
			return "", ErrInvalidCgroupV2Path
		}
	}

	resolvedMount, err := resolveExistingDirectory(mountRoot)
	if err != nil {
		return "", fmt.Errorf("%w: resolve mount root: %v", ErrInvalidCgroupV2Path, err)
	}
	relativePath := filepath.FromSlash(strings.TrimPrefix(unifiedPath, "/"))
	target, err := resolveExistingDirectory(filepath.Join(resolvedMount, relativePath))
	if err != nil {
		return "", fmt.Errorf("%w: resolve delegated root: %v", ErrInvalidCgroupV2Path, err)
	}
	if !isWithin(resolvedMount, target) || target == resolvedMount {
		return "", ErrInvalidCgroupV2Path
	}
	return target, nil
}

// PrepareDelegatedCgroupV2 prepares a no-internal-process hierarchy beneath a
// systemd-delegated cgroup and returns the standard attempt driver for it.
func PrepareDelegatedCgroupV2(root, namespace string, pid int) (*CgroupV2Driver, error) {
	if pid <= 0 {
		return nil, errors.New("pid must be positive")
	}
	driver, err := NewCgroupV2Driver(root, namespace)
	if err != nil {
		return nil, err
	}
	resolvedRoot, err := resolveExistingDirectory(driver.root)
	if err != nil {
		return nil, fmt.Errorf("resolve delegated cgroup root: %w", err)
	}
	driver, err = NewCgroupV2Driver(resolvedRoot, namespace)
	if err != nil {
		return nil, err
	}

	managerPath, err := createCgroupDirectory(resolvedRoot, "manager")
	if err != nil {
		return nil, fmt.Errorf("create manager cgroup: %w", err)
	}
	if err := os.WriteFile(filepath.Join(managerPath, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return nil, fmt.Errorf("attach manager pid: %w", err)
	}
	rootProcesses, err := os.ReadFile(filepath.Join(resolvedRoot, "cgroup.procs"))
	if err != nil {
		return nil, fmt.Errorf("confirm delegated root is empty: %w", err)
	}
	if strings.TrimSpace(string(rootProcesses)) != "" {
		return nil, ErrCgroupV2RootBusy
	}

	if err := enableDelegatedControllers(resolvedRoot); err != nil {
		return nil, err
	}
	runtimePath, err := createCgroupDirectory(resolvedRoot, "aegisrt")
	if err != nil {
		return nil, fmt.Errorf("create aegisrt cgroup: %w", err)
	}
	if err := enableDelegatedControllers(runtimePath); err != nil {
		return nil, err
	}
	namespacePath, err := createCgroupDirectory(runtimePath, namespace)
	if err != nil {
		return nil, fmt.Errorf("create namespace cgroup: %w", err)
	}
	if err := enableDelegatedControllers(namespacePath); err != nil {
		return nil, err
	}
	return driver, nil
}

func enableDelegatedControllers(root string) error {
	content, err := os.ReadFile(filepath.Join(root, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("%w at %s: %v", ErrCgroupV2Controllers, root, err)
	}
	available := make(map[string]bool)
	for _, name := range strings.Fields(string(content)) {
		available[strings.TrimPrefix(name, "+")] = true
	}
	for _, name := range delegatedControllers {
		if !available[name] {
			return fmt.Errorf("%w at %s: missing %s", ErrCgroupV2Controllers, root, name)
		}
	}
	requested := make([]string, 0, len(delegatedControllers))
	for _, name := range delegatedControllers {
		requested = append(requested, "+"+name)
	}
	controlPath := filepath.Join(root, "cgroup.subtree_control")
	if err := os.WriteFile(controlPath, []byte(strings.Join(requested, " ")), 0o644); err != nil {
		return fmt.Errorf("enable controllers at %s: %w", root, err)
	}
	enabledContent, err := os.ReadFile(controlPath)
	if err != nil {
		return fmt.Errorf("confirm controllers at %s: %w", root, err)
	}
	enabled := make(map[string]bool)
	for _, name := range strings.Fields(string(enabledContent)) {
		enabled[strings.TrimPrefix(name, "+")] = true
	}
	for _, name := range delegatedControllers {
		if !enabled[name] {
			return fmt.Errorf("%w at %s: failed to enable %s", ErrCgroupV2Controllers, root, name)
		}
	}
	return nil
}

func createCgroupDirectory(parent, name string) (string, error) {
	target := filepath.Join(parent, name)
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(target, 0o755); err != nil {
			return "", err
		}
		return target, nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("cgroup path is not a directory")
	}
	return target, nil
}

func resolveExistingDirectory(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func isWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
