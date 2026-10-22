package benchmark

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const faultKernelCSVHeader = "schema_version,run_id,status,evidence_scope,case_id,pair_id,seed,repetition,trial_order,treatment,role,analysis_eligible,workload_sha256,input_sha256,clock_mode,fault_mode,injection_boundary,isolation_mode,daemon_control,attempt_before,attempt_after,daemon_exit_ms,cleanup_ms,retry_start_ms,recovery_ms,retries,daemon_sigkill_observed,orphan_observed_before_restart,checkpoint_loaded,recovery_requeued,restart_ready,target_final_status,survivor_final_status,old_processes_after_cleanup,old_cgroups_after_cleanup,isolation_escape_count,duplicate_artifacts,elapsed_ms,timeout_ms,reason_code"

func writeFaultKernelCSV(writer io.Writer, traces []FaultKernelTrialTrace) error {
	if len(traces) == 0 {
		return fmt.Errorf("fault-kernel measurement traces are empty")
	}
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(strings.Split(faultKernelCSVHeader, ",")); err != nil {
		return err
	}
	invalidPairs := make(map[string]bool)
	for _, trace := range traces {
		if trace.Status == "INVALID" {
			invalidPairs[trace.PairID] = true
		}
	}
	for index, trace := range traces {
		if trace.Phase != "measurement" {
			return fmt.Errorf("fault-kernel CSV trace %d is not a measurement", index)
		}
		record, err := faultKernelCSVRecord(trace, !invalidPairs[trace.PairID])
		if err != nil {
			return fmt.Errorf("fault-kernel CSV trace %d: %w", index, err)
		}
		if err := csvWriter.Write(record); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func faultKernelCSVRecord(trace FaultKernelTrialTrace, analysisEligible bool) ([]string, error) {
	metrics, err := RecomputeFaultKernelMetrics(trace)
	if err != nil {
		return nil, err
	}
	if err := ValidateFaultKernelMetrics(trace, metrics); err != nil {
		return nil, err
	}
	float := func(value float64) string { return strconv.FormatFloat(value, 'f', 6, 64) }
	optionalFloat := func(value *float64) string {
		if value == nil {
			return ""
		}
		return float(*value)
	}
	integer := func(value int) string { return strconv.Itoa(value) }
	boolean := func(value bool) string { return strconv.FormatBool(value) }
	optionalInteger := func(observed bool, value int) string {
		if !observed {
			return ""
		}
		return integer(value)
	}
	duplicateArtifacts := ""
	if trace.Observations.DuplicateArtifactsObserved {
		duplicateArtifacts = integer(trace.Observations.DuplicateArtifacts)
	}
	return []string{
		FaultKernelCSVSchemaV1, trace.RunID, trace.Status, trace.EvidenceScope, trace.CaseID, trace.PairID,
		strconv.FormatUint(uint64(trace.Seed), 10), integer(trace.Repetition), integer(trace.TrialOrder),
		trace.Treatment, trace.Role, boolean(analysisEligible), trace.WorkloadSHA256, trace.InputSHA256, trace.ClockMode,
		trace.Protocol.FaultMode, trace.Protocol.InjectionBoundary, trace.Protocol.IsolationMode,
		trace.Protocol.DaemonControl, integer(trace.Target.AttemptBefore), integer(trace.Target.AttemptAfter),
		optionalFloat(metrics.DaemonExitMS), optionalFloat(metrics.CleanupMS), optionalFloat(metrics.RetryStartMS),
		optionalFloat(metrics.RecoveryMS), integer(metrics.Retries), boolean(trace.Observations.DaemonSIGKILLObserved),
		boolean(trace.Observations.OrphanObservedBeforeRestart), boolean(trace.Observations.CheckpointLoaded),
		boolean(trace.Observations.RecoveryRequeued), boolean(trace.Observations.RestartReady),
		trace.Target.FinalStatus, trace.Survivor.FinalStatus,
		optionalInteger(trace.Observations.OldProcessesAfterCleanupObserved, trace.Observations.OldProcessesAfterCleanup),
		optionalInteger(trace.Observations.OldCgroupsAfterCleanupObserved, trace.Observations.OldCgroupsAfterCleanup),
		integer(trace.Observations.IsolationEscapeCount), duplicateArtifacts,
		float(metrics.ElapsedMS), integer(trace.TimeoutMS), trace.ReasonCode,
	}, nil
}
