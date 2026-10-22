//go:build linux

package benchmark

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func faultKernelTestComponentOwner() uint32 {
	return uint32(os.Geteuid())
}

func TestReadFaultKernelComponentRejectsUnsafeLinuxFiles(t *testing.T) {
	directory := t.TempDir()
	owner := faultKernelTestComponentOwner()
	valid := writeFaultKernelComponentTestFile(t, directory, "valid", 0o755)
	if size, digest, err := readFaultKernelComponent(valid, owner); err != nil ||
		size != int64(len("component")) || !validFaultKernelHash(digest) {
		t.Fatalf("size=%d digest=%q err=%v", size, digest, err)
	}

	tests := map[string]struct {
		path  string
		owner uint32
	}{
		"wrong owner":      {path: valid, owner: owner + 1},
		"group writable":   {path: writeFaultKernelComponentTestFile(t, directory, "group-write", 0o775), owner: owner},
		"world writable":   {path: writeFaultKernelComponentTestFile(t, directory, "world-write", 0o757), owner: owner},
		"not executable":   {path: writeFaultKernelComponentTestFile(t, directory, "no-exec", 0o644), owner: owner},
		"empty executable": {path: writeFaultKernelComponentTestFile(t, directory, "empty", 0o755, ""), owner: owner},
		"directory":        {path: directory, owner: owner},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := readFaultKernelComponent(test.path, test.owner); !errors.Is(err, ErrFaultKernelUnavailable) {
				t.Fatalf("error=%v, want unavailable", err)
			}
		})
	}
}

func TestReadFaultKernelComponentRejectsLinuxSymlink(t *testing.T) {
	directory := t.TempDir()
	target := writeFaultKernelComponentTestFile(t, directory, "target", 0o755)
	link := filepath.Join(directory, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readFaultKernelComponent(link, faultKernelTestComponentOwner()); !errors.Is(err, ErrFaultKernelUnavailable) {
		t.Fatalf("error=%v, want unavailable", err)
	}
}

func TestBuildFaultKernelComponentManifestRequiresRootOwnerOnLinux(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test requires a non-root process")
	}
	directory := t.TempDir()
	harness := writeFaultKernelComponentTestFile(t, directory, "harness", 0o755)
	daemon := writeFaultKernelComponentTestFile(t, directory, "daemon", 0o755)
	if _, _, _, err := buildFaultKernelComponentManifest(harness, daemon, ""); !errors.Is(err, ErrFaultKernelUnavailable) {
		t.Fatalf("error=%v, want unavailable", err)
	}
}

func writeFaultKernelComponentTestFile(
	t *testing.T, directory, name string, mode os.FileMode, content ...string,
) string {
	t.Helper()
	payload := "component"
	if len(content) == 1 {
		payload = content[0]
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(payload), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
