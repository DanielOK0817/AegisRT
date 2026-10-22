package statefile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const testMaxPayloadSize = 16 << 20

type testEnvelope struct {
	Format   string `json:"format"`
	Version  int    `json:"version"`
	Payload  []byte `json:"payload"`
	Checksum string `json:"checksum"`
}

func TestStoreRoundTripAndEnvelope(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime.state")
	store, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	payload := []byte(`{"generation":7,"tasks":["alpha"]}`)
	if err := store.Save(payload); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !bytes.Equal(loaded, payload) {
		t.Fatalf("Load() = %q, want %q", loaded, payload)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var envelope testEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("saved envelope is not JSON: %v", err)
	}
	if envelope.Format != fileFormat || envelope.Version != fileVersion {
		t.Fatalf("envelope identity = %q/%d", envelope.Format, envelope.Version)
	}
	wantChecksum := sha256.Sum256(payload)
	if envelope.Checksum != hex.EncodeToString(wantChecksum[:]) {
		t.Fatalf("checksum = %q, want %q", envelope.Checksum, hex.EncodeToString(wantChecksum[:]))
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("Stat() error = %v", statErr)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("permissions = %o, want 600", got)
		}
	}
}

func TestLoadMissingPreservesNotExist(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "missing.state"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = store.Load()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load() error = %v, want fs.ErrNotExist", err)
	}
}

func TestSaveRejectsOversizedPayloadWithoutCreatingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime.state")
	store, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := store.Save(make([]byte, testMaxPayloadSize+1)); err == nil {
		t.Fatal("Save() error = nil, want payload size error")
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat() error = %v, want fs.ErrNotExist", err)
	}
}

func TestLoadRejectsInvalidEnvelope(t *testing.T) {
	t.Parallel()

	payload := []byte("valid payload")
	valid := makeTestEnvelope(t, payload)
	oversizedFile := bytes.Repeat([]byte("x"), maxEnvelopeSize+1)

	tests := map[string][]byte{
		"unknown field":  addJSONField(t, valid, "unexpected", true),
		"trailing JSON":  append(append([]byte{}, valid...), []byte("\n{}")...),
		"bad checksum":   mutateJSONField(t, valid, "checksum", strings.Repeat("0", 64)),
		"bad format":     mutateJSONField(t, valid, "format", "other-state"),
		"bad version":    mutateJSONField(t, valid, "version", float64(fileVersion+1)),
		"null payload":   mutateJSONField(t, valid, "payload", nil),
		"large envelope": oversizedFile,
	}

	for name, content := range tests {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "runtime.state")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			store, err := New(path)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if _, err := store.Load(); err == nil {
				t.Fatal("Load() error = nil, want invalid envelope error")
			}
		})
	}
}

func TestStoreRejectsNonRegularFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime.state")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if _, err := New(path); err == nil {
		t.Fatal("New() error = nil, want non-regular file error")
	}
}

func TestStoreRejectsBroadUnixPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permissions are enforced through ACLs outside fs.FileMode")
	}
	path := filepath.Join(t.TempDir(), "runtime.state")
	if err := os.WriteFile(path, makeTestEnvelope(t, []byte("state")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path); err == nil {
		t.Fatal("New accepted a state file readable by group")
	}
}

func TestStoreRejectsSymbolicLink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "target.state")
	link := filepath.Join(dir, "runtime.state")
	if err := os.WriteFile(target, makeTestEnvelope(t, []byte("original")), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := New(link); err == nil {
		t.Fatal("New() error = nil, want symbolic link error")
	}

	lateLink := filepath.Join(dir, "late.state")
	store, err := New(lateLink)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := os.Symlink(target, lateLink); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load() error = nil, want symbolic link error")
	}
	if err := store.Save([]byte("replacement")); err == nil {
		t.Fatal("Save() error = nil, want symbolic link error")
	}
	targetRaw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(targetRaw, makeTestEnvelope(t, []byte("original"))) {
		t.Fatal("symbolic link target was modified")
	}
}

func TestStoreConcurrentAccessAlwaysReturnsWholePayload(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime.state")
	store, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	payloads := [][]byte{
		bytes.Repeat([]byte("a"), 1024),
		bytes.Repeat([]byte("b"), 2048),
		bytes.Repeat([]byte("c"), 4096),
		bytes.Repeat([]byte("d"), 8192),
	}
	if err := store.Save(payloads[0]); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 20; iteration++ {
				if (worker+iteration)%2 == 0 {
					if saveErr := store.Save(payloads[(worker+iteration)%len(payloads)]); saveErr != nil {
						errCh <- saveErr
						return
					}
					continue
				}
				loaded, loadErr := store.Load()
				if loadErr != nil {
					errCh <- loadErr
					return
				}
				if !matchesAny(loaded, payloads) {
					errCh <- errors.New("Load returned a partial or unknown payload")
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent operation error = %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".runtime.state.tmp-*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestOpenExclusiveRejectsSecondWriterUntilClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.state")
	first, err := OpenExclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenExclusive(path)
	if !errors.Is(err, ErrLocked) || second != nil {
		t.Fatalf("second OpenExclusive = (%v, %v), want ErrLocked", second, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Load(); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("Load after Close error = %v, want fs.ErrClosed", err)
	}
	third, err := OpenExclusive(path)
	if err != nil {
		t.Fatalf("OpenExclusive after Close: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCommitUncertainErrorPreservesCauseAndMarker(t *testing.T) {
	cause := errors.New("directory sync failed")
	err := &commitUncertainError{cause: cause}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(%v, cause) = false", err)
	}
	var marker interface{ CommitUncertain() bool }
	if !errors.As(err, &marker) || !marker.CommitUncertain() {
		t.Fatal("commit uncertainty marker was not discoverable")
	}
}

func makeTestEnvelope(t *testing.T, payload []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(payload)
	raw, err := json.Marshal(testEnvelope{
		Format:   fileFormat,
		Version:  fileVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return append(raw, '\n')
}

func addJSONField(t *testing.T, raw []byte, key string, value any) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	document[key] = value
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return encoded
}

func mutateJSONField(t *testing.T, raw []byte, key string, value any) []byte {
	t.Helper()
	return addJSONField(t, raw, key, value)
}

func matchesAny(got []byte, candidates [][]byte) bool {
	for _, candidate := range candidates {
		if bytes.Equal(got, candidate) {
			return true
		}
	}
	return false
}
