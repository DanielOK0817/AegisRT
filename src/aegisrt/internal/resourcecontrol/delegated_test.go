package resourcecontrol

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSelfCgroupV2Root(t *testing.T) {
	mountRoot := t.TempDir()
	want := filepath.Join(mountRoot, "user.slice", "session.scope")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	procPath := filepath.Join(t.TempDir(), "cgroup")
	writeFixture(t, procPath, "7:cpu:/legacy\n0::/user.slice/session.scope\n")

	got, err := ResolveSelfCgroupV2Root(procPath, mountRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("root = %q, want %q", got, want)
	}
}

func TestResolveSelfCgroupV2RootRejectsUnsafeHierarchy(t *testing.T) {
	mountRoot := t.TempDir()
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "missing", content: "7:cpu:/legacy\n"},
		{name: "duplicate", content: "0::/first\n0::/second\n"},
		{name: "relative", content: "0::user.slice/session.scope\n"},
		{name: "mount root", content: "0::/\n"},
		{name: "parent traversal", content: "0::/user.slice/../../outside\n"},
		{name: "malformed", content: "0::/valid:extra\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			procPath := filepath.Join(t.TempDir(), "cgroup")
			writeFixture(t, procPath, test.content)
			if _, err := ResolveSelfCgroupV2Root(procPath, mountRoot); !errors.Is(err, ErrInvalidCgroupV2Path) {
				t.Fatalf("error = %v, want ErrInvalidCgroupV2Path", err)
			}
		})
	}
}

func TestResolveSelfCgroupV2RootRejectsSymlinkEscape(t *testing.T) {
	mountRoot := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(mountRoot, "escaped")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	procPath := filepath.Join(t.TempDir(), "cgroup")
	writeFixture(t, procPath, "0::/escaped\n")

	if _, err := ResolveSelfCgroupV2Root(procPath, mountRoot); !errors.Is(err, ErrInvalidCgroupV2Path) {
		t.Fatalf("error = %v, want ErrInvalidCgroupV2Path", err)
	}
}

func TestPrepareDelegatedCgroupV2(t *testing.T) {
	root := delegatedFixture(t, "runtime-a", "cpu memory pids")
	driver, err := PrepareDelegatedCgroupV2(root, "runtime-a", 4321)
	if err != nil {
		t.Fatal(err)
	}
	if driver.root != root || driver.namespace != "runtime-a" {
		t.Fatalf("unexpected driver: %+v", driver)
	}
	assertFileContent(t, filepath.Join(root, "manager", "cgroup.procs"), "4321")
	for _, path := range []string{
		root,
		filepath.Join(root, "aegisrt"),
		filepath.Join(root, "aegisrt", "runtime-a"),
	} {
		assertEnabledControllers(t, filepath.Join(path, "cgroup.subtree_control"))
	}
}

func TestPrepareDelegatedCgroupV2MovesManagerBeforeRootCheck(t *testing.T) {
	root := delegatedFixture(t, "runtime-a", "cpu memory pids")
	writeFixture(t, filepath.Join(root, "cgroup.procs"), "9876\n")

	if _, err := PrepareDelegatedCgroupV2(root, "runtime-a", 9876); !errors.Is(err, ErrCgroupV2RootBusy) {
		t.Fatalf("error = %v, want ErrCgroupV2RootBusy", err)
	}
	assertFileContent(t, filepath.Join(root, "manager", "cgroup.procs"), "9876")
	assertFileContent(t, filepath.Join(root, "cgroup.subtree_control"), "")
}

func TestPrepareDelegatedCgroupV2FailsClosedOnMissingController(t *testing.T) {
	root := delegatedFixture(t, "runtime-a", "cpu pids")

	if _, err := PrepareDelegatedCgroupV2(root, "runtime-a", 111); !errors.Is(err, ErrCgroupV2Controllers) {
		t.Fatalf("error = %v, want ErrCgroupV2Controllers", err)
	}
	assertFileContent(t, filepath.Join(root, "manager", "cgroup.procs"), "111")
	assertFileContent(t, filepath.Join(root, "cgroup.subtree_control"), "")
}

func TestPrepareDelegatedCgroupV2StopsAtFailedLevel(t *testing.T) {
	root := delegatedFixture(t, "runtime-a", "cpu memory pids")
	writeFixture(t, filepath.Join(root, "aegisrt", "cgroup.controllers"), "cpu memory\n")

	if _, err := PrepareDelegatedCgroupV2(root, "runtime-a", 222); !errors.Is(err, ErrCgroupV2Controllers) {
		t.Fatalf("error = %v, want ErrCgroupV2Controllers", err)
	}
	assertEnabledControllers(t, filepath.Join(root, "cgroup.subtree_control"))
	assertFileContent(t, filepath.Join(root, "aegisrt", "cgroup.subtree_control"), "")
	assertFileContent(t, filepath.Join(root, "aegisrt", "runtime-a", "cgroup.subtree_control"), "")
}

func TestPrepareDelegatedCgroupV2RejectsInputsBeforeMutation(t *testing.T) {
	root := delegatedFixture(t, "runtime-a", "cpu memory pids")
	for _, test := range []struct {
		namespace string
		pid       int
	}{
		{namespace: "../escape", pid: 12},
		{namespace: "runtime-a", pid: 0},
	} {
		if _, err := PrepareDelegatedCgroupV2(root, test.namespace, test.pid); err == nil {
			t.Fatalf("PrepareDelegatedCgroupV2(%q, %d) succeeded", test.namespace, test.pid)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "manager")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid input mutated manager cgroup: %v", err)
	}
}

func delegatedFixture(t *testing.T, namespace, controllers string) string {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{
		root,
		filepath.Join(root, "aegisrt"),
		filepath.Join(root, "aegisrt", namespace),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(path, "cgroup.controllers"), controllers+"\n")
		writeFixture(t, filepath.Join(path, "cgroup.subtree_control"), "")
	}
	writeFixture(t, filepath.Join(root, "cgroup.procs"), "")
	return root
}

func assertEnabledControllers(t *testing.T, path string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(content))
	if len(fields) != 3 {
		t.Fatalf("%s = %q, want three controllers", path, content)
	}
	for index, controller := range []string{"cpu", "memory", "pids"} {
		if strings.TrimPrefix(fields[index], "+") != controller {
			t.Fatalf("%s controller %d = %q, want %q", path, index, fields[index], controller)
		}
	}
}
