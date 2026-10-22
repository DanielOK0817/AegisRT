package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"aegisrt/internal/contextvm"
)

const (
	contextPlanID             = "context-dedup-budget-compression-smoke-v2"
	contextWorkloadVersion    = "context-c1-c2-c3-safe-generator-v2"
	contextContentGenerator   = "safe-token-sequence-v1"
	contextSchemaV2           = "aegisrt.experiment.context/v2"
	contextTrialTraceSchemaV2 = "aegisrt.experiment.context-trial-trace/v2"
	contextCounterName        = "whitespace-v1"
	contextProviderCacheMode  = "none"
	maxContextPlanCells       = 128
)

type contextWorkloadBundle struct {
	Version          string                `json:"version"`
	ContentGenerator string                `json:"content_generator"`
	Cases            []contextCaseWorkload `json:"cases"`
}

type contextCaseWorkload struct {
	CaseID           string                  `json:"case_id"`
	Seed             uint32                  `json:"seed"`
	WorkflowID       string                  `json:"workflow_id"`
	Agents           []string                `json:"agents"`
	ComparisonBudget int                     `json:"comparison_target_budget"`
	Pages            []contextPageDescriptor `json:"pages"`
}

type contextPageDescriptor struct {
	Key           string `json:"key"`
	Scope         string `json:"scope"`
	AgentID       string `json:"agent_id,omitempty"`
	Order         int    `json:"order"`
	Pin           bool   `json:"pin"`
	Cold          bool   `json:"cold"`
	TokenCount    int    `json:"token_count"`
	ContentSHA256 string `json:"content_sha256"`
}

type contextModeInput struct {
	CaseID           string `json:"case_id"`
	Treatment        string `json:"treatment"`
	Role             string `json:"role"`
	ContextMode      string `json:"context_mode"`
	StorageTopology  string `json:"storage_topology"`
	OverBudgetAction string `json:"over_budget_action"`
	SummarizerMode   string `json:"summarizer_mode"`
}

type contextInputContract struct {
	Kind               string             `json:"kind"`
	PlanID             string             `json:"plan_id"`
	WorkloadVersion    string             `json:"workload_version"`
	CounterName        string             `json:"counter_name"`
	ProviderCacheMode  string             `json:"provider_cache_mode"`
	TrialTimeoutMS     int                `json:"trial_timeout_ms"`
	WarmupPairs        int                `json:"warmup_pairs"`
	Pairing            string             `json:"pairing"`
	Counterbalance     string             `json:"counterbalance"`
	BootstrapSeed      uint32             `json:"bootstrap_seed"`
	BootstrapResamples int                `json:"bootstrap_resamples"`
	Plan               ContextPlan        `json:"plan"`
	Modes              []contextModeInput `json:"modes"`
	WorkloadSHA256     string             `json:"workload_sha256"`
}

func buildContextWorkloads(plan ContextPlan) (contextWorkloadBundle, error) {
	bundle := contextWorkloadBundle{Version: contextWorkloadVersion, ContentGenerator: contextContentGenerator}
	for _, seed := range plan.Seeds {
		c1, err := buildC1Workload(seed)
		if err != nil {
			return contextWorkloadBundle{}, err
		}
		c2, err := buildC2Workload(seed)
		if err != nil {
			return contextWorkloadBundle{}, err
		}
		c3, err := buildC3Workload(seed)
		if err != nil {
			return contextWorkloadBundle{}, err
		}
		bundle.Cases = append(bundle.Cases, c1, c2, c3)
	}
	return bundle, nil
}

func buildC3Workload(seed uint32) (contextCaseWorkload, error) {
	workload, err := buildC2Workload(seed)
	if err != nil {
		return contextCaseWorkload{}, err
	}
	workload.CaseID = "C3-smoke"
	workload.WorkflowID = "workflow-c3"
	workload.ComparisonBudget = 8
	return workload, nil
}

func buildC1Workload(seed uint32) (contextCaseWorkload, error) {
	agents := []string{"agent-1", "agent-2", "agent-3", "agent-4"}
	workload := contextCaseWorkload{
		CaseID: "C1-smoke", Seed: seed, WorkflowID: "workflow-c1", Agents: agents, ComparisonBudget: 6,
	}
	for index, agentID := range agents {
		descriptor, err := contextPage("shared-agent-context", contextvm.ScopeAgent, agentID, index, false, false, seed)
		if err != nil {
			return contextCaseWorkload{}, err
		}
		workload.Pages = append(workload.Pages, descriptor)
	}
	return workload, nil
}

