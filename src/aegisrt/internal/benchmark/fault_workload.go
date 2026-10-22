package benchmark

import (
	"fmt"
	"strings"
)

const (
	faultPlanID             = "fault-containment-restart-smoke-v1"
	faultWorkloadVersion    = "fault-safe-process-workload-v1"
	faultFixtureProtocol    = "aegisbench-fault-agent-v1"
	faultSchemaV1           = "aegisrt.experiment.fault/v1"
	faultTrialTraceSchemaV1 = "aegisrt.experiment.fault-trial-trace/v1"
	faultClockMode          = "harness-monotonic-v1"
	maxFaultPlanCells       = 16
	faultRestartHoldMS      = 500
	faultTimeoutHoldMS      = 1500
	faultAgentTimeoutMS     = 500
	faultNormalTimeoutMS    = 5000
)

type faultWorkloadBundle struct {
	Version         string              `json:"version"`
	FixtureProtocol string              `json:"fixture_protocol"`
	Cases           []faultCaseWorkload `json:"cases"`
}

type faultCaseWorkload struct {
	CaseID          string              `json:"case_id"`
	Seed            uint32              `json:"seed"`
	WorkflowID      string              `json:"workflow_id"`
	Target          faultTaskDescriptor `json:"target"`
	Unaffected      faultTaskDescriptor `json:"unaffected"`
	TargetHoldMS    int                 `json:"target_hold_ms"`
	TargetTimeoutMS int                 `json:"target_timeout_ms"`
	TrialTimeoutMS  int                 `json:"trial_timeout_ms"`
}

type faultTaskDescriptor struct {
	TaskID         string `json:"task_id"`
	AgentID        string `json:"agent_id"`
	Priority       int    `json:"priority"`
	MaxAttempts    int    `json:"max_attempts"`
	IdempotencyKey string `json:"idempotency_key"`
}

type faultModeInput struct {
	CaseID           string `json:"case_id"`
	Treatment        string `json:"treatment"`
	Role             string `json:"role"`
	FaultMode        string `json:"fault_mode"`
	Injection        string `json:"injection_boundary"`
	ExpectedRecovery string `json:"expected_recovery"`
}

type faultInputContract struct {
	Kind               string           `json:"kind"`
	PlanID             string           `json:"plan_id"`
	WorkloadVersion    string           `json:"workload_version"`
	FixtureProtocol    string           `json:"fixture_protocol"`
	EvidenceScope      string           `json:"evidence_scope"`
	ClockMode          string           `json:"clock_mode"`
	WarmupPairs        int              `json:"warmup_pairs"`
	Pairing            string           `json:"pairing"`
	Counterbalance     string           `json:"counterbalance"`
	BootstrapSeed      uint32           `json:"bootstrap_seed"`
	BootstrapResamples int              `json:"bootstrap_resamples"`
	Plan               FaultPlan        `json:"plan"`
	Modes              []faultModeInput `json:"modes"`
	WorkloadSHA256     string           `json:"workload_sha256"`
}

func buildFaultWorkloads(plan FaultPlan) faultWorkloadBundle {
	bundle := faultWorkloadBundle{Version: faultWorkloadVersion, FixtureProtocol: faultFixtureProtocol}
	for _, seed := range plan.Seeds {
		bundle.Cases = append(bundle.Cases,
			newFaultCase("F1-smoke", seed, 1, 0, faultNormalTimeoutMS),
			newFaultCase("F2-smoke", seed, 1, faultTimeoutHoldMS, faultAgentTimeoutMS),
			newFaultCase("F3-service-smoke", seed, 2, faultRestartHoldMS, faultNormalTimeoutMS),
		)
	}
	return bundle
}

func newFaultCase(caseID string, seed uint32, maxAttempts, holdMS, timeoutMS int) faultCaseWorkload {
	prefix := strings.ToLower(strings.TrimSuffix(caseID, "-smoke"))
	prefix = strings.ReplaceAll(prefix, "-service", "")
	workflowID := fmt.Sprintf("workflow-%s-%d", prefix, seed)
	return faultCaseWorkload{
		CaseID: caseID, Seed: seed, WorkflowID: workflowID,
		Target: faultTaskDescriptor{
			TaskID: fmt.Sprintf("%s-target-%d", prefix, seed), AgentID: "fault-target-agent",
			Priority: 10, MaxAttempts: maxAttempts, IdempotencyKey: fmt.Sprintf("%s-target-key-%d", prefix, seed),
		},
		Unaffected: faultTaskDescriptor{
			TaskID: fmt.Sprintf("%s-survivor-%d", prefix, seed), AgentID: "fault-survivor-agent",
			Priority: 1, MaxAttempts: 1, IdempotencyKey: fmt.Sprintf("%s-survivor-key-%d", prefix, seed),
		},
		TargetHoldMS: holdMS, TargetTimeoutMS: timeoutMS, TrialTimeoutMS: trialTimeoutMS,
	}
}

