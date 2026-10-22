package benchmark

import (
	"encoding/csv"
	"io"
	"strconv"
	"strings"
)

const faultCSVHeader = "schema_version,run_id,status,evidence_scope,case_id,pair_id,seed,repetition,trial_order,treatment,role,workload_sha256,input_sha256,fault_mode,injection_boundary,isolation_mode,expected_recovery,clock_mode,attempt_before,attempt_after,detection_ms,requeue_ms,recovery_ms,retries,injection_observed,detection_observed,recovery_observed,detection_censored,recovery_censored,cleanup_observed,unaffected_tasks_total,unaffected_tasks_succeeded,duplicate_artifacts,orphan_processes,orphan_cgroups,mutations_after_rejection,target_final_status,trial_elapsed_ms,timeout_ms,observed_reason_code,reason_code"

func writeFaultCSV(writer io.Writer, rows []FaultRow) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(strings.Split(faultCSVHeader, ",")); err != nil {
		return err
	}
	for _, row := range rows {
		if err := csvWriter.Write(row.faultCSVRecord()); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func (row FaultRow) faultCSVRecord() []string {
	return []string{
		row.SchemaVersion, row.RunID, row.Status, row.EvidenceScope, row.CaseID, row.PairID,
		strconv.FormatUint(uint64(row.Seed), 10), strconv.Itoa(row.Repetition), strconv.Itoa(row.TrialOrder),
		row.Treatment, row.Role, row.WorkloadSHA256, row.InputSHA256, row.FaultMode,
		row.InjectionBoundary, row.IsolationMode, row.ExpectedRecovery, row.ClockMode,
		strconv.Itoa(row.AttemptBefore), strconv.Itoa(row.AttemptAfter), nullableFloat(row.DetectionMS),
		nullableFloat(row.RequeueMS), nullableFloat(row.RecoveryMS), strconv.Itoa(row.Retries),
		strconv.FormatBool(row.InjectionObserved), strconv.FormatBool(row.DetectionObserved),
		strconv.FormatBool(row.RecoveryObserved), strconv.FormatBool(row.DetectionCensored),
		strconv.FormatBool(row.RecoveryCensored), strconv.FormatBool(row.CleanupObserved),
		strconv.Itoa(row.UnaffectedTasksTotal), strconv.Itoa(row.UnaffectedTasksSucceeded),
		strconv.Itoa(row.DuplicateArtifacts), nullableInt(row.OrphanProcesses), nullableInt(row.OrphanCgroups),
		nullableInt(row.MutationsAfterRejection), row.TargetFinalStatus,
		strconv.FormatFloat(row.TrialElapsedMS, 'f', 6, 64), strconv.Itoa(row.TimeoutMS),
		row.ObservedReasonCode, row.ReasonCode,
	}
}

func nullableFloat(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'f', 6, 64)
}

func nullableInt(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}
