package runtimecore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const runtimeInstanceIDBytes = 32

func newRuntimeInstanceID() (string, error) {
	value := make([]byte, runtimeInstanceIDBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate runtime instance ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func validateRuntimeInstanceID(value string) error {
	if len(value) != hex.EncodedLen(runtimeInstanceIDBytes) {
		return fmt.Errorf("must be %d lowercase hexadecimal characters", hex.EncodedLen(runtimeInstanceIDBytes))
	}
	if value != strings.ToLower(value) {
		return errors.New("must use canonical lowercase hexadecimal encoding")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != runtimeInstanceIDBytes {
		return errors.New("must encode exactly 32 bytes as hexadecimal")
	}
	return nil
}
