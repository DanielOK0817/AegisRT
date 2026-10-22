package benchmark

import (
	"fmt"

	"aegisrt/internal/domain"
)

func validateFaultCaseSemantics(trace FaultTrialTrace) error {
	if err := validateFaultSurvivor(trace.UnaffectedTasks[0]); err != nil {
		return err
	}
	if trace.Role == "baseline" {
		if err := validateFaultSuccessAttempt(trace.Target); err != nil {
			return fmt.Errorf("baseline target: %w", err)
		}
		if trace.ObservedReasonCode != "NONE" {
			return fmt.Errorf("baseline has a fault reason")
		}
	} else {
		switch trace.CaseID {
		case "F1-smoke":
			attempt := trace.Target.Attempts[0]
			if trace.Target.FinalStatus != domain.TaskStatusFailed || trace.Target.AttemptCount != 1 ||
				attempt.Status != domain.AttemptStatusFailed || attempt.ExitCode == nil || *attempt.ExitCode != 7 ||
				attempt.TimedOut || attempt.Canceled || attempt.ReasonCode != "AGENT_EXIT" ||
				trace.ObservedReasonCode != "AGENT_EXIT" {
				return fmt.Errorf("F1 treatment does not prove contained exit")
			}
		case "F2-smoke":
			attempt := trace.Target.Attempts[0]
			if trace.Target.FinalStatus != domain.TaskStatusFailed || trace.Target.AttemptCount != 1 ||
				attempt.Status != domain.AttemptStatusFailed || !attempt.TimedOut || attempt.Canceled ||
				attempt.ReasonCode != "AGENT_TIMEOUT" || trace.ObservedReasonCode != "AGENT_TIMEOUT" {
				return fmt.Errorf("F2 treatment does not prove deadline containment")
			}
		case "F3-service-smoke":
			if err := validateF3Attempts(trace.Target); err != nil {
				return err
			}
			if trace.ObservedReasonCode != "RUNSERVICE_GRACEFUL_RESTART" {
				return fmt.Errorf("F3 treatment has the wrong reason")
			}
		default:
			return fmt.Errorf("unsupported fault case %q", trace.CaseID)
		}
	}
	if err := validateFaultRecovery(trace); err != nil {
		return err
	}
	return validateFaultPublicationSet(trace)
}

func validateFaultSurvivor(task FaultTaskTrace) error {
	if err := validateFaultSuccessAttempt(task); err != nil {
		return fmt.Errorf("unaffected task: %w", err)
	}
	return nil
}

func validateFaultSuccessAttempt(task FaultTaskTrace) error {
	if task.FinalStatus != domain.TaskStatusSucceeded || task.AttemptCount != 1 || len(task.Attempts) != 1 {
		return fmt.Errorf("task did not succeed exactly once")
	}
	attempt := task.Attempts[0]
	if attempt.Status != domain.AttemptStatusSucceeded || attempt.ExitCode == nil || *attempt.ExitCode != 0 ||
		attempt.TimedOut || attempt.Canceled || attempt.ReasonCode != "NONE" {
		return fmt.Errorf("successful attempt has inconsistent process evidence")
	}
	return nil
}

func validateF3Attempts(task FaultTaskTrace) error {
	if task.FinalStatus != domain.TaskStatusSucceeded || task.MaxAttempts != 2 ||
		task.AttemptCount != 2 || len(task.Attempts) != 2 {
		return fmt.Errorf("F3 target did not complete on its second attempt")
	}
	first, second := task.Attempts[0], task.Attempts[1]
	if first.Status != domain.AttemptStatusLost || first.TimedOut || !first.Canceled ||
		first.ReasonCode != "RUNTIME_INTERRUPTED" || second.Status != domain.AttemptStatusSucceeded ||
		second.ExitCode == nil || *second.ExitCode != 0 || second.TimedOut || second.Canceled ||
		second.ReasonCode != "NONE" {
		return fmt.Errorf("F3 attempt transition is not LOST then SUCCEEDED")
	}
	return nil
}

func validateFaultRecovery(trace FaultTrialTrace) error {
	recovery := trace.Recovery
	if recovery.RecoveryPerformedOnOpen {
		return fmt.Errorf("fault smoke must not claim recovery performed while opening the checkpoint")
	}
	if trace.CaseID != "F3-service-smoke" {
		if recovery.CheckpointBackend != "none" || recovery.CheckpointLoaded || recovery.RuntimeReopened ||
			recovery.Requeued || recovery.RetryCount != 0 {
			return fmt.Errorf("terminal containment case claims restart recovery")
		}
		return nil
	}
	if recovery.CheckpointBackend != "statefile-v1" {
		return fmt.Errorf("F3 must use the statefile checkpoint backend")
	}
	if trace.Role == "baseline" {
		if recovery.CheckpointLoaded || recovery.RuntimeReopened || recovery.Requeued || recovery.RetryCount != 0 {
			return fmt.Errorf("F3 baseline claims restart recovery")
		}
		return nil
	}
	if !recovery.CheckpointLoaded || !recovery.RuntimeReopened || !recovery.Requeued || recovery.RetryCount != 1 {
		return fmt.Errorf("F3 treatment recovery evidence is incomplete")
	}
	return nil
}

