package benchmark

type ContextPlan struct {
	Seeds       []uint32 `json:"seeds"`
	Repetitions int      `json:"repetitions"`
}

type ContextCollectOptions struct {
	OutputDir string
	Plan      ContextPlan
}

type ContextResult struct {
	Rows           []ContextRow
	WorkloadSHA256 string
	InputSHA256    string
	traces         []trialTrace
}

type ContextEvidence struct {
	Result       ContextResult
	workloadJSON []byte
	inputJSON    []byte
	trialTraces  []ContextTrialTrace
}

func (e ContextEvidence) CanonicalWorkloadJSON() []byte {
	return append([]byte(nil), e.workloadJSON...)
}

func (e ContextEvidence) CanonicalInputJSON() []byte {
	return append([]byte(nil), e.inputJSON...)
}

func (e ContextEvidence) TrialTraceRecords() []ContextTrialTrace {
	result := make([]ContextTrialTrace, len(e.trialTraces))
	for index, record := range e.trialTraces {
		result[index] = cloneContextTrialTrace(record)
	}
	return result
}

type ContextRow struct {
	SchemaVersion           string
	RunID                   string
	Status                  string
	EvidenceScope           string
	CaseID                  string
	PairID                  string
	Seed                    uint32
	Repetition              int
	TrialOrder              int
	Treatment               string
	Role                    string
	CounterName             string
	ContextMode             string
	ProviderCacheMode       string
	WorkloadSHA256          string
	PageCount               int
	StorageLogicalTokens    int
	StoragePhysicalTokens   int
	StorageReusedTokens     int
	DroppedTokens           int
	SummaryPages            int
	SummarizedSourceTokens  int
	SummaryTokens           int
	DispatchInputTokens     int
	TargetBudgetExceedances int
	ElapsedMS               float64
	ManifestOperations      int
	ManifestConflicts       int
	TimeoutMS               int
	ReasonCode              string
}

type ContextTrialTrace struct {
	SchemaVersion      string                 `json:"schema_version"`
	RunID              string                 `json:"run_id"`
	EvidenceScope      string                 `json:"evidence_scope"`
	PlanID             string                 `json:"plan_id"`
	CaseID             string                 `json:"case_id"`
	PairID             string                 `json:"pair_id"`
	Seed               uint32                 `json:"seed"`
	Repetition         int                    `json:"repetition"`
	TrialOrder         int                    `json:"trial_order"`
	Treatment          string                 `json:"treatment"`
	Role               string                 `json:"role"`
	CounterName        string                 `json:"counter_name"`
	ContextMode        string                 `json:"context_mode"`
	ProviderCacheMode  string                 `json:"provider_cache_mode"`
	WorkloadSHA256     string                 `json:"workload_sha256"`
	InputSHA256        string                 `json:"input_sha256"`
	Status             string                 `json:"status"`
	ComparisonBudget   int                    `json:"comparison_target_budget"`
	SummarizerMode     string                 `json:"summarizer_mode"`
	ElapsedNS          int64                  `json:"elapsed_ns"`
	ManifestOperations int                    `json:"manifest_operations"`
	ManifestConflicts  int                    `json:"manifest_conflicts"`
	Stores             []ContextStoreTrace    `json:"stores"`
	Dispatches         []ContextDispatchTrace `json:"dispatches"`
	ACLProbes          []ContextACLProbe      `json:"acl_probes"`
}

type ContextStoreTrace struct {
	StoreID        string `json:"store_id"`
	PutCalls       int    `json:"put_calls"`
	UniquePages    int    `json:"unique_pages"`
	UniqueBlobs    int    `json:"unique_blobs"`
	ReusedPuts     int    `json:"reused_puts"`
	LogicalTokens  int    `json:"logical_tokens"`
	PhysicalTokens int    `json:"physical_tokens"`
	ReusedTokens   int    `json:"reused_tokens"`
}

type ContextDispatchTrace struct {
	TaskID                 string                    `json:"task_id"`
	AgentID                string                    `json:"agent_id"`
	StoreID                string                    `json:"store_id"`
	TextSHA256             string                    `json:"assembled_text_sha256"`
	AppliedBudget          int                       `json:"applied_budget"`
	ReferencedTokens       int                       `json:"referenced_tokens"`
	AssembledTokens        int                       `json:"assembled_tokens"`
	ReusedRefTokens        int                       `json:"reused_ref_tokens"`
	DroppedTokens          int                       `json:"dropped_tokens"`
	SelectedPages          int                       `json:"selected_pages"`
	DroppedPages           int                       `json:"dropped_pages"`
	SummaryPages           int                       `json:"summary_pages"`
	SummarizedSourceTokens int                       `json:"summarized_source_tokens"`
	SummaryTokens          int                       `json:"summary_tokens"`
	PinnedPageRequired     bool                      `json:"pinned_page_required"`
	PinnedPagePreserved    bool                      `json:"pinned_page_preserved"`
	Pages                  []ContextPageOutcomeTrace `json:"pages"`
}

type ContextPageOutcomeTrace struct {
	PageID             string `json:"page_id"`
	ContentSHA256      string `json:"content_sha256"`
	Scope              string `json:"scope"`
	OwnerAgentID       string `json:"owner_agent_id"`
	TokenCount         int    `json:"token_count"`
	Pin                bool   `json:"pin"`
	Cold               bool   `json:"cold"`
	Outcome            string `json:"outcome"`
	SummaryPageID      string `json:"summary_page_id,omitempty"`
	SummaryContentHash string `json:"summary_content_sha256,omitempty"`
	SummaryTokenCount  int    `json:"summary_token_count,omitempty"`
}

type ContextACLProbe struct {
	PageID         string `json:"page_id"`
	OwnerAgentID   string `json:"owner_agent_id"`
	RequestAgentID string `json:"request_agent_id"`
	ResultCode     string `json:"result_code"`
}

func cloneContextTrialTrace(record ContextTrialTrace) ContextTrialTrace {
	record.Stores = append([]ContextStoreTrace(nil), record.Stores...)
	record.ACLProbes = append([]ContextACLProbe(nil), record.ACLProbes...)
	record.Dispatches = append([]ContextDispatchTrace(nil), record.Dispatches...)
	for index := range record.Dispatches {
		record.Dispatches[index].Pages = append([]ContextPageOutcomeTrace(nil), record.Dispatches[index].Pages...)
	}
	return record
}
