package contextvm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// StoreState is the durable, content-deduplicated representation of a Store.
// Blobs own content and token counts; PageState values contain metadata only.
type StoreState struct {
	Blobs []BlobState `json:"blobs,omitempty"`
	Pages []PageState `json:"pages,omitempty"`
}

type BlobState struct {
	ContentHash string `json:"content_hash"`
	Content     string `json:"content"`
	TokenCount  int    `json:"token_count"`
}

type PageState struct {
	ID          PageID       `json:"id"`
	ContentHash string       `json:"content_hash"`
	Scope       Scope        `json:"scope"`
	WorkflowID  string       `json:"workflow_id,omitempty"`
	AgentID     string       `json:"agent_id,omitempty"`
	SourceRefs  []ContextRef `json:"source_refs,omitempty"`
}

// State returns an immutable, canonically ordered snapshot.
func (s *Store) State() StoreState {
	if s == nil {
		return StoreState{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := StoreState{
		Blobs: make([]BlobState, 0, len(s.blobs)),
		Pages: make([]PageState, 0, len(s.pages)),
	}
	for contentHash, blob := range s.blobs {
		state.Blobs = append(state.Blobs, BlobState{
			ContentHash: contentHash,
			Content:     blob.content,
			TokenCount:  blob.tokens,
		})
	}
	for _, page := range s.pages {
		state.Pages = append(state.Pages, stateFromPage(page))
	}
	sort.Slice(state.Blobs, func(i, j int) bool {
		return state.Blobs[i].ContentHash < state.Blobs[j].ContentHash
	})
	sort.Slice(state.Pages, func(i, j int) bool {
		return state.Pages[i].ID < state.Pages[j].ID
	})
	return state
}

// NewStoreFromState validates a complete snapshot before returning a Store.
// It never publishes partially restored state.
func NewStoreFromState(counter Counter, state StoreState) (*Store, error) {
	if counter == nil {
		return nil, fmt.Errorf("%w: token counter is required", ErrInvalidPage)
	}
	blobs, err := restoreBlobs(counter, state.Blobs)
	if err != nil {
		return nil, err
	}
	store := NewStore()
	seenPages := make(map[PageID]struct{}, len(state.Pages))
	usedBlobs := make(map[string]struct{}, len(blobs))
	for _, item := range state.Pages {
		if _, exists := seenPages[item.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate page %s", ErrInvalidPage, item.ID)
		}
		seenPages[item.ID] = struct{}{}
		blob, exists := blobs[item.ContentHash]
		if !exists {
			return nil, fmt.Errorf("%w: blob %s for page %s", ErrPageNotFound, item.ContentHash, item.ID)
		}
		page := item.page(blob)
		if err := page.Validate(); err != nil {
			return nil, fmt.Errorf("restore page %s: %w", item.ID, err)
		}
		if _, _, err := store.Put(page); err != nil {
			return nil, fmt.Errorf("restore page %s: %w", item.ID, err)
		}
		usedBlobs[item.ContentHash] = struct{}{}
	}
	if err := validateRestoredReferences(store); err != nil {
		return nil, err
	}
	for contentHash := range blobs {
		if _, used := usedBlobs[contentHash]; !used {
			return nil, fmt.Errorf("%w: orphan blob %s", ErrInvalidPage, contentHash)
		}
	}
	return store, nil
}

func restoreBlobs(counter Counter, state []BlobState) (map[string]BlobState, error) {
	blobs := make(map[string]BlobState, len(state))
	for _, blob := range state {
		if _, exists := blobs[blob.ContentHash]; exists {
			return nil, fmt.Errorf("%w: duplicate blob %s", ErrInvalidPage, blob.ContentHash)
		}
		if !validDigest(blob.ContentHash) {
			return nil, fmt.Errorf("%w: invalid blob hash %q", ErrInvalidPage, blob.ContentHash)
		}
		digest := sha256.Sum256([]byte(blob.Content))
		if blob.ContentHash != hex.EncodeToString(digest[:]) {
			return nil, fmt.Errorf("%w: blob hash does not match content", ErrInvalidPage)
		}
		count, err := counter.Count(blob.Content)
		if err != nil {
			return nil, fmt.Errorf("%w: count blob %s tokens: %w", ErrInvalidPage, blob.ContentHash, err)
		}
		if count < 0 || blob.TokenCount < 0 || count != blob.TokenCount {
			return nil, fmt.Errorf("%w: blob %s has stored count %d and restored count %d",
				ErrCounterMismatch, blob.ContentHash, blob.TokenCount, count)
		}
		blobs[blob.ContentHash] = blob
	}
	return blobs, nil
}

func validateRestoredReferences(store *Store) error {
	for pageID, page := range store.pages {
		for _, ref := range page.SourceRefs {
			if _, exists := store.pages[ref.PageID]; !exists {
				return fmt.Errorf("%w: source page %s for page %s", ErrPageNotFound, ref.PageID, pageID)
			}
		}
	}
	return nil
}

func stateFromPage(page Page) PageState {
	return PageState{
		ID:          page.ID,
		ContentHash: page.ContentHash,
		Scope:       page.Scope,
		WorkflowID:  page.WorkflowID,
		AgentID:     page.AgentID,
		SourceRefs:  cloneRefs(page.SourceRefs),
	}
}

func (state PageState) page(blob BlobState) Page {
	return Page{
		ID:          state.ID,
		ContentHash: state.ContentHash,
		Content:     blob.Content,
		Scope:       state.Scope,
		WorkflowID:  state.WorkflowID,
		AgentID:     state.AgentID,
		TokenCount:  blob.TokenCount,
		SourceRefs:  cloneRefs(state.SourceRefs),
	}
}