func validateFaultPublicationSet(trace FaultTrialTrace) error {
	want := map[string]bool{trace.UnaffectedTasks[0].TaskID: true}
	if trace.Role == "baseline" || trace.CaseID == "F3-service-smoke" {
		want[trace.Target.TaskID] = true
	}
	if len(trace.Publications) != len(want) || trace.DuplicateArtifacts != 0 {
		return fmt.Errorf("fault publication count does not match successful attempts")
	}
	for _, publication := range trace.Publications {
		if !want[publication.TaskID] || publication.ArtifactKey != "result" || publication.ObservedCount != 1 {
			return fmt.Errorf("fault publication set is inconsistent")
		}
		delete(want, publication.TaskID)
	}
	if len(want) != 0 {
		return fmt.Errorf("fault publication set is incomplete")
	}
	wantProcesses := 2
	if trace.CaseID == "F3-service-smoke" && trace.Role == "treatment" {
		wantProcesses = 3
	}
	if trace.Cleanup.ChildProcessesStarted != wantProcesses {
		return fmt.Errorf("fault child process count is inconsistent")
	}
	return nil
}

func validateFaultEvidencePairs(rows []FaultRow, traces []FaultTrialTrace) error {
	if len(rows) == 0 || len(rows) != len(traces) {
		return fmt.Errorf("fault rows and traces must be non-empty and one-to-one")
	}
	pairs := make(map[string][]FaultTrialTrace)
	seenTrials := make(map[string]struct{}, len(traces))
	runID, workloadHash, inputHash := traces[0].RunID, traces[0].WorkloadSHA256, traces[0].InputSHA256
	for index, trace := range traces {
		if trace.RunID != runID || trace.WorkloadSHA256 != workloadHash || trace.InputSHA256 != inputHash {
			return fmt.Errorf("fault trace %d changed run or canonical input identity", index)
		}
		derived, err := faultRowFromTrace(trace)
		if err != nil {
			return fmt.Errorf("fault trace %d: %w", index, err)
		}
		if !faultRowsEqual(derived, rows[index]) {
			return fmt.Errorf("fault row %d cannot be reproduced from its trace", index)
		}
		key := trace.PairID + "/" + trace.Treatment
		if _, exists := seenTrials[key]; exists {
			return fmt.Errorf("duplicate fault trial %q", key)
		}
		seenTrials[key] = struct{}{}
		pairs[trace.PairID] = append(pairs[trace.PairID], trace)
	}
	for pairID, pair := range pairs {
		baseline, treatment, err := orderedFaultPair(pair)
		if err != nil {
			return fmt.Errorf("fault pair %q: %w", pairID, err)
		}
		if baseline.RunID != treatment.RunID || baseline.CaseID != treatment.CaseID || baseline.Seed != treatment.Seed ||
			baseline.Repetition != treatment.Repetition || baseline.WorkloadSHA256 != treatment.WorkloadSHA256 ||
			baseline.InputSHA256 != treatment.InputSHA256 || baseline.TimeoutMS != treatment.TimeoutMS ||
			baseline.Target.TaskID != treatment.Target.TaskID ||
			baseline.UnaffectedTasks[0].TaskID != treatment.UnaffectedTasks[0].TaskID {
			return fmt.Errorf("fault pair %q changed a controlled input", pairID)
		}
	}
	return nil
}

func orderedFaultPair(pair []FaultTrialTrace) (FaultTrialTrace, FaultTrialTrace, error) {
	if len(pair) != 2 {
		return FaultTrialTrace{}, FaultTrialTrace{}, fmt.Errorf("pair must contain two trials")
	}
	var baseline, treatment *FaultTrialTrace
	orders := map[int]int{}
	for index := range pair {
		orders[pair[index].TrialOrder]++
		switch pair[index].Role {
		case "baseline":
			baseline = &pair[index]
		case "treatment":
			treatment = &pair[index]
		default:
			return FaultTrialTrace{}, FaultTrialTrace{}, fmt.Errorf("invalid role %q", pair[index].Role)
		}
	}
	if baseline == nil || treatment == nil || orders[0] != 1 || orders[1] != 1 || len(orders) != 2 {
		return FaultTrialTrace{}, FaultTrialTrace{}, fmt.Errorf("invalid roles or trial order")
	}
	return *baseline, *treatment, nil
}
