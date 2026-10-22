package resourcecontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// RuntimeNamespaceForStateFile returns the stable cgroup namespace bound to a
// durable Runtime state identity. Supervisors use the same value before a
// daemon starts so they can reclaim a partially created hierarchy.
func RuntimeNamespaceForStateFile(statePath string) (string, error) {
	absolute, err := filepath.Abs(statePath)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(filepath.Clean(absolute)))
	return "rt-" + hex.EncodeToString(digest[:12]), nil
}
