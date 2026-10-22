package benchmark

import (
	"encoding/csv"
	"io"
	"strconv"
	"strings"
)

const contextCSVHeader = "schema_version,run_id,status,evidence_scope,case_id,pair_id,seed,repetition,trial_order,treatment,role,counter_name,context_mode,provider_cache_mode,workload_sha256,page_count,storage_logical_tokens,storage_physical_tokens,storage_reused_tokens,dropped_tokens,summary_pages,summarized_source_tokens,summary_tokens,dispatch_input_tokens,target_budget_exceedances,elapsed_ms,manifest_operations,manifest_conflicts,timeout_ms,reason_code"

func writeContextCSV(writer io.Writer, rows []ContextRow) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(strings.Split(contextCSVHeader, ",")); err != nil {
		return err
	}
	for _, row := range rows {
		if err := csvWriter.Write(row.contextCSVRecord()); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func (row ContextRow) contextCSVRecord() []string {
	return []string{
		row.SchemaVersion, row.RunID, row.Status, row.EvidenceScope, row.CaseID, row.PairID,
		strconv.FormatUint(uint64(row.Seed), 10), strconv.Itoa(row.Repetition), strconv.Itoa(row.TrialOrder),
		row.Treatment, row.Role, row.CounterName, row.ContextMode, row.ProviderCacheMode, row.WorkloadSHA256,
		strconv.Itoa(row.PageCount), strconv.Itoa(row.StorageLogicalTokens), strconv.Itoa(row.StoragePhysicalTokens),
		strconv.Itoa(row.StorageReusedTokens), strconv.Itoa(row.DroppedTokens), strconv.Itoa(row.SummaryPages),
		strconv.Itoa(row.SummarizedSourceTokens), strconv.Itoa(row.SummaryTokens),
		strconv.Itoa(row.DispatchInputTokens), strconv.Itoa(row.TargetBudgetExceedances),
		strconv.FormatFloat(row.ElapsedMS, 'f', 6, 64), strconv.Itoa(row.ManifestOperations),
		strconv.Itoa(row.ManifestConflicts), strconv.Itoa(row.TimeoutMS), row.ReasonCode,
	}
}
