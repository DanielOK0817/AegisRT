package benchmark

import (
	"fmt"
	"math"
	"reflect"

	"aegisrt/internal/domain"
)

func faultRowFromTrace(trace FaultTrialTrace) (FaultRow, error) {
	row := FaultRow{
		SchemaVersion: faultSchemaV1, RunID: trace.RunID, Status: trace.Status,
		EvidenceScope: trace.EvidenceScope, CaseID: trace.CaseID, PairID: trace.PairID,
		Seed: trace.Seed, Repetition: trace.Repetition, TrialOrder: trace.TrialOrder,
		Treatment: trace.Treatment, Role: trace.Role, WorkloadSHA256: trace.WorkloadSHA256,
		InputSHA256: trace.InputSHA256, FaultMode: trace.Protocol.FaultMode,
		InjectionBoundary: trace.Protocol.InjectionBoundary, IsolationMode: trace.Protocol.IsolationMode,
		ExpectedRecovery: trace.Protocol.ExpectedRecovery, ClockMode: trace.ClockMode,
		AttemptAfter: trace.Target.AttemptCount, DuplicateArtifacts: trace.DuplicateArtifacts,
		CleanupObserved: trace.Cleanup.CleanupObserved, OrphanProcesses: cloneIntPointer(trace.Cleanup.OrphanProcesses),
		OrphanCgroups: cloneIntPointer(trace.Cleanup.OrphanCgroups), TargetFinalStatus: string(trace.Target.FinalStatus),
		TrialElapsedMS: float64(trace.Milestones.FinishedNS) / 1e6, TimeoutMS: trace.TimeoutMS,
		ObservedReasonCode: trace.ObservedReasonCode, ReasonCode: trace.ReasonCode,
	}
	if len(trace.Target.Attempts) > 0 {
		row.AttemptBefore = trace.Target.Attempts[0].Number
	}
	row.Retries = row.AttemptAfter - row.AttemptBefore
	row.InjectionObserved = trace.Milestones.InjectedNS != nil
	row.DetectionObserved = trace.Milestones.DetectedNS != nil
	row.RecoveryObserved = trace.Milestones.RecoveredNS != nil
	row.DetectionCensored = row.InjectionObserved && !row.DetectionObserved
	row.RecoveryCensored = row.InjectionObserved && !row.RecoveryObserved
	row.DetectionMS = milestoneDeltaMS(trace.Milestones.InjectedNS, trace.Milestones.DetectedNS)
	row.RequeueMS = milestoneDeltaMS(trace.Milestones.InjectedNS, trace.Milestones.RequeuedNS)
	row.RecoveryMS = milestoneDeltaMS(trace.Milestones.InjectedNS, trace.Milestones.RecoveredNS)
	row.UnaffectedTasksTotal = len(trace.UnaffectedTasks)
	for _, task := range trace.UnaffectedTasks {
		if task.FinalStatus == domain.TaskStatusSucceeded {
			row.UnaffectedTasksSucceeded++
		}
	}
	if err := validateFaultTraceAndRow(trace, row); err != nil {
		return FaultRow{}, err
	}
	return row, nil
}

func milestoneDeltaMS(start, end *int64) *float64 {
	if start == nil || end == nil {
		return nil
	}
	value := float64(*end-*start) / 1e6
	return &value
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	return intPointer(*value)
}

