package contextvm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
)

// Manifest is an immutable view of the shared and private pages visible to one
// agent. Revision and ParentID make copy-on-write lineage explicit.
type Manifest struct {
	ID           string       `json:"id"`
	ParentID     string       `json:"parent_id,omitempty"`
	WorkflowID   string       `json:"workflow_id"`
	AgentID      string       `json:"agent_id"`
	Revision     uint64       `json:"revision"`
	SharedPages  []ContextRef `json:"shared_pages,omitempty"`
	PrivatePages []ContextRef `json:"private_pages,omitempty"`
}

func NewManifest(workflowID, agentID string, shared, private []ContextRef) (Manifest, error) {
	manifest := Manifest{
		WorkflowID:   workflowID,
		AgentID:      agentID,
		Revision:     1,
		SharedPages:  cloneRefs(shared),
		PrivatePages: cloneRefs(private),
	}
	if err := manifest.validateStructure(false); err != nil {
		return Manifest{}, err
	}
	manifest.ID = calculateManifestID(manifest)
	return manifest, nil
}

// Validate verifies both structural integrity and that every page has the
// expected scope and ACL ownership for its manifest section.
func (m Manifest) Validate(store *Store) error {
	if err := m.validateStructure(true); err != nil {
		return err
	}
	request := AccessRequest{WorkflowID: m.WorkflowID, AgentID: m.AgentID}
	for _, ref := range m.SharedPages {
		page, err := store.lookup(ref.PageID)
		if err != nil {
			return fmt.Errorf("%w: shared page %s: %w", ErrInvalidManifest, ref.PageID, err)
		}
		if page.Scope != ScopeSystem && page.Scope != ScopeWorkflow {
			return fmt.Errorf("%w: shared page %s has scope %s: %w",
				ErrInvalidManifest, ref.PageID, page.Scope, ErrAccessDenied)
		}
		if err := Authorize(page, request); err != nil {
			return fmt.Errorf("%w: shared page %s: %w", ErrInvalidManifest, ref.PageID, err)
		}
	}
	for _, ref := range m.PrivatePages {
		page, err := store.lookup(ref.PageID)
		if err != nil {
			return fmt.Errorf("%w: private page %s: %w", ErrInvalidManifest, ref.PageID, err)
		}
		if page.Scope != ScopeAgent && page.Scope != ScopeEphemeral {
			return fmt.Errorf("%w: private page %s has scope %s: %w",
				ErrInvalidManifest, ref.PageID, page.Scope, ErrAccessDenied)
		}
		if err := Authorize(page, request); err != nil {
			return fmt.Errorf("%w: private page %s: %w", ErrInvalidManifest, ref.PageID, err)
		}
	}
	return nil
}

// CopyOnWrite replaces all references to one shared page with a new agent
// private page. The receiver and its backing slices remain untouched.
func (m Manifest) CopyOnWrite(sourceID PageID, replacement Page) (Manifest, error) {
	if err := m.validateStructure(true); err != nil {
		return Manifest{}, err
	}
	shared := make([]ContextRef, 0, len(m.SharedPages))
	found := false
	pinned := false
	for _, ref := range m.SharedPages {
		if ref.PageID == sourceID {
			found = true
			pinned = pinned || ref.Pin
			continue
		}
		shared = append(shared, ref)
	}
	if !found {
		return Manifest{}, fmt.Errorf("%w: %s", ErrCOWSourceNotFound, sourceID)
	}
	if err := replacement.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalidCOWPage, err)
	}
	if replacement.Scope != ScopeAgent || replacement.WorkflowID != m.WorkflowID || replacement.AgentID != m.AgentID {
		return Manifest{}, fmt.Errorf("%w: replacement must be an agent page owned by manifest principal", ErrInvalidCOWPage)
	}
	if !hasSourceRef(replacement.SourceRefs, sourceID) {
		return Manifest{}, fmt.Errorf("%w: replacement must reference source %s", ErrInvalidCOWPage, sourceID)
	}

	for _, ref := range m.PrivatePages {
		if ref.PageID == replacement.ID {
			return Manifest{}, fmt.Errorf("%w: replacement page already exists", ErrInvalidCOWPage)
		}
	}

	next := Manifest{
		ParentID:     m.ID,
		WorkflowID:   m.WorkflowID,
		AgentID:      m.AgentID,
		Revision:     m.Revision + 1,
		SharedPages:  cloneRefs(shared),
		PrivatePages: cloneRefs(m.PrivatePages),
	}
	next.PrivatePages = append(next.PrivatePages, ContextRef{PageID: replacement.ID, Pin: pinned})
	next.ID = calculateManifestID(next)
	return next, nil
}

func (m Manifest) Refs() []ContextRef {
	result := make([]ContextRef, 0, len(m.SharedPages)+len(m.PrivatePages))
	result = append(result, m.SharedPages...)
	result = append(result, m.PrivatePages...)
	return result
}

func (m Manifest) validateStructure(checkID bool) error {
	if err := validateOwnerID("workflow_id", m.WorkflowID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := validateOwnerID("agent_id", m.AgentID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if m.Revision == 0 {
		return fmt.Errorf("%w: revision must be positive", ErrInvalidManifest)
	}
	if m.ParentID != "" && !validDigest(m.ParentID) {
		return fmt.Errorf("%w: parent_id is not a SHA-256 digest", ErrInvalidManifest)
	}
	for _, ref := range m.Refs() {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
		}
	}
	if checkID && (!validDigest(m.ID) || calculateManifestID(m) != m.ID) {
		return fmt.Errorf("%w: id does not match manifest fields", ErrInvalidManifest)
	}
	return nil
}

func calculateManifestID(manifest Manifest) string {
	hasher := sha256.New()
	writeHashField(hasher, manifest.ParentID)
	writeHashField(hasher, manifest.WorkflowID)
	writeHashField(hasher, manifest.AgentID)
	writeHashField(hasher, fmt.Sprintf("%d", manifest.Revision))
	writeHashField(hasher, "shared")
	writeManifestRefs(hasher, manifest.SharedPages)
	writeHashField(hasher, "private")
	writeManifestRefs(hasher, manifest.PrivatePages)
	return hex.EncodeToString(hasher.Sum(nil))
}

func writeManifestRefs(hasher hash.Hash, refs []ContextRef) {
	for _, ref := range refs {
		writeHashField(hasher, string(ref.PageID))
		writeHashField(hasher, fmt.Sprintf("%t", ref.Pin))
		writeHashField(hasher, fmt.Sprintf("%t", ref.Cold))
	}
}

func hasSourceRef(refs []ContextRef, pageID PageID) bool {
	for _, ref := range refs {
		if ref.PageID == pageID {
			return true
		}
	}
	return false
}
