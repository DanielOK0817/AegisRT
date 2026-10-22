package benchmark

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const (
	ResourceKernelProfileSmoke          = "smoke"
	ResourceKernelEvidenceScope         = "openeuler-kernel"
	ResourceKernelCSVSchemaV2           = "aegisrt.experiment.resource/v2"
	resourceKernelPlanID                = "resource-cgroup-pressure-smoke-v1"
	resourceKernelTrialTimeoutMS        = 5000
	resourceKernelSeed           uint32 = 104729
)

const resourceKernelCSVHeader = "schema_version,run_id,status,evidence_scope,case_id,pair_id,seed,repetition,trial_order,treatment,role,workload_sha256,backend,controller,limit_kind,requested_limit,observed_value,observation_statistic,unit,throttle_events,oom_events,pid_limit_events,pressure_some_avg10,pressure_full_avg10,admitted,rejected,ledger_invariant,containment_result,elapsed_ms,timeout_ms,reason_code"

var ErrResourceKernelUnsupported = errors.New("resource-kernel requires a supported Linux host")

type ResourceKernelPlan struct {
	Profile        string   `json:"profile"`
	PlanID         string   `json:"plan_id"`
	CaseIDs        []string `json:"case_ids"`
	Seeds          []uint32 `json:"seeds"`
	Repetitions    int      `json:"repetitions"`
	TrialTimeoutMS int      `json:"trial_timeout_ms"`
}

func NewResourceKernelSmokePlan() ResourceKernelPlan {
	return ResourceKernelPlan{Profile: ResourceKernelProfileSmoke, PlanID: resourceKernelPlanID,
		CaseIDs: []string{"R2", "R3-memory", "R3-pids"}, Seeds: []uint32{resourceKernelSeed},
		Repetitions: 1, TrialTimeoutMS: resourceKernelTrialTimeoutMS}
}

func ValidateResourceKernelPlan(plan ResourceKernelPlan) error {
	fixed := NewResourceKernelSmokePlan()
	if plan.Profile != fixed.Profile || plan.PlanID != fixed.PlanID || plan.Repetitions != fixed.Repetitions ||
		plan.TrialTimeoutMS != fixed.TrialTimeoutMS || !equalStrings(plan.CaseIDs, fixed.CaseIDs) || !equalUint32s(plan.Seeds, fixed.Seeds) {
		return errors.New("resource-kernel smoke plan differs from the fixed protocol")
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalUint32s(left, right []uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type ResourceKernelCollectOptions struct {
	OutputDir string
	Plan      ResourceKernelPlan
}

type ResourceKernelRow struct {
	SchemaVersion        string
	RunID                string
	Status               string
	EvidenceScope        string
	CaseID               string
	PairID               string
	Seed                 uint32
	Repetition           int
	TrialOrder           int
	Treatment            string
	Role                 string
	WorkloadSHA256       string
	Backend              string
	Controller           string
	LimitKind            string
	RequestedLimit       int64
	ObservedValue        int64
	ObservationStatistic string
	Unit                 string
	ThrottleEvents       int64
	OOMEvents            int64
	PIDLimitEvents       int64
	PressureSomeAvg10    float64
	PressureFullAvg10    float64
	Admitted             int
	Rejected             int
	LedgerInvariant      bool
	ContainmentResult    string
	ElapsedMS            float64
	TimeoutMS            int
	ReasonCode           string
}

func writeResourceKernelCSV(writer io.Writer, rows []ResourceKernelRow) error {
	if len(rows) == 0 {
		return errors.New("resource-kernel rows are empty")
	}
	output := csv.NewWriter(writer)
	if err := output.Write(splitCSVHeader(resourceKernelCSVHeader)); err != nil {
		return err
	}
	for index, row := range rows {
		if err := validateResourceKernelRow(row); err != nil {
			return fmt.Errorf("row %d: %w", index, err)
		}
		if err := output.Write(resourceKernelCSVRecord(row)); err != nil {
			return err
		}
	}
	output.Flush()
	return output.Error()
}

func splitCSVHeader(header string) []string {
	record, _ := csv.NewReader(newStringReader(header)).Read()
	return record
}

type stringReader string

func newStringReader(value string) *stringReader { reader := stringReader(value); return &reader }
func (reader *stringReader) Read(buffer []byte) (int, error) {
	if len(*reader) == 0 {
		return 0, io.EOF
	}
	count := copy(buffer, string(*reader))
	*reader = (*reader)[count:]
	return count, nil
}

func validateResourceKernelRow(row ResourceKernelRow) error {
	if row.SchemaVersion != ResourceKernelCSVSchemaV2 || row.EvidenceScope != ResourceKernelEvidenceScope ||
		row.Backend != "cgroup-v2" || row.Status == "" || row.CaseID == "" || row.PairID == "" ||
		len(row.WorkloadSHA256) != 64 || !isLowerHex(row.WorkloadSHA256) || row.TimeoutMS != resourceKernelTrialTimeoutMS {
		return errors.New("resource-kernel row identity is invalid")
	}
	if row.Role != "baseline" && row.Role != "treatment" {
		return errors.New("resource-kernel role is invalid")
	}
	if row.ObservationStatistic != "mean_over_trial" && row.ObservationStatistic != "peak" {
		return errors.New("resource-kernel observation statistic is invalid")
	}
	if row.ContainmentResult != "PASSED" && row.ContainmentResult != "FAILED" {
		return errors.New("containment result is invalid")
	}
	return nil
}

func resourceKernelCSVRecord(row ResourceKernelRow) []string {
	return []string{row.SchemaVersion, row.RunID, row.Status, row.EvidenceScope, row.CaseID, row.PairID,
		strconv.FormatUint(uint64(row.Seed), 10), strconv.Itoa(row.Repetition), strconv.Itoa(row.TrialOrder),
		row.Treatment, row.Role, row.WorkloadSHA256, row.Backend, row.Controller, row.LimitKind,
		strconv.FormatInt(row.RequestedLimit, 10), strconv.FormatInt(row.ObservedValue, 10),
		row.ObservationStatistic, row.Unit,
		strconv.FormatInt(row.ThrottleEvents, 10), strconv.FormatInt(row.OOMEvents, 10),
		strconv.FormatInt(row.PIDLimitEvents, 10), strconv.FormatFloat(row.PressureSomeAvg10, 'f', 6, 64),
		strconv.FormatFloat(row.PressureFullAvg10, 'f', 6, 64), strconv.Itoa(row.Admitted), strconv.Itoa(row.Rejected),
		strconv.FormatBool(row.LedgerInvariant), row.ContainmentResult, strconv.FormatFloat(row.ElapsedMS, 'f', 3, 64),
		strconv.Itoa(row.TimeoutMS), row.ReasonCode}
}
