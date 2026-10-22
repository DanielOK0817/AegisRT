package contextvm

import (
	"errors"
	"reflect"
	"testing"
)

func TestStoreStateDeduplicatesContentAndSortsDeterministically(t *testing.T) {
	store := NewStore()
	shared := mustPage(t, PageSpec{
		Content: "shared body", Scope: ScopeWorkflow, WorkflowID: "workflow-1",
	})
	private := mustPage(t, PageSpec{
		Content: "shared body", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1",
	})
	summary := mustPage(t, PageSpec{
		Content: "summary body", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1",
		SourceRefs: []ContextRef{{PageID: private.ID, Cold: true}},
	})
	for _, page := range []Page{summary, private, shared} {
		mustPut(t, store, page)
	}

	first := store.State()
	second := store.State()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("State() is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if len(first.Blobs) != 2 || len(first.Pages) != 3 {
		t.Fatalf("State() sizes = blobs %d pages %d, want 2 and 3", len(first.Blobs), len(first.Pages))
	}
	if first.Blobs[0].ContentHash > first.Blobs[1].ContentHash {
		t.Fatalf("blobs are not sorted: %#v", first.Blobs)
	}
	for index := 1; index < len(first.Pages); index++ {
		if first.Pages[index-1].ID > first.Pages[index].ID {
			t.Fatalf("pages are not sorted: %#v", first.Pages)
		}
	}
	for _, page := range first.Pages {
		if page.ContentHash == "" {
			t.Fatalf("PageState has no content hash: %#v", page)
		}
	}
	for index := range first.Pages {
		if len(first.Pages[index].SourceRefs) > 0 {
			first.Pages[index].SourceRefs[0].Cold = false
		}
	}
	if !reflect.DeepEqual(second, store.State()) {
		t.Fatal("State() exposed Store reference backing storage")
	}
}

func TestNewStoreFromStateRoundTripAndRebuildsStructuralStats(t *testing.T) {
	original := NewStore()
	shared := mustPage(t, PageSpec{
		Content: "same words", Scope: ScopeWorkflow, WorkflowID: "workflow-1",
	})
	private := mustPage(t, PageSpec{
		Content: "same words", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1",
	})
	summary := mustPage(t, PageSpec{
		Content: "short summary", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1",
		SourceRefs: []ContextRef{{PageID: private.ID}},
	})
	for _, page := range []Page{shared, private, summary} {
		mustPut(t, original, page)
	}

	restored, err := NewStoreFromState(WhitespaceCounter{}, original.State())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.State(), original.State()) {
		t.Fatalf("restored state = %#v, want %#v", restored.State(), original.State())
	}
	for _, page := range []Page{shared, private, summary} {
		got, err := restored.Get(ContextRef{PageID: page.ID}, AccessRequest{
			WorkflowID: "workflow-1", AgentID: "agent-1",
		})
		if err != nil {
			t.Fatalf("Get(%s): %v", page.ID, err)
		}
		if !pagesEqual(got, page) {
			t.Fatalf("restored page = %#v, want %#v", got, page)
		}
	}
	stats := restored.Stats()
	if stats.PutCalls != 3 || stats.GetCalls != 3 || stats.UniquePages != 3 || stats.UniqueBlobs != 2 || stats.ReusedPuts != 1 {
		t.Fatalf("restored structural stats = %#v", stats)
	}
	if stats.LogicalBytes != uint64(2*len("same words")+len("short summary")) ||
		stats.PhysicalBytes != uint64(len("same words")+len("short summary")) {
		t.Fatalf("restored byte stats = %#v", stats)
	}
	if stats.LogicalTokens != 6 || stats.PhysicalTokens != 4 || stats.ReusedTokens != 2 {
		t.Fatalf("restored token stats = %#v", stats)
	}
}

func TestNewStoreFromStateRejectsCorruptOrDanglingState(t *testing.T) {
	store := NewStore()
	source := mustPage(t, PageSpec{
		Content: "source page", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1",
	})
	derived := mustPage(t, PageSpec{
		Content: "derived page", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1",
		SourceRefs: []ContextRef{{PageID: source.ID}},
	})
	mustPut(t, store, source)
	mustPut(t, store, derived)
	valid := store.State()

	tests := []struct {
		name   string
		mutate func(*StoreState)
		want   error
	}{
		{
			name: "duplicate blob",
			mutate: func(state *StoreState) {
				state.Blobs = append(state.Blobs, state.Blobs[0])
			},
			want: ErrInvalidPage,
		},
		{
			name: "blob hash mismatch",
			mutate: func(state *StoreState) {
				state.Blobs[0].ContentHash = state.Blobs[1].ContentHash
			},
			want: ErrInvalidPage,
		},
		{
			name: "token mismatch",
			mutate: func(state *StoreState) {
				state.Blobs[0].TokenCount++
			},
			want: ErrCounterMismatch,
		},
		{
			name: "duplicate page",
			mutate: func(state *StoreState) {
				state.Pages = append(state.Pages, clonePageState(state.Pages[0]))
			},
			want: ErrInvalidPage,
		},
		{
			name: "missing blob",
			mutate: func(state *StoreState) {
				state.Blobs = state.Blobs[1:]
			},
			want: ErrPageNotFound,
		},
		{
			name: "invalid page id",
			mutate: func(state *StoreState) {
				state.Pages[0].ID = PageID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			},
			want: ErrInvalidPage,
		},
		{
			name: "dangling source ref",
			mutate: func(state *StoreState) {
				for index := range state.Pages {
					if len(state.Pages[index].SourceRefs) > 0 {
						state.Pages[index].SourceRefs[0].PageID = PageID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
						blob := blobForPage(*state, state.Pages[index])
						page := state.Pages[index].page(blob)
						state.Pages[index].ID = calculatePageID(page)
					}
				}
			},
			want: ErrPageNotFound,
		},
		{
			name: "orphan blob",
			mutate: func(state *StoreState) {
				orphan := mustPage(t, PageSpec{Content: "orphan", Scope: ScopeSystem})
				state.Blobs = append(state.Blobs, BlobState{
					ContentHash: orphan.ContentHash, Content: orphan.Content, TokenCount: orphan.TokenCount,
				})
			},
			want: ErrInvalidPage,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cloneStoreState(valid)
			test.mutate(&state)
			if _, err := NewStoreFromState(WhitespaceCounter{}, state); !errors.Is(err, test.want) {
				t.Fatalf("NewStoreFromState() error = %v, want %v", err, test.want)
			}
		})
	}
	if _, err := NewStoreFromState(nil, valid); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("nil counter error = %v, want ErrInvalidPage", err)
	}
}

func cloneStoreState(state StoreState) StoreState {
	result := StoreState{Blobs: append([]BlobState(nil), state.Blobs...)}
	result.Pages = make([]PageState, len(state.Pages))
	for index, page := range state.Pages {
		result.Pages[index] = clonePageState(page)
	}
	return result
}

func clonePageState(page PageState) PageState {
	page.SourceRefs = append([]ContextRef(nil), page.SourceRefs...)
	return page
}

func blobForPage(state StoreState, page PageState) BlobState {
	for _, blob := range state.Blobs {
		if blob.ContentHash == page.ContentHash {
			return blob
		}
	}
	return BlobState{}
}
