package resourcecontrol

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"aegisrt/internal/domain"
)

func TestCgroupV2CreateAndAttach(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	path, err := driver.Create("attempt-01", Limits{
		Resources:   domain.ResourceVector{CPUMilli: 1500, MemoryMiB: 256},
		PIDsMax:     32,
		DisableSwap: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(path, "cpu.max"), "150000 100000")
	assertFileContent(t, filepath.Join(path, "memory.max"), "268435456")
	assertFileContent(t, filepath.Join(path, "pids.max"), "32")
	assertFileContent(t, filepath.Join(path, "memory.swap.max"), "0")

	if err := driver.Attach("attempt-01", 1234); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(path, "cgroup.procs"), "1234")
}

func TestCgroupV2UnlimitedControls(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	path, err := driver.Create("idle", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(path, "cpu.max"), "max 100000")
	assertFileContent(t, filepath.Join(path, "memory.max"), "max")
	assertFileContent(t, filepath.Join(path, "pids.max"), "max")
	assertFileContent(t, filepath.Join(path, "memory.swap.max"), "max")
}

func TestCgroupV2RejectsInvalidInputs(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", "a/b", "space id"} {
		if _, err := driver.Create(id, Limits{}); !errors.Is(err, ErrInvalidAttemptID) {
			t.Fatalf("Create(%q) error = %v, want ErrInvalidAttemptID", id, err)
		}
	}
	_, err = driver.Create("overflow", Limits{
		Resources: domain.ResourceVector{MemoryMiB: math.MaxInt64},
	})
	if !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("overflow error = %v, want ErrInvalidLimits", err)
	}
	if err := driver.Attach("valid", 0); err == nil {
		t.Fatal("Attach accepted a non-positive pid")
	}
}

func TestCgroupV2DestroysOnlyValidatedEmptyGroup(t *testing.T) {
	root := t.TempDir()
	driver, err := NewCgroupV2Driver(root, "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "aegisrt", "runtime-a", "finished-attempt")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := driver.Destroy("finished-attempt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempt cgroup still exists: %v", err)
	}
	if err := driver.Destroy("../escape"); !errors.Is(err, ErrInvalidAttemptID) {
		t.Fatalf("invalid destroy error = %v", err)
	}
}

func TestCgroupV2Stats(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	path, err := driver.Create("attempt-stats", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(path, "cpu.stat"), "usage_usec 123\nuser_usec 80\nsystem_usec 43\n")
	writeFixture(t, filepath.Join(path, "memory.current"), "4096\n")
	writeFixture(t, filepath.Join(path, "memory.events"), "low 1\nhigh 2\nmax 3\noom 4\noom_kill 5\n")

	stats, err := driver.Stats("attempt-stats")
	if err != nil {
		t.Fatal(err)
	}
	if stats.CPUUsageMicros != 123 || stats.CPUUserMicros != 80 || stats.CPUSystemMicros != 43 {
		t.Fatalf("unexpected cpu stats: %+v", stats)
	}
	if stats.MemoryCurrentBytes != 4096 || stats.MemoryEvents["oom_kill"] != 5 {
		t.Fatalf("unexpected memory stats: %+v", stats)
	}
}

func TestCgroupV2StatsRejectsMalformedData(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	path, err := driver.Create("bad-stats", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(path, "cpu.stat"), "invalid\n")
	writeFixture(t, filepath.Join(path, "memory.current"), "1")
	writeFixture(t, filepath.Join(path, "memory.events"), "oom 0\n")
	if _, err := driver.Stats("bad-stats"); err == nil {
		t.Fatal("Stats accepted malformed cpu.stat")
	}
}

func TestCgroupV2StatsRequiresCPUUsage(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	path, err := driver.Create("missing-usage", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(path, "cpu.stat"), "user_usec 1\n")
	writeFixture(t, filepath.Join(path, "memory.current"), "1")
	writeFixture(t, filepath.Join(path, "memory.events"), "oom 0\n")
	if _, err := driver.Stats("missing-usage"); err == nil {
		t.Fatal("Stats accepted cpu.stat without usage_usec")
	}
}

func TestCgroupV2NamespacesSeparateIdenticalAttemptIDs(t *testing.T) {
	root := t.TempDir()
	first, err := NewCgroupV2Driver(root, "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCgroupV2Driver(root, "runtime-b")
	if err != nil {
		t.Fatal(err)
	}
	firstPath, err := first.Create("same-attempt", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := second.Create("same-attempt", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if firstPath == secondPath {
		t.Fatalf("namespaced paths collided: %s", firstPath)
	}
	for _, path := range []string{firstPath, secondPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("namespaced cgroup %s: %v", path, err)
		}
	}
}

func TestCgroupV2RejectsInvalidNamespace(t *testing.T) {
	for _, namespace := range []string{"", "../escape", "a/b", "space id"} {
		if _, err := NewCgroupV2Driver(t.TempDir(), namespace); !errors.Is(err, ErrInvalidNamespace) {
			t.Fatalf("NewCgroupV2Driver namespace %q error = %v, want ErrInvalidNamespace", namespace, err)
		}
	}
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != expected {
		t.Fatalf("%s = %q, want %q", path, content, expected)
	}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
