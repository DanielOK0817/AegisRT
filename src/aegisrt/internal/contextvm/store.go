package contextvm

import (
	"fmt"
	"sync"
)

type StoreStats struct {
	PutCalls       uint64 `json:"put_calls"`
	GetCalls       uint64 `json:"get_calls"`
	UniquePages    uint64 `json:"unique_pages"`
	UniqueBlobs    uint64 `json:"unique_blobs"`
	ReusedPuts     uint64 `json:"reused_puts"`
	LogicalBytes   uint64 `json:"logical_bytes"`
	PhysicalBytes  uint64 `json:"physical_bytes"`
	LogicalTokens  uint64 `json:"logical_tokens"`
	PhysicalTokens uint64 `json:"physical_tokens"`
	ReusedTokens   uint64 `json:"reused_tokens"`
}

type Store struct {
	mu    sync.RWMutex
	pages map[PageID]Page
	blobs map[string]contentBlob
	stats StoreStats
}

type contentBlob struct {
	content string
	tokens  int
}

func NewStore() *Store {
	return &Store{pages: make(map[PageID]Page), blobs: make(map[string]contentBlob)}
}

// Clone creates an isolated transaction candidate while retaining in-process
// access counters. Durable restoration deliberately rebuilds those counters.
func (s *Store) Clone() *Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := NewStore()
	for id, page := range s.pages {
		result.pages[id] = clonePage(page)
	}
	for id, blob := range s.blobs {
		result.blobs[id] = blob
	}
	result.stats = s.stats
	return result
}

// Put stores an immutable page. The returned bool is true when the exact page
// already existed and no additional physical bytes were stored.
func (s *Store) Put(page Page) (ContextRef, bool, error) {
	if s == nil {
		return ContextRef{}, false, fmt.Errorf("%w: store is nil", ErrInvalidPage)
	}
	if err := page.Validate(); err != nil {
		return ContextRef{}, false, err
	}
	page = clonePage(page)
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, blobExists := s.blobs[page.ContentHash]
	if blobExists {
		if blob.content != page.Content {
			return ContextRef{}, false, fmt.Errorf("%w: content hash %s", ErrHashCollision, page.ContentHash)
		}
		if blob.tokens != page.TokenCount {
			return ContextRef{}, false, fmt.Errorf("%w: content %s has counts %d and %d",
				ErrCounterMismatch, page.ContentHash, blob.tokens, page.TokenCount)
		}
		page.Content = blob.content
	}
	if existing, exists := s.pages[page.ID]; exists {
		if !pagesEqual(existing, page) {
			return ContextRef{}, false, fmt.Errorf("%w: %s", ErrHashCollision, page.ID)
		}
		s.stats.PutCalls++
		s.stats.ReusedPuts++
		s.stats.LogicalBytes += uint64(len(page.Content))
		s.stats.LogicalTokens += uint64(page.TokenCount)
		s.stats.ReusedTokens += uint64(page.TokenCount)
		return ContextRef{PageID: page.ID}, true, nil
	}
	s.pages[page.ID] = page
	s.stats.PutCalls++
	s.stats.UniquePages++
	s.stats.LogicalBytes += uint64(len(page.Content))
	s.stats.LogicalTokens += uint64(page.TokenCount)
	if blobExists {
		s.stats.ReusedPuts++
		s.stats.ReusedTokens += uint64(page.TokenCount)
	} else {
		s.blobs[page.ContentHash] = contentBlob{content: page.Content, tokens: page.TokenCount}
		s.stats.UniqueBlobs++
		s.stats.PhysicalBytes += uint64(len(page.Content))
		s.stats.PhysicalTokens += uint64(page.TokenCount)
	}
	return ContextRef{PageID: page.ID}, false, nil
}

func (s *Store) Get(ref ContextRef, request AccessRequest) (Page, error) {
	if s == nil {
		return Page{}, fmt.Errorf("%w: store is nil", ErrPageNotFound)
	}
	if err := ref.Validate(); err != nil {
		return Page{}, err
	}
	s.mu.Lock()
	page, exists := s.pages[ref.PageID]
	s.stats.GetCalls++
	s.mu.Unlock()
	if !exists {
		return Page{}, fmt.Errorf("%w: %s", ErrPageNotFound, ref.PageID)
	}
	if err := Authorize(page, request); err != nil {
		return Page{}, err
	}
	return clonePage(page), nil
}

func (s *Store) lookup(id PageID) (Page, error) {
	if s == nil {
		return Page{}, fmt.Errorf("%w: store is nil", ErrPageNotFound)
	}
	s.mu.RLock()
	page, exists := s.pages[id]
	s.mu.RUnlock()
	if !exists {
		return Page{}, fmt.Errorf("%w: %s", ErrPageNotFound, id)
	}
	return clonePage(page), nil
}

// Lookup returns immutable page metadata for trusted repository integrity
// checks. Runtime access paths must continue to use Get so ACLs are enforced.
func (s *Store) Lookup(id PageID) (Page, error) {
	return s.lookup(id)
}

func (s *Store) Stats() StoreStats {
	if s == nil {
		return StoreStats{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

func pagesEqual(left, right Page) bool {
	if left.ID != right.ID || left.ContentHash != right.ContentHash || left.Content != right.Content ||
		left.Scope != right.Scope || left.WorkflowID != right.WorkflowID || left.AgentID != right.AgentID ||
		left.TokenCount != right.TokenCount || len(left.SourceRefs) != len(right.SourceRefs) {
		return false
	}
	for index := range left.SourceRefs {
		if left.SourceRefs[index] != right.SourceRefs[index] {
			return false
		}
	}
	return true
}
