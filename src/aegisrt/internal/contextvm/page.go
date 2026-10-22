package contextvm

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

type PageID string

type Scope string

const (
	ScopeSystem    Scope = "system"
	ScopeWorkflow  Scope = "workflow"
	ScopeAgent     Scope = "agent"
	ScopeEphemeral Scope = "ephemeral"
)

func (s Scope) Validate() error {
	switch s {
	case ScopeSystem, ScopeWorkflow, ScopeAgent, ScopeEphemeral:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidScope, s)
	}
}

type ContextRef struct {
	PageID PageID `json:"page_id"`
	Pin    bool   `json:"pin,omitempty"`
	Cold   bool   `json:"cold,omitempty"`
}

func (r ContextRef) Validate() error {
	if !validDigest(string(r.PageID)) {
		return fmt.Errorf("%w: page_id %q is not a SHA-256 digest", ErrInvalidReference, r.PageID)
	}
	return nil
}

type PageSpec struct {
	Content    string       `json:"content"`
	Scope      Scope        `json:"scope"`
	WorkflowID string       `json:"workflow_id,omitempty"`
	AgentID    string       `json:"agent_id,omitempty"`
	SourceRefs []ContextRef `json:"source_refs,omitempty"`
}

// Page is an immutable CAS object. ContentHash addresses the exact body while
// ID also binds security ownership, token accounting, and summary lineage.
type Page struct {
	ID          PageID       `json:"id"`
	ContentHash string       `json:"content_hash"`
	Content     string       `json:"content"`
	Scope       Scope        `json:"scope"`
	WorkflowID  string       `json:"workflow_id,omitempty"`
	AgentID     string       `json:"agent_id,omitempty"`
	TokenCount  int          `json:"token_count"`
	SourceRefs  []ContextRef `json:"source_refs,omitempty"`
}

func NewPage(spec PageSpec, counter Counter) (Page, error) {
	if counter == nil {
		return Page{}, fmt.Errorf("%w: token counter is required", ErrInvalidPage)
	}
	if err := validatePageSpec(spec); err != nil {
		return Page{}, err
	}
	tokens, err := counter.Count(spec.Content)
	if err != nil {
		return Page{}, fmt.Errorf("%w: count tokens: %v", ErrInvalidPage, err)
	}
	if tokens < 0 {
		return Page{}, fmt.Errorf("%w: token counter returned %d", ErrInvalidPage, tokens)
	}
	contentSum := sha256.Sum256([]byte(spec.Content))
	page := Page{
		ContentHash: hex.EncodeToString(contentSum[:]),
		Content:     spec.Content,
		Scope:       spec.Scope,
		WorkflowID:  spec.WorkflowID,
		AgentID:     spec.AgentID,
		TokenCount:  tokens,
		SourceRefs:  cloneRefs(spec.SourceRefs),
	}
	page.ID = calculatePageID(page)
	return page, nil
}

func (p Page) Validate() error {
	spec := PageSpec{Content: p.Content, Scope: p.Scope, WorkflowID: p.WorkflowID, AgentID: p.AgentID, SourceRefs: p.SourceRefs}
	if err := validatePageSpec(spec); err != nil {
		return err
	}
	if p.TokenCount < 0 {
		return fmt.Errorf("%w: token_count must be non-negative", ErrInvalidPage)
	}
	contentSum := sha256.Sum256([]byte(p.Content))
	if p.ContentHash != hex.EncodeToString(contentSum[:]) {
		return fmt.Errorf("%w: content_hash does not match content", ErrInvalidPage)
	}
	if !validDigest(string(p.ID)) || calculatePageID(p) != p.ID {
		return fmt.Errorf("%w: id does not match immutable page fields", ErrInvalidPage)
	}
	return nil
}

func validatePageSpec(spec PageSpec) error {
	if strings.TrimSpace(spec.Content) == "" {
		return fmt.Errorf("%w: content is required", ErrInvalidPage)
	}
	if err := spec.Scope.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPage, err)
	}
	switch spec.Scope {
	case ScopeSystem:
		if spec.WorkflowID != "" || spec.AgentID != "" {
			return fmt.Errorf("%w: system page must not have an owner", ErrInvalidPage)
		}
	case ScopeWorkflow:
		if err := validateOwnerID("workflow_id", spec.WorkflowID); err != nil || spec.AgentID != "" {
			return fmt.Errorf("%w: workflow page requires only workflow_id", ErrInvalidPage)
		}
	case ScopeAgent, ScopeEphemeral:
		if err := validateOwnerID("workflow_id", spec.WorkflowID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPage, err)
		}
		if err := validateOwnerID("agent_id", spec.AgentID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPage, err)
		}
	}
	seen := make(map[PageID]struct{}, len(spec.SourceRefs))
	for _, ref := range spec.SourceRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: source_refs: %v", ErrInvalidPage, err)
		}
		if _, exists := seen[ref.PageID]; exists {
			return fmt.Errorf("%w: duplicate source_ref %q", ErrInvalidPage, ref.PageID)
		}
		seen[ref.PageID] = struct{}{}
	}
	return nil
}

func calculatePageID(page Page) PageID {
	hasher := sha256.New()
	writeHashField(hasher, page.ContentHash)
	writeHashField(hasher, string(page.Scope))
	writeHashField(hasher, page.WorkflowID)
	writeHashField(hasher, page.AgentID)
	writeHashField(hasher, fmt.Sprintf("%d", page.TokenCount))
	for _, ref := range page.SourceRefs {
		writeHashField(hasher, string(ref.PageID))
		writeHashField(hasher, fmt.Sprintf("%t", ref.Pin))
		writeHashField(hasher, fmt.Sprintf("%t", ref.Cold))
	}
	return PageID(hex.EncodeToString(hasher.Sum(nil)))
}

func writeHashField(hasher hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write([]byte(value))
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateOwnerID(field, value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 128 {
		return fmt.Errorf("%s is invalid", field)
	}
	for index, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || index > 0 && (char == '-' || char == '_' || char == '.' || char == ':')
		if !valid {
			return fmt.Errorf("%s contains invalid character %q", field, char)
		}
	}
	return nil
}

func cloneRefs(source []ContextRef) []ContextRef {
	return append([]ContextRef(nil), source...)
}

func clonePage(page Page) Page {
	page.SourceRefs = cloneRefs(page.SourceRefs)
	return page
}
