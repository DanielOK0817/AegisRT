package contextvm

import (
	"fmt"
	"math"
)

// Append creates the next immutable manifest revision and routes one owned
// workflow or agent page to the correct section. System and ephemeral pages
// require separate lifecycle policies and cannot be appended through this API.
func (m Manifest) Append(page Page, ref ContextRef) (Manifest, error) {
	if err := m.validateStructure(true); err != nil {
		return Manifest{}, err
	}
	if err := page.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("%w: append page: %w", ErrInvalidManifest, err)
	}
	if err := ref.Validate(); err != nil {
		return Manifest{}, err
	}
	if ref.PageID != page.ID {
		return Manifest{}, fmt.Errorf("%w: page_id does not match appended page", ErrInvalidReference)
	}
	if ref.Pin && ref.Cold {
		return Manifest{}, fmt.Errorf("%w: %w: page %s cannot be both pinned and cold",
			ErrInvalidManifest, ErrReferenceFlagsConflict, ref.PageID)
	}
	if err := rejectExistingReference(m.Refs(), ref); err != nil {
		return Manifest{}, err
	}
	if m.Revision == math.MaxUint64 {
		return Manifest{}, fmt.Errorf("%w: manifest %s", ErrManifestRevisionOverflow, m.ID)
	}

	next := Manifest{
		ParentID:     m.ID,
		WorkflowID:   m.WorkflowID,
		AgentID:      m.AgentID,
		Revision:     m.Revision + 1,
		SharedPages:  cloneRefs(m.SharedPages),
		PrivatePages: cloneRefs(m.PrivatePages),
	}
	switch page.Scope {
	case ScopeWorkflow:
		if page.WorkflowID != m.WorkflowID || page.AgentID != "" {
			return Manifest{}, fmt.Errorf("%w: workflow page is not owned by manifest workflow", ErrInvalidManifest)
		}
		next.SharedPages = append(next.SharedPages, ref)
	case ScopeAgent:
		if page.WorkflowID != m.WorkflowID || page.AgentID != m.AgentID {
			return Manifest{}, fmt.Errorf("%w: agent page is not owned by manifest principal", ErrInvalidManifest)
		}
		next.PrivatePages = append(next.PrivatePages, ref)
	case ScopeSystem, ScopeEphemeral:
		return Manifest{}, fmt.Errorf("%w: scope %s cannot be appended", ErrInvalidManifest, page.Scope)
	default:
		return Manifest{}, fmt.Errorf("%w: unsupported page scope %s", ErrInvalidManifest, page.Scope)
	}
	next.ID = calculateManifestID(next)
	return next, nil
}

func rejectExistingReference(refs []ContextRef, candidate ContextRef) error {
	for _, existing := range refs {
		if existing.PageID != candidate.PageID {
			continue
		}
		if existing.Pin != candidate.Pin || existing.Cold != candidate.Cold {
			return fmt.Errorf("%w: %w: page %s", ErrInvalidManifest, ErrReferenceFlagsConflict, candidate.PageID)
		}
		return fmt.Errorf("%w: %w: page %s", ErrInvalidManifest, ErrDuplicateReference, candidate.PageID)
	}
	return nil
}
