package benchmark

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFaultKernelComponentHashesOpenedFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "component")
	payload := []byte("stable-component-bytes")
	if err := os.WriteFile(path, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	size, digest, err := readFaultKernelComponent(path, faultKernelTestComponentOwner())
	if err != nil || size != int64(len(payload)) || digest != hashBytes(payload) {
		t.Fatalf("size=%d digest=%q err=%v", size, digest, err)
	}
}

func TestReadFaultKernelComponentRejectsInvalidPortableObjects(t *testing.T) {
	directory := t.TempDir()
	empty := filepath.Join(directory, "empty")
	if err := os.WriteFile(empty, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"empty": empty, "directory": directory, "missing": filepath.Join(directory, "missing"),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := readFaultKernelComponent(path, faultKernelTestComponentOwner())
			if !errors.Is(err, ErrFaultKernelUnavailable) {
				t.Fatalf("error=%v, want unavailable", err)
			}
			if strings.Contains(err.Error(), directory) {
				t.Fatalf("error leaked component path: %v", err)
			}
		})
	}
}
