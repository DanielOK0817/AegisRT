package resourcecontrol

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestReclaimAttemptHierarchyRemovesOnlyFixedLevels(t *testing.T) {
	driver, paths := attemptHierarchyFixture(t, "daemon", "runtime-inner")

	if err := driver.ReclaimAttemptHierarchy("daemon", "runtime-inner"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(paths.attempt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempt hierarchy remains: %v", err)
	}
}

func TestReclaimAttemptHierarchyAllowsVirtualFilesAndUsesPostorder(t *testing.T) {
	driver, paths := attemptHierarchyFixture(t, "daemon", "runtime-inner")
	for _, path := range []string{paths.attempt, paths.manager, paths.runtimeRoot, paths.runtimeNamespace} {
		writeFixture(t, filepath.Join(path, "cgroup.procs"), "")
		writeFixture(t, filepath.Join(path, "cgroup.events"), "populated 0\n")
	}
	var removed []string
	err := driver.reclaimAttemptHierarchy("daemon", "runtime-inner", func(path string) error {
		removed = append(removed, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{paths.runtimeNamespace, paths.runtimeRoot, paths.manager, paths.attempt}
	if !reflect.DeepEqual(removed, want) {
		t.Fatalf("remove order = %v, want %v", removed, want)
	}
}

func TestReclaimAttemptHierarchyFailsClosedBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, paths attemptHierarchyPaths)
		want   error
	}{
		{
			name: "unknown child directory",
			mutate: func(t *testing.T, paths attemptHierarchyPaths) {
				if err := os.Mkdir(filepath.Join(paths.runtimeNamespace, "unknown"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrUnsafeCgroupHierarchy,
		},
		{
			name: "remaining process",
			mutate: func(t *testing.T, paths attemptHierarchyPaths) {
				writeFixture(t, filepath.Join(paths.manager, "cgroup.procs"), "4321\n")
			},
			want: ErrCgroupHierarchyPopulated,
		},
		{
			name: "unexpected symlink",
			mutate: func(t *testing.T, paths attemptHierarchyPaths) {
				if err := os.Symlink(t.TempDir(), filepath.Join(paths.manager, "escaped")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			want: ErrUnsafeCgroupHierarchy,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver, paths := attemptHierarchyFixture(t, "daemon-secret", "runtime-secret")
			test.mutate(t, paths)
			var removed []string
			err := driver.reclaimAttemptHierarchy("daemon-secret", "runtime-secret", func(path string) error {
				removed = append(removed, path)
				return nil
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if len(removed) != 0 {
				t.Fatalf("removed levels before complete validation: %v", removed)
			}
			for _, path := range []string{paths.attempt, paths.manager, paths.runtimeRoot, paths.runtimeNamespace} {
				if _, statErr := os.Lstat(path); statErr != nil {
					t.Fatalf("fixed level was mutated: %v", statErr)
				}
			}
			for _, secret := range []string{"daemon-secret", "runtime-secret", driver.root} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error disclosed identifier %q: %v", secret, err)
				}
			}
		})
	}
}

func TestReclaimAttemptHierarchyValidatesIdentifiersBeforeMutation(t *testing.T) {
	driver, paths := attemptHierarchyFixture(t, "daemon", "runtime-inner")
	for _, test := range []struct {
		attemptID string
		namespace string
		want      error
	}{
		{attemptID: "../escape", namespace: "runtime-inner", want: ErrInvalidAttemptID},
		{attemptID: "daemon", namespace: "../escape", want: ErrInvalidNamespace},
	} {
		if err := driver.ReclaimAttemptHierarchy(test.attemptID, test.namespace); !errors.Is(err, test.want) {
			t.Fatalf("error = %v, want %v", err, test.want)
		}
	}
	if _, err := os.Stat(paths.attempt); err != nil {
		t.Fatalf("invalid input mutated hierarchy: %v", err)
	}
}

func TestReclaimAttemptHierarchySanitizesRemovalFailure(t *testing.T) {
	driver, _ := attemptHierarchyFixture(t, "daemon-secret", "runtime-secret")
	err := driver.reclaimAttemptHierarchy("daemon-secret", "runtime-secret", func(string) error {
		return &os.PathError{Op: "remove", Path: "private-path", Err: errors.New("failure")}
	})
	if !errors.Is(err, ErrUnsafeCgroupHierarchy) {
		t.Fatalf("error = %v, want unsafe hierarchy", err)
	}
	for _, secret := range []string{"daemon-secret", "runtime-secret", "private-path", driver.root} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error disclosed identifier %q: %v", secret, err)
		}
	}
}

func TestReclaimAttemptHierarchyRetriesTransientKernelRemoval(t *testing.T) {
	driver, paths := attemptHierarchyFixture(t, "daemon", "runtime-inner")
	calls := 0
	err := driver.reclaimAttemptHierarchy("daemon", "runtime-inner", func(path string) error {
		calls++
		if path == paths.runtimeNamespace && calls == 1 {
			return syscall.EBUSY
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Fatalf("remove calls = %d, want one retry plus four fixed levels", calls)
	}
}

type attemptHierarchyPaths struct {
	attempt          string
	manager          string
	runtimeRoot      string
	runtimeNamespace string
}

func attemptHierarchyFixture(t *testing.T, attemptID, runtimeNamespace string) (*CgroupV2Driver, attemptHierarchyPaths) {
	t.Helper()
	driver, err := NewCgroupV2Driver(t.TempDir(), "benchmark")
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := driver.groupPath(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	paths := attemptHierarchyPaths{
		attempt:          attempt,
		manager:          filepath.Join(attempt, "manager"),
		runtimeRoot:      filepath.Join(attempt, "aegisrt"),
		runtimeNamespace: filepath.Join(attempt, "aegisrt", runtimeNamespace),
	}
	for _, path := range []string{paths.manager, paths.runtimeNamespace} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return driver, paths
}
