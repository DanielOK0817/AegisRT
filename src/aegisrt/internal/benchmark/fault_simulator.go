package benchmark

import (
	"context"
	"fmt"
	"time"
)

func SimulateFaultEvidence(
	ctx context.Context,
	plan FaultPlan,
	command FaultAgentCommand,
) (FaultEvidence, error) {
	if ctx == nil {
		return FaultEvidence{}, fmt.Errorf("context is required")
	}
	if err := validateFaultPlan(plan); err != nil {
		return FaultEvidence{}, err
	}
	if err := validateFaultAgentCommand(command); err != nil {
		return FaultEvidence{}, err
	}
	workloads := buildFaultWorkloads(plan)
	workloadJSON, err := canonicalJSON(workloads)
	if err != nil {
		return FaultEvidence{}, err
	}
	workloadHash := hashBytes(workloadJSON)
	inputJSON, err := canonicalJSON(buildFaultInput(plan, workloadHash))
	if err != nil {
		return FaultEvidence{}, err
	}
	inputHash := hashBytes(inputJSON)

	evidence := FaultEvidence{
		Result:       FaultResult{WorkloadSHA256: workloadHash, InputSHA256: inputHash},
		workloadJSON: append([]byte(nil), workloadJSON...),
		inputJSON:    append([]byte(nil), inputJSON...),
	}
	for seedIndex := range plan.Seeds {
		start := seedIndex * 3
		for _, workload := range workloads.Cases[start : start+3] {
			for repetition := 0; repetition < plan.Repetitions; repetition++ {
				modes, modeErr := faultModes(workload.CaseID)
				if modeErr != nil {
					return FaultEvidence{}, modeErr
				}
				if seedIndex%2 == 1 {
					modes[0], modes[1] = modes[1], modes[0]
				}
				pairID := faultPairID(workload.CaseID, workload.Seed, repetition)
				for order, mode := range modes {
					started := time.Now()
					trialCtx, cancel := context.WithTimeout(ctx, time.Duration(workload.TrialTimeoutMS)*time.Millisecond)
					trace, runErr := runFaultTrial(trialCtx, workload, mode, command)
					cancel()
					finished := time.Now()
					if runErr != nil {
						return FaultEvidence{}, fmt.Errorf("run %s/%s seed %d: %w", workload.CaseID, mode.treatment, workload.Seed, runErr)
					}
					if err := ctx.Err(); err != nil {
						return FaultEvidence{}, err
					}
					trace = bindFaultTrialIdentity(
						trace, workload, mode, pairID, repetition, order, workloadHash, inputHash,
					)
					row, rowErr := faultRowFromTrace(trace)
					if rowErr != nil {
						return FaultEvidence{}, rowErr
					}
					evidence.Result.Rows = append(evidence.Result.Rows, row)
					evidence.Result.traces = append(evidence.Result.traces, trialTrace{
						rowIndex: len(evidence.Result.Rows) - 1,
						started:  started,
						finished: finished,
					})
					evidence.trialTraces = append(evidence.trialTraces, trace)
				}
			}
		}
	}
	if err := validateFaultEvidencePairs(evidence.Result.Rows, evidence.trialTraces); err != nil {
		return FaultEvidence{}, err
	}
	return evidence, nil
}

func runFaultTrial(
	ctx context.Context,
	workload faultCaseWorkload,
	mode faultTrialMode,
	command FaultAgentCommand,
) (FaultTrialTrace, error) {
	switch workload.CaseID {
	case "F1-smoke", "F2-smoke":
		return runContainedFaultTrial(ctx, workload, mode, command)
	case "F3-service-smoke":
		return runServiceRestartFaultTrial(ctx, workload, mode, command)
	default:
		return FaultTrialTrace{}, fmt.Errorf("unsupported fault case %q", workload.CaseID)
	}
}

func bindFaultTrialIdentity(
	trace FaultTrialTrace,
	workload faultCaseWorkload,
	mode faultTrialMode,
	pairID string,
	repetition, order int,
	workloadHash, inputHash string,
) FaultTrialTrace {
	trace.SchemaVersion = faultTrialTraceSchemaV1
	trace.EvidenceScope = "windows-functional"
	trace.PlanID = faultPlanID
	trace.CaseID = workload.CaseID
	trace.PairID = pairID
	trace.Seed = workload.Seed
	trace.Repetition = repetition
	trace.TrialOrder = order
	trace.Treatment = mode.treatment
	trace.Role = mode.role
	trace.WorkloadSHA256 = workloadHash
	trace.InputSHA256 = inputHash
	trace.Status = "SUCCEEDED"
	return trace
}