func buildFaultInput(plan FaultPlan, workloadHash string) faultInputContract {
	return faultInputContract{
		Kind: "fault", PlanID: faultPlanID, WorkloadVersion: faultWorkloadVersion,
		FixtureProtocol: faultFixtureProtocol, EvidenceScope: "windows-functional", ClockMode: faultClockMode,
		WarmupPairs: 0, Pairing: "seed-and-repetition-v1", Counterbalance: "alternating-seed-index-v1",
		BootstrapSeed: 424242, BootstrapResamples: 10000, Plan: plan, WorkloadSHA256: workloadHash,
		Modes: []faultModeInput{
			{CaseID: "F1-smoke", Treatment: "no-injection", Role: "baseline", FaultMode: "none", Injection: "matched-dispatch-control", ExpectedRecovery: "normal-completion"},
			{CaseID: "F1-smoke", Treatment: "agent-exit", Role: "treatment", FaultMode: "agent_exit", Injection: "agent-process-start", ExpectedRecovery: "contained-terminal-failure"},
			{CaseID: "F2-smoke", Treatment: "no-injection", Role: "baseline", FaultMode: "none", Injection: "matched-dispatch-control", ExpectedRecovery: "normal-completion"},
			{CaseID: "F2-smoke", Treatment: "agent-timeout", Role: "treatment", FaultMode: "agent_timeout", Injection: "configured-agent-deadline", ExpectedRecovery: "contained-terminal-failure"},
			{CaseID: "F3-service-smoke", Treatment: "no-injection", Role: "baseline", FaultMode: "none", Injection: "matched-service-control", ExpectedRecovery: "normal-completion"},
			{CaseID: "F3-service-smoke", Treatment: "runservice-graceful-restart", Role: "treatment", FaultMode: "runservice_graceful_restart", Injection: "active-attempt-root-cancel", ExpectedRecovery: "requeue-then-complete"},
		},
	}
}

func validateFaultPlan(plan FaultPlan) error {
	if len(plan.Seeds) == 0 || len(plan.Seeds) > maxFaultPlanCells {
		return fmt.Errorf("fault seeds must contain between 1 and %d values", maxFaultPlanCells)
	}
	if plan.Repetitions <= 0 || plan.Repetitions > maxFaultPlanCells || len(plan.Seeds)*plan.Repetitions > maxFaultPlanCells {
		return fmt.Errorf("fault seed-repetition cells must be between 1 and %d", maxFaultPlanCells)
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

// ValidateFaultPlan checks the bounded public fault evidence plan contract.
func ValidateFaultPlan(plan FaultPlan) error {
	return validateFaultPlan(plan)
}

func validateFaultAgentCommand(command FaultAgentCommand) error {
	if strings.TrimSpace(command.Entrypoint) == "" || strings.ContainsRune(command.Entrypoint, 0) || len(command.ArgsPrefix) > 16 {
		return fmt.Errorf("fault Agent command is invalid")
	}
	for _, argument := range command.ArgsPrefix {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("fault Agent command argument is invalid")
		}
	}
	return nil
}

func faultModes(caseID string) ([2]faultTrialMode, error) {
	switch caseID {
	case "F1-smoke":
		return [2]faultTrialMode{{"no-injection", "baseline", "none"}, {"agent-exit", "treatment", "agent_exit"}}, nil
	case "F2-smoke":
		return [2]faultTrialMode{{"no-injection", "baseline", "none"}, {"agent-timeout", "treatment", "agent_timeout"}}, nil
	case "F3-service-smoke":
		return [2]faultTrialMode{{"no-injection", "baseline", "none"}, {"runservice-graceful-restart", "treatment", "runservice_graceful_restart"}}, nil
	default:
		return [2]faultTrialMode{}, fmt.Errorf("unsupported fault case %q", caseID)
	}
}

func faultPairID(caseID string, seed uint32, repetition int) string {
	prefix := strings.ToLower(strings.TrimSuffix(caseID, "-smoke"))
	return fmt.Sprintf("%s-smoke-%d-%d", prefix, seed, repetition)
}
