// Package apitoken loads the daemon API token from a restricted local file.
package apitoken

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

const (
	MinimumTokenBytes = 32
	MaximumTokenBytes = 256
	MaximumFileBytes  = 512
)

var (
	ErrInvalidFile       = errors.New("invalid API token file")
	ErrFileTooLarge      = errors.New("API token file exceeds size limit")
	ErrInvalidToken      = errors.New("invalid API token")
	ErrUnsafePermissions = errors.New("unsafe API token file permissions")
)

// Load returns a private copy of one bearer token. A single trailing line
// ending is accepted so files created by common secret managers remain usable.
func Load(path string, strictPermissions bool) ([]byte, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path {
		return nil, ErrInvalidFile
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open: %v", ErrInvalidFile, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: stat: %v", ErrInvalidFile, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: not a regular file", ErrInvalidFile)
	}
	if strictPermissions && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, ErrUnsafePermissions
	}
	if info.Size() > MaximumFileBytes {
		return nil, fmt.Errorf("%w: got %d bytes", ErrFileTooLarge, info.Size())
	}
	content, err := io.ReadAll(io.LimitReader(file, MaximumFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", ErrInvalidFile, err)
	}
	if len(content) > MaximumFileBytes {
		return nil, ErrFileTooLarge
	}
	token := bytes.TrimSuffix(content, []byte("\n"))
	token = bytes.TrimSuffix(token, []byte("\r"))
	if err := validateToken(token); err != nil {
		return nil, err
	}
	return append([]byte(nil), token...), nil
}

func validateToken(token []byte) error {
	if len(token) < MinimumTokenBytes || len(token) > MaximumTokenBytes {
		return fmt.Errorf("%w: length must be between %d and %d bytes",
			ErrInvalidToken, MinimumTokenBytes, MaximumTokenBytes)
	}
	for _, value := range token {
		if isTokenCharacter(value) {
			continue
		}
		return fmt.Errorf("%w: unsupported character", ErrInvalidToken)
	}
	return nil
}

func isTokenCharacter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || bytes.ContainsRune([]byte("-._~+/="), rune(value))
}