func validateFaultTraceAndRow(trace FaultTrialTrace, row FaultRow) error {
	if trace.SchemaVersion != faultTrialTraceSchemaV1 || trace.EvidenceScope != "windows-functional" ||
		trace.PlanID != faultPlanID || trace.Status != "SUCCEEDED" || trace.ClockMode != faultClockMode ||
		trace.ReasonCode != "NONE" || trace.TimeoutMS != trialTimeoutMS {
		return fmt.Errorf("fault trace has unsupported identity")
	}
	if trace.RunID != "" {
		if err := validateRunID(trace.RunID); err != nil {
			return err
		}
	}
	if !validLabel(trace.PairID, true) || trace.Repetition < 0 || trace.TrialOrder < 0 || trace.TrialOrder > 1 ||
		len(trace.WorkloadSHA256) != 64 || !isLowerHex(trace.WorkloadSHA256) ||
		len(trace.InputSHA256) != 64 || !isLowerHex(trace.InputSHA256) {
		return fmt.Errorf("fault trace has invalid protocol fields")
	}
	if err := validateFaultMode(trace); err != nil {
		return err
	}
	if err := validateFaultMilestones(trace); err != nil {
		return err
	}
	if err := validateFaultTask(trace.Target); err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if len(trace.UnaffectedTasks) != 1 {
		return fmt.Errorf("fault trace must contain one unaffected task")
	}
	if err := validateFaultTask(trace.UnaffectedTasks[0]); err != nil {
		return fmt.Errorf("unaffected task: %w", err)
	}
	if err := validateFaultCleanupAndPublications(trace); err != nil {
		return err
	}
	if err := validateFaultCaseSemantics(trace); err != nil {
		return err
	}
	if err := validateFaultRowIdentity(trace, row); err != nil {
		return err
	}
	return nil
}

func validateFaultMode(trace FaultTrialTrace) error {
	want, ok := map[string]struct {
		caseID, treatment, role, boundary, recovery string
	}{
		"none/F1-smoke":                                {"F1-smoke", "no-injection", "baseline", "matched-dispatch-control", "normal-completion"},
		"agent_exit/F1-smoke":                          {"F1-smoke", "agent-exit", "treatment", "agent-process-start", "contained-terminal-failure"},
		"none/F2-smoke":                                {"F2-smoke", "no-injection", "baseline", "matched-dispatch-control", "normal-completion"},
		"agent_timeout/F2-smoke":                       {"F2-smoke", "agent-timeout", "treatment", "configured-agent-deadline", "contained-terminal-failure"},
		"none/F3-service-smoke":                        {"F3-service-smoke", "no-injection", "baseline", "matched-service-control", "normal-completion"},
		"runservice_graceful_restart/F3-service-smoke": {"F3-service-smoke", "runservice-graceful-restart", "treatment", "active-attempt-root-cancel", "requeue-then-complete"},
	}[trace.Protocol.FaultMode+"/"+trace.CaseID]
	if !ok || trace.CaseID != want.caseID || trace.Treatment != want.treatment || trace.Role != want.role ||
		trace.Protocol.InjectionBoundary != want.boundary || trace.Protocol.ExpectedRecovery != want.recovery ||
		trace.Protocol.IsolationMode != "tracked-child-process" {
		return fmt.Errorf("fault trace mode does not match its case")
	}
	return nil
}

func validateFaultMilestones(trace FaultTrialTrace) error {
	m := trace.Milestones
	if m.FinishedNS <= 0 || m.CleanupNS == nil || *m.CleanupNS != m.FinishedNS {
		return fmt.Errorf("fault trace has invalid finish or cleanup milestone")
	}
	for _, value := range []*int64{m.AgentReadyNS, m.InjectedNS, m.DetectedNS, m.RequeuedNS, m.ResumedNS, m.RecoveredNS, m.CleanupNS} {
		if value != nil && (*value < 0 || *value > m.FinishedNS) {
			return fmt.Errorf("fault trace milestone is outside the trial")
		}
	}
	if trace.Role == "baseline" {
		if m.AgentReadyNS != nil || m.InjectedNS != nil || m.DetectedNS != nil || m.RequeuedNS != nil || m.ResumedNS != nil || m.RecoveredNS != nil {
			return fmt.Errorf("baseline fault trace contains injection milestones")
		}
		return nil
	}
	if m.InjectedNS == nil || m.DetectedNS == nil || m.RecoveredNS == nil ||
		*m.DetectedNS < *m.InjectedNS || *m.RecoveredNS < *m.DetectedNS {
		return fmt.Errorf("fault treatment is missing ordered detection/recovery milestones")
	}
	if trace.CaseID == "F3-service-smoke" {
		if m.AgentReadyNS == nil || *m.AgentReadyNS > *m.InjectedNS || m.RequeuedNS == nil || m.ResumedNS == nil ||
			*m.RequeuedNS < *m.DetectedNS || *m.ResumedNS <= *m.RequeuedNS || *m.RecoveredNS <= *m.ResumedNS {
			return fmt.Errorf("F3 recovery milestones are incomplete or unordered")
		}
	} else if m.AgentReadyNS != nil || m.RequeuedNS != nil || m.ResumedNS != nil {
		return fmt.Errorf("terminal containment case must not claim requeue or resume")
	}
	return nil
}

