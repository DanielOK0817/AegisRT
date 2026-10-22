package benchmark

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const schedulerCSVHeader = "schema_version,run_id,status,evidence_scope,case_id,pair_id,seed,repetition,trial_order,treatment,role,policy,workflow_shape,workload_sha256,workflow_count,task_count,capacity_cpu_millis,capacity_memory_bytes,capacity_tokens,capacity_tool_slots,makespan_ms,queue_p50_ms,queue_p95_ms,critical_path_ms,dependency_unlock_ms,throughput_tasks_s,jain_fairness,admission_rejections,starvation_count,decision_count,timeout_ms,reason_code"

func writeSchedulerCSV(writer io.Writer, rows []SchedulerRow) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(strings.Split(schedulerCSVHeader, ",")); err != nil {
		return err
	}
	for _, row := range rows {
		if err := csvWriter.Write(row.csvRecord()); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func (row SchedulerRow) csvRecord() []string {
	integer := func(value int64) string { return strconv.FormatInt(value, 10) }
	decimal := func(value float64) string { return strconv.FormatFloat(value, 'f', 6, 64) }
	return []string{
		row.SchemaVersion, row.RunID, row.Status, row.EvidenceScope, row.CaseID, row.PairID,
		strconv.FormatUint(uint64(row.Seed), 10), strconv.Itoa(row.Repetition), strconv.Itoa(row.TrialOrder),
		row.Treatment, row.Role, row.Policy, row.WorkflowShape, row.WorkloadSHA256,
		strconv.Itoa(row.WorkflowCount), strconv.Itoa(row.TaskCount), integer(row.CapacityCPUMillis),
		integer(row.CapacityMemoryBytes), integer(row.CapacityTokens), integer(row.CapacityToolSlots),
		decimal(row.MakespanMS), decimal(row.QueueP50MS), decimal(row.QueueP95MS), decimal(row.CriticalPathMS),
		decimal(row.DependencyUnlockMS), decimal(row.ThroughputTasksS), decimal(row.JainFairness),
		strconv.Itoa(row.AdmissionRejections), strconv.Itoa(row.StarvationCount), strconv.Itoa(row.DecisionCount),
		strconv.Itoa(row.TimeoutMS), row.ReasonCode,
	}
}

func validateSchedulerRows(rows []SchedulerRow) error {
	wantColumns := len(strings.Split(schedulerCSVHeader, ","))
	if len(rows) == 0 {
		return fmt.Errorf("scheduler rows are empty")
	}
	pairs := make(map[string][]SchedulerRow)
	runID, workloadHash := rows[0].RunID, rows[0].WorkloadSHA256
	for index, row := range rows {
		if len(row.csvRecord()) != wantColumns {
			return fmt.Errorf("scheduler row %d has invalid column count", index)
		}
		if !validLabel(row.RunID, true) || row.RunID != runID || row.Status != "SUCCEEDED" || row.EvidenceScope != "windows-functional" || row.ReasonCode != "NONE" {
			return fmt.Errorf("scheduler row %d has invalid evidence identity", index)
		}
		if row.SchemaVersion != schedulerSchemaV1 || !validSchedulerRowIdentity(row) || !validLabel(row.PairID, true) {
			return fmt.Errorf("scheduler row %d has invalid experiment identity", index)
		}
		if len(row.WorkloadSHA256) != 64 || !isLowerHex(row.WorkloadSHA256) || row.WorkloadSHA256 != workloadHash {
			return fmt.Errorf("scheduler row %d has invalid workload hash", index)
		}
		if row.TrialOrder < 0 || row.TrialOrder > 1 || row.Repetition < 0 || row.TaskCount <= 0 || row.DecisionCount != row.TaskCount {
			return fmt.Errorf("scheduler row %d has invalid trial counts", index)
		}
		if row.CapacityCPUMillis <= 0 || row.CapacityMemoryBytes <= 0 || row.CapacityTokens <= 0 || row.CapacityToolSlots <= 0 || row.TimeoutMS <= 0 {
			return fmt.Errorf("scheduler row %d has invalid capacity or timeout", index)
		}
		if row.AdmissionRejections < 0 || row.StarvationCount < 0 || !validMetrics(row) {
			return fmt.Errorf("scheduler row %d has invalid metrics", index)
		}
		pairs[row.PairID] = append(pairs[row.PairID], row)
	}
	for pairID, pair := range pairs {
		if err := validatePair(pairID, pair); err != nil {
			return err
		}
	}
	return nil
}

func validMetrics(row SchedulerRow) bool {
	values := []float64{row.MakespanMS, row.QueueP50MS, row.QueueP95MS, row.CriticalPathMS, row.DependencyUnlockMS, row.ThroughputTasksS, row.JainFairness}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return row.MakespanMS > 0 && row.ThroughputTasksS > 0 && row.JainFairness <= 1 &&
		row.QueueP50MS <= row.QueueP95MS && row.QueueP95MS <= row.MakespanMS && row.DependencyUnlockMS <= row.MakespanMS
}

func validatePair(pairID string, pair []SchedulerRow) error {
	if pairID == "" || len(pair) != 2 {
		return fmt.Errorf("pair %q must contain exactly two rows", pairID)
	}
	roles := map[string]int{}
	orders := map[int]int{}
	for _, row := range pair {
		roles[row.Role]++
		orders[row.TrialOrder]++
	}
	if roles["baseline"] != 1 || roles["treatment"] != 1 || len(roles) != 2 || orders[0] != 1 || orders[1] != 1 || len(orders) != 2 {
		return fmt.Errorf("pair %q has invalid roles or order", pairID)
	}
	baseline := pair[0]
	if baseline.Role != "baseline" {
		baseline = pair[1]
	}
	treatment := pair[0]
	if treatment.Role != "treatment" {
		treatment = pair[1]
	}
	if baseline.Policy != baselinePolicy(baseline.CaseID) || treatment.Policy != treatment.Treatment ||
		baseline.Treatment != treatment.Treatment || treatment.Policy != "priority" && treatment.Policy != "dag-drcfs" {
		return fmt.Errorf("pair %q has invalid policies", pairID)
	}
	if baseline.RunID != treatment.RunID || baseline.CaseID != treatment.CaseID || baseline.Seed != treatment.Seed ||
		baseline.Repetition != treatment.Repetition || baseline.WorkloadSHA256 != treatment.WorkloadSHA256 ||
		baseline.EvidenceScope != treatment.EvidenceScope || baseline.WorkflowShape != treatment.WorkflowShape ||
		baseline.WorkflowCount != treatment.WorkflowCount || baseline.TaskCount != treatment.TaskCount ||
		baseline.CapacityCPUMillis != treatment.CapacityCPUMillis || baseline.CapacityMemoryBytes != treatment.CapacityMemoryBytes ||
		baseline.CapacityTokens != treatment.CapacityTokens || baseline.CapacityToolSlots != treatment.CapacityToolSlots || baseline.TimeoutMS != treatment.TimeoutMS {
		return fmt.Errorf("pair %q changed a controlled input", pairID)
	}
	return nil
}

func validSchedulerRowIdentity(row SchedulerRow) bool {
	switch row.CaseID {
	case "S1-smoke":
		return row.WorkflowShape == s1SmokeShape && row.WorkflowCount == 1
	case "S1":
		return row.WorkflowShape == formalS1Shape && row.WorkflowCount == 1
	case "S2":
		return row.WorkflowShape == formalS2Shape && row.WorkflowCount == 3 && row.Treatment == "dag-drcfs"
	case "S3":
		return row.WorkflowShape == formalS3Shape && row.WorkflowCount == 2 && row.Treatment == "dag-drcfs"
	default:
		return false
	}
}

func validLabel(value string, lowerOnly bool) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, char := range value {
		letter := char >= 'a' && char <= 'z' || !lowerOnly && char >= 'A' && char <= 'Z'
		digit := char >= '0' && char <= '9'
		if !(letter || digit || index > 0 && (char == '.' || char == '_' || char == '-')) {
			return false
		}
	}
	return true
}
