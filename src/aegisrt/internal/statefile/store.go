// Package statefile provides a small, crash-resistant file store for runtime
// snapshots. It treats stored bytes as opaque and protects them with a
// versioned JSON envelope and a SHA-256 checksum.
package statefile

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	fileFormat       = "aegisrt-state"
	fileVersion      = 1
	maxPayloadSize   = 16 << 20
	maxEnvelopeSize  = ((maxPayloadSize + 2) / 3 * 4) + 1024
	stateFileMode    = 0o600
	temporaryPattern = ".%s.tmp-*"
)

var ErrLocked = errors.New("state file is locked by another process")

type commitUncertainError struct {
	cause error
}

func (e *commitUncertainError) Error() string       { return e.cause.Error() }
func (e *commitUncertainError) Unwrap() error       { return e.cause }
func (*commitUncertainError) CommitUncertain() bool { return true }

type envelope struct {
	Format   string `json:"format"`
	Version  int    `json:"version"`
	Payload  []byte `json:"payload"`
	Checksum string `json:"checksum"`
}

// Store serializes operations made through the same instance. Save replaces
// the destination atomically, so readers never observe a partially written
// envelope.
type Store struct {
	path     string
	mu       sync.Mutex
	lockFile *os.File
	closed   bool
}

// New validates path without creating the state file. A missing destination is
// valid; Load preserves fs.ErrNotExist until the first successful Save.
func New(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("state file path is empty")
	}

	cleanPath := filepath.Clean(path)
	if err := validateDestination(cleanPath); err != nil {
		return nil, err
	}
	return &Store{path: cleanPath}, nil
}

// OpenExclusive acquires a process-level lease held until Close. Daemons use
// this constructor so two writers cannot fork one checkpoint history.
func OpenExclusive(path string) (*Store, error) {
	store, err := New(path)
	if err != nil {
		return nil, err
	}
	lockFile, err := openLockFile(store.path + ".lock")
	if err != nil {
		return nil, err
	}
	if err = lockFileExclusive(lockFile); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("%w: %v", ErrLocked, err)
	}
	store.lockFile = lockFile
	return store, nil
}

// Load verifies the envelope and returns a copy of its opaque payload.
func (s *Store) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fs.ErrClosed
	}

	info, err := os.Lstat(s.path)
	if err != nil {
		return nil, fmt.Errorf("inspect state file: %w", err)
	}
	if err := validateRegular(info); err != nil {
		return nil, err
	}

	file, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open state file: %w", err)
	}
	defer file.Close()

	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened state file: %w", err)
	}
	if err := validateRegular(openedInfo); err != nil {
		return nil, err
	}
	if !os.SameFile(info, openedInfo) {
		return nil, errors.New("state file changed while opening")
	}
	if openedInfo.Size() > maxEnvelopeSize {
		return nil, fmt.Errorf("state envelope exceeds %d bytes", maxEnvelopeSize)
	}

	raw, err := io.ReadAll(io.LimitReader(file, maxEnvelopeSize+1))
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}
	if len(raw) > maxEnvelopeSize {
		return nil, fmt.Errorf("state envelope exceeds %d bytes", maxEnvelopeSize)
	}
	return decode(raw)
}

// Save wraps payload in an integrity-protected envelope and atomically replaces
// the destination. The parent directory must already exist.
func (s *Store) Save(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fs.ErrClosed
	}

	if len(payload) > maxPayloadSize {
		return fmt.Errorf("state payload exceeds %d bytes", maxPayloadSize)
	}
	if err := validateDestination(s.path); err != nil {
		return err
	}

	raw, err := encode(payload)
	if err != nil {
		return err
	}
	directory := filepath.Dir(s.path)
	file, err := os.CreateTemp(directory, fmt.Sprintf(temporaryPattern, filepath.Base(s.path)))
	if err != nil {
		return fmt.Errorf("create state temporary file: %w", err)
	}
	temporaryPath := file.Name()
	renamed := false
	defer func() {
		_ = file.Close()
		if !renamed {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := file.Chmod(stateFileMode); err != nil {
		return fmt.Errorf("restrict state temporary file: %w", err)
	}
	if err := writeAll(file, raw); err != nil {
		return fmt.Errorf("write state temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync state temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close state temporary file: %w", err)
	}

	// Re-check immediately before replacement so a link introduced after the
	// first validation is rejected rather than silently accepted.
	if err := validateDestination(s.path); err != nil {
		return err
	}
	if err := replaceStateFile(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	renamed = true
	if err := syncDirectory(directory); err != nil {
		return &commitUncertainError{cause: fmt.Errorf("sync state directory: %w", err)}
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.lockFile == nil {
		return nil
	}
	unlockErr := unlockFile(s.lockFile)
	closeErr := s.lockFile.Close()
	s.lockFile = nil
	return errors.Join(unlockErr, closeErr)
}

func encode(payload []byte) ([]byte, error) {
	payloadCopy := append([]byte{}, payload...)
	sum := sha256.Sum256(payloadCopy)
	raw, err := json.Marshal(envelope{
		Format:   fileFormat,
		Version:  fileVersion,
		Payload:  payloadCopy,
		Checksum: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return nil, fmt.Errorf("encode state envelope: %w", err)
	}
	return append(raw, '\n'), nil
}

func decode(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	var stored envelope
	if err := decoder.Decode(&stored); err != nil {
		return nil, fmt.Errorf("decode state envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("state envelope contains trailing JSON")
		}
		return nil, fmt.Errorf("decode trailing state data: %w", err)
	}

	if stored.Format != fileFormat {
		return nil, fmt.Errorf("unsupported state format %q", stored.Format)
	}
	if stored.Version != fileVersion {
		return nil, fmt.Errorf("unsupported state version %d", stored.Version)
	}
	if stored.Payload == nil {
		return nil, errors.New("state payload is null")
	}
	if len(stored.Payload) > maxPayloadSize {
		return nil, fmt.Errorf("state payload exceeds %d bytes", maxPayloadSize)
	}

	wantChecksum, err := hex.DecodeString(stored.Checksum)
	if err != nil || len(wantChecksum) != sha256.Size {
		return nil, errors.New("state checksum is malformed")
	}
	gotChecksum := sha256.Sum256(stored.Payload)
	if subtle.ConstantTimeCompare(wantChecksum, gotChecksum[:]) != 1 {
		return nil, errors.New("state checksum mismatch")
	}
	return append([]byte(nil), stored.Payload...), nil
}

func validateDestination(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect state file: %w", err)
	}
	return validateRegular(info)
}

func openLockFile(path string) (*os.File, error) {
	before, statErr := os.Lstat(path)
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect state lock: %w", statErr)
	}
	if statErr == nil {
		if err := validateRegular(before); err != nil {
			return nil, fmt.Errorf("state lock: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, stateFileMode)
	if err != nil {
		return nil, fmt.Errorf("open state lock: %w", err)
	}
	if err := file.Chmod(stateFileMode); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("restrict state lock: %w", err)
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect opened state lock: %w", err)
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, current) {
		_ = file.Close()
		return nil, errors.New("state lock changed while opening")
	}
	if err := validateRegular(current); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("state lock: %w", err)
	}
	return file, nil
}

func validateRegular(info fs.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("state file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("state file must be regular, mode is %s", info.Mode())
	}
	return validateStatePermissions(info)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
