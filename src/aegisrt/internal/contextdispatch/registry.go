package contextdispatch

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"aegisrt/internal/contextvm"
)

var (
	ErrManifestNotFound        = errors.New("context manifest not found")
	ErrInvalidRegistryKey      = errors.New("invalid context manifest registry key")
	ErrInvalidRegistryManifest = errors.New("invalid context manifest registry entry")
	ErrRegistryUnavailable     = errors.New("context manifest registry is unavailable")
)

type manifestKey struct {
	workflowID string
	agentID    string
}

// Registry stores immutable ContextVM manifest snapshots by workflow and
// agent principal. Set and Get copy both reference slices so callers cannot
// mutate registry state after either operation returns.
type Registry struct {
	mu        sync.RWMutex
	manifests map[manifestKey]contextvm.Manifest
}

func NewRegistry() *Registry {
	return &Registry{manifests: make(map[manifestKey]contextvm.Manifest)}
}

func (r *Registry) Set(manifest contextvm.Manifest) error {
	if r == nil {
		return ErrRegistryUnavailable
	}
	if err := validateRegistryManifest(manifest); err != nil {
		return err
	}
	key := manifestKey{workflowID: manifest.WorkflowID, agentID: manifest.AgentID}
	copy := cloneManifest(manifest)
	r.mu.Lock()
	if r.manifests == nil {
		r.manifests = make(map[manifestKey]contextvm.Manifest)
	}
	r.manifests[key] = copy
	r.mu.Unlock()
	return nil
}

func (r *Registry) Get(workflowID, agentID string) (contextvm.Manifest, error) {
	if r == nil {
		return contextvm.Manifest{}, ErrRegistryUnavailable
	}
	key, err := newManifestKey(workflowID, agentID)
	if err != nil {
		return contextvm.Manifest{}, err
	}
	r.mu.RLock()
	manifest, exists := r.manifests[key]
	r.mu.RUnlock()
	if !exists {
		return contextvm.Manifest{}, fmt.Errorf("%w: workflow=%q agent=%q",
			ErrManifestNotFound, workflowID, agentID)
	}
	return cloneManifest(manifest), nil
}

// EnsureEmpty returns the existing manifest or atomically installs a
// deterministic empty manifest. Holding one write lock prevents a future
// concurrent Context writer from being overwritten by a stale Get/Set pair.
func (r *Registry) EnsureEmpty(workflowID, agentID string) (contextvm.Manifest, error) {
	manifest, _, err := r.ensureEmpty(workflowID, agentID)
	return manifest, err
}

// EnsureEmptyWithOwnership also reports whether this call installed the
// returned manifest. It supports exact rollback by higher-level transactions.
func (r *Registry) EnsureEmptyWithOwnership(workflowID, agentID string) (contextvm.Manifest, bool, error) {
	return r.ensureEmpty(workflowID, agentID)
}

// ensureEmpty reports ownership of a newly installed manifest so a caller can
// safely roll back a larger operation with DeleteIfID.
func (r *Registry) ensureEmpty(workflowID, agentID string) (contextvm.Manifest, bool, error) {
	if r == nil {
		return contextvm.Manifest{}, false, ErrRegistryUnavailable
	}
	key, err := newManifestKey(workflowID, agentID)
	if err != nil {
		return contextvm.Manifest{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if manifest, exists := r.manifests[key]; exists {
		return cloneManifest(manifest), false, nil
	}
	manifest, err := contextvm.NewManifest(workflowID, agentID, nil, nil)
	if err != nil {
		return contextvm.Manifest{}, false, fmt.Errorf("%w: create empty manifest: %v", ErrInvalidRegistryManifest, err)
	}
	if r.manifests == nil {
		r.manifests = make(map[manifestKey]contextvm.Manifest)
	}
	r.manifests[key] = cloneManifest(manifest)
	return cloneManifest(manifest), true, nil
}

// DeleteIfID removes only the exact manifest installed by the caller. A
// concurrent Context update therefore cannot be removed by stale rollback.
func (r *Registry) DeleteIfID(workflowID, agentID, manifestID string) (bool, error) {
	if r == nil {
		return false, ErrRegistryUnavailable
	}
	key, err := newManifestKey(workflowID, agentID)
	if err != nil {
		return false, err
	}
	if !validDigest(manifestID) {
		return false, fmt.Errorf("%w: manifest id is not a SHA-256 digest", ErrInvalidRegistryManifest)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	manifest, exists := r.manifests[key]
	if !exists || manifest.ID != manifestID {
		return false, nil
	}
	delete(r.manifests, key)
	return true, nil
}

func newManifestKey(workflowID, agentID string) (manifestKey, error) {
	access := contextvm.AccessRequest{WorkflowID: workflowID, AgentID: agentID}
	if err := access.Validate(); err != nil {
		return manifestKey{}, fmt.Errorf("%w: %v", ErrInvalidRegistryKey, err)
	}
	return manifestKey{workflowID: workflowID, agentID: agentID}, nil
}

func validateRegistryManifest(manifest contextvm.Manifest) error {
	if _, err := newManifestKey(manifest.WorkflowID, manifest.AgentID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRegistryManifest, err)
	}
	if manifest.Revision == 0 || !validDigest(manifest.ID) {
		return fmt.Errorf("%w: manifest id and revision are required", ErrInvalidRegistryManifest)
	}
	if manifest.ParentID != "" && !validDigest(manifest.ParentID) {
		return fmt.Errorf("%w: parent id is not a SHA-256 digest", ErrInvalidRegistryManifest)
	}
	for _, ref := range manifest.Refs() {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidRegistryManifest, err)
		}
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneManifest(manifest contextvm.Manifest) contextvm.Manifest {
	manifest.SharedPages = append([]contextvm.ContextRef(nil), manifest.SharedPages...)
	manifest.PrivatePages = append([]contextvm.ContextRef(nil), manifest.PrivatePages...)
	return manifest
}