func validateFaultTask(task FaultTaskTrace) error {
	if !validLabel(task.TaskID, true) || task.MaxAttempts <= 0 || task.AttemptCount <= 0 ||
		task.AttemptCount != len(task.Attempts) || task.AttemptCount > task.MaxAttempts {
		return fmt.Errorf("invalid task identity or attempt count")
	}
	for index, attempt := range task.Attempts {
		if attempt.Number != index+1 || !attempt.ProcessWaitCompleted || attempt.RunElapsedNS < 0 ||
			attempt.ReasonCode == "" || attempt.TimedOut && attempt.Canceled {
			return fmt.Errorf("invalid attempt %d", index+1)
		}
		if attempt.Status != domain.AttemptStatusSucceeded && attempt.Status != domain.AttemptStatusFailed &&
			attempt.Status != domain.AttemptStatusLost {
			return fmt.Errorf("unsupported terminal attempt status %q", attempt.Status)
		}
	}
	return nil
}

func validateFaultCleanupAndPublications(trace FaultTrialTrace) error {
	cleanup := trace.Cleanup
	if !cleanup.CleanupObserved || !cleanup.ResourcesReleased || cleanup.ChildProcessesStarted <= 0 ||
		cleanup.ChildProcessesReaped != cleanup.ChildProcessesStarted || cleanup.OrphanProcesses == nil ||
		*cleanup.OrphanProcesses != 0 || cleanup.OrphanCgroups != nil {
		return fmt.Errorf("fault cleanup evidence is incomplete")
	}
	duplicates := 0
	seen := make(map[string]struct{}, len(trace.Publications))
	for _, publication := range trace.Publications {
		key := publication.TaskID + "\x00" + publication.ArtifactKey
		if !validLabel(publication.TaskID, true) || !validLabel(publication.ArtifactKey, true) ||
			len(publication.ContentSHA256) != 64 || !isLowerHex(publication.ContentSHA256) ||
			publication.ObservedCount <= 0 {
			return fmt.Errorf("fault publication is invalid")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate publication trace key")
		}
		seen[key] = struct{}{}
		duplicates += publication.ObservedCount - 1
	}
	if trace.DuplicateArtifacts != duplicates {
		return fmt.Errorf("duplicate artifact count does not match observed callbacks")
	}
	return nil
}

func validateFaultRowIdentity(trace FaultTrialTrace, row FaultRow) error {
	derived := row
	if derived.SchemaVersion != faultSchemaV1 || derived.RunID != trace.RunID || derived.Status != trace.Status ||
		derived.EvidenceScope != trace.EvidenceScope || derived.CaseID != trace.CaseID || derived.PairID != trace.PairID ||
		derived.Seed != trace.Seed || derived.Repetition != trace.Repetition || derived.TrialOrder != trace.TrialOrder ||
		derived.Treatment != trace.Treatment || derived.Role != trace.Role || derived.WorkloadSHA256 != trace.WorkloadSHA256 ||
		derived.InputSHA256 != trace.InputSHA256 || derived.ClockMode != trace.ClockMode ||
		derived.TimeoutMS != trace.TimeoutMS || derived.ReasonCode != trace.ReasonCode ||
		math.Abs(derived.TrialElapsedMS-float64(trace.Milestones.FinishedNS)/1e6) > 1e-9 {
		return fmt.Errorf("fault row identity does not match trace")
	}
	return nil
}

func faultRowsEqual(left, right FaultRow) bool {
	return reflect.DeepEqual(left, right)
}