func buildC2Workload(seed uint32) (contextCaseWorkload, error) {
	workload := contextCaseWorkload{
		CaseID: "C2-smoke", Seed: seed, WorkflowID: "workflow-c2", Agents: []string{"agent-context"}, ComparisonBudget: 6,
	}
	inputs := []struct {
		key       string
		pin, cold bool
	}{
		{key: "large-optional", cold: true},
		{key: "small-optional"},
		{key: "pinned-core", pin: true},
	}
	for order, input := range inputs {
		descriptor, err := contextPage(input.key, contextvm.ScopeWorkflow, "", order, input.pin, input.cold, seed)
		if err != nil {
			return contextCaseWorkload{}, err
		}
		workload.Pages = append(workload.Pages, descriptor)
	}
	return workload, nil
}

func contextPage(key string, scope contextvm.Scope, agentID string, order int, pin, cold bool, seed uint32) (contextPageDescriptor, error) {
	content, err := generatedContextContent(key, seed)
	if err != nil {
		return contextPageDescriptor{}, err
	}
	tokens, err := (contextvm.WhitespaceCounter{}).Count(content)
	if err != nil {
		return contextPageDescriptor{}, err
	}
	sum := sha256.Sum256([]byte(content))
	return contextPageDescriptor{
		Key: key, Scope: string(scope), AgentID: agentID, Order: order, Pin: pin, Cold: cold,
		TokenCount: tokens, ContentSHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func generatedContextContent(key string, seed uint32) (string, error) {
	switch key {
	case "shared-agent-context":
		return fmt.Sprintf("shared deterministic context alpha beta seed-%d", seed), nil
	case "pinned-core":
		return fmt.Sprintf("pinned core seed-%d", seed), nil
	case "large-optional":
		return "large optional context has five tokens", nil
	case "small-optional":
		return "small optional note", nil
	default:
		return "", fmt.Errorf("unsupported context content key %q", key)
	}
}

func buildContextInput(plan ContextPlan, workloadHash string) contextInputContract {
	return contextInputContract{
		Kind: "context", PlanID: contextPlanID, WorkloadVersion: contextWorkloadVersion,
		CounterName: contextCounterName, ProviderCacheMode: contextProviderCacheMode,
		TrialTimeoutMS: trialTimeoutMS, WarmupPairs: 0, Pairing: "seed-and-repetition-v1",
		Counterbalance: "alternating-seed-index-v1", BootstrapSeed: 424242, BootstrapResamples: 10000,
		Plan: plan, WorkloadSHA256: workloadHash,
		Modes: []contextModeInput{
			{CaseID: "C1-smoke", Treatment: "copy-per-task", Role: "baseline", ContextMode: "isolated-store-copy-v1", StorageTopology: "store-per-agent", OverBudgetAction: "fail", SummarizerMode: "disabled"},
			{CaseID: "C1-smoke", Treatment: "contextvm-dedup", Role: "treatment", ContextMode: "shared-store-blob-dedup-v1", StorageTopology: "shared-store", OverBudgetAction: "fail", SummarizerMode: "disabled"},
			{CaseID: "C2-smoke", Treatment: "unbudgeted-logical", Role: "baseline", ContextMode: "unbudgeted-assembly-v1", StorageTopology: "single-store", OverBudgetAction: "allow-for-baseline", SummarizerMode: "disabled"},
			{CaseID: "C2-smoke", Treatment: "contextvm-budgeted", Role: "treatment", ContextMode: "budgeted-drop-no-summary-v1", StorageTopology: "single-store", OverBudgetAction: "drop-unpinned", SummarizerMode: "disabled"},
			{CaseID: "C3-smoke", Treatment: "contextvm-drop", Role: "baseline", ContextMode: "budgeted-drop-no-summary-v1", StorageTopology: "single-store", OverBudgetAction: "drop-unpinned", SummarizerMode: "disabled"},
			{CaseID: "C3-smoke", Treatment: "contextvm-compress", Role: "treatment", ContextMode: "budgeted-extractive-summary-v1", StorageTopology: "single-store", OverBudgetAction: "summarize-cold-then-drop", SummarizerMode: "deterministic-extractive-v1"},
		},
	}
}

func validateContextPlan(plan ContextPlan) error {
	if len(plan.Seeds) == 0 || len(plan.Seeds) > 128 {
		return fmt.Errorf("seeds must contain between 1 and 128 values")
	}
	if plan.Repetitions <= 0 || plan.Repetitions > 1000 {
		return fmt.Errorf("repetitions must be between 1 and 1000")
	}
	if len(plan.Seeds)*plan.Repetitions > maxContextPlanCells {
		return fmt.Errorf("seed-repetition cells must not exceed %d", maxContextPlanCells)
	}
	seen := make(map[uint32]struct{}, len(plan.Seeds))
	for _, seed := range plan.Seeds {
		if _, exists := seen[seed]; exists {
			return fmt.Errorf("duplicate seed %d", seed)
		}
		seen[seed] = struct{}{}
	}
	return nil
}
