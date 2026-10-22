package apitoken

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadAcceptsRestrictedTokenFile(t *testing.T) {
	want := strings.Repeat("a", 32)
	path := writeTokenFile(t, want+"\n", 0o600)
	got, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("token mismatch: got length %d", len(got))
	}
	got[0] = 'z'
	again, err := Load(path, true)
	if err != nil || string(again) != want {
		t.Fatal("Load did not return independent token bytes")
	}
}

func TestLoadRejectsInvalidTokenWithoutLeakingIt(t *testing.T) {
	secret := "short-secret-value"
	path := writeTokenFile(t, secret, 0o600)
	_, err := Load(path, false)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("error = %v, want ErrInvalidToken", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked token: %v", err)
	}

	path = writeTokenFile(t, strings.Repeat("b", 32)+"\nsecond-line", 0o600)
	if _, err := Load(path, false); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("multiline error = %v, want ErrInvalidToken", err)
	}
}

func TestLoadRejectsOversizedAndNonRegularFiles(t *testing.T) {
	path := writeTokenFile(t, strings.Repeat("a", MaximumFileBytes+1), 0o600)
	if _, err := Load(path, false); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized error = %v, want ErrFileTooLarge", err)
	}
	if _, err := Load(t.TempDir(), false); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("directory error = %v, want ErrInvalidFile", err)
	}
}

func TestLoadRejectsUnsafeUnixPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission semantics")
	}
	path := writeTokenFile(t, strings.Repeat("a", 32), 0o644)
	if _, err := Load(path, true); !errors.Is(err, ErrUnsafePermissions) {
		t.Fatalf("permission error = %v, want ErrUnsafePermissions", err)
	}
}

func TestLoadValidatesPath(t *testing.T) {
	if _, err := Load("", false); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("empty path error = %v, want ErrInvalidFile", err)
	}
}

func writeTokenFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.token")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	return path
}
