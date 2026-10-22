package benchmark

import (
	"context"

	"aegisrt/internal/domain"
)

type FaultPlan struct {
	Seeds       []uint32 `json:"seeds"`
	Repetitions int      `json:"repetitions"`
}

type FaultAgentCommand struct {
	Entrypoint string
	ArgsPrefix []string
}

type FaultCollectOptions struct {
	OutputDir string
	Plan      FaultPlan
	Agent     FaultAgentCommand
}

type FaultResult struct {
	Rows           []FaultRow
	WorkloadSHA256 string
	InputSHA256    string
	traces         []trialTrace
}

type FaultEvidence struct {
	Result       FaultResult
	workloadJSON []byte
	inputJSON    []byte
	trialTraces  []FaultTrialTrace
}

func (e FaultEvidence) CanonicalWorkloadJSON() []byte {
	return append([]byte(nil), e.workloadJSON...)
}

func (e FaultEvidence) CanonicalInputJSON() []byte {
	return append([]byte(nil), e.inputJSON...)
}

func (e FaultEvidence) TrialTraceRecords() []FaultTrialTrace {
	result := make([]FaultTrialTrace, len(e.trialTraces))
	for index, trace := range e.trialTraces {
		result[index] = cloneFaultTrialTrace(trace)
	}
	return result
}

type FaultRow struct {
	SchemaVersion            string
	RunID                    string
	Status                   string
	EvidenceScope            string
	CaseID                   string
	PairID                   string
	Seed                     uint32
	Repetition               int
	TrialOrder               int
	Treatment                string
	Role                     string
	WorkloadSHA256           string
	InputSHA256              string
	FaultMode                string
	InjectionBoundary        string
	IsolationMode            string
	ExpectedRecovery         string
	ClockMode                string
	AttemptBefore            int
	AttemptAfter             int
	DetectionMS              *float64
	RequeueMS                *float64
	RecoveryMS               *float64
	Retries                  int
	InjectionObserved        bool
	DetectionObserved        bool
	RecoveryObserved         bool
	DetectionCensored        bool
	RecoveryCensored         bool
	CleanupObserved          bool
	UnaffectedTasksTotal     int
	UnaffectedTasksSucceeded int
	DuplicateArtifacts       int
	OrphanProcesses          *int
	OrphanCgroups            *int
	MutationsAfterRejection  *int
	TargetFinalStatus        string
	TrialElapsedMS           float64
	TimeoutMS                int
	ObservedReasonCode       string
	ReasonCode               string
}

type FaultTrialTrace struct {
	SchemaVersion      string                  `json:"schema_version"`
	RunID              string                  `json:"run_id"`
	EvidenceScope      string                  `json:"evidence_scope"`
	PlanID             string                  `json:"plan_id"`
	CaseID             string                  `json:"case_id"`
	PairID             string                  `json:"pair_id"`
	Seed               uint32                  `json:"seed"`
	Repetition         int                     `json:"repetition"`
	TrialOrder         int                     `json:"trial_order"`
	Treatment          string                  `json:"treatment"`
	Role               string                  `json:"role"`
	WorkloadSHA256     string                  `json:"workload_sha256"`
	InputSHA256        string                  `json:"input_sha256"`
	Status             string                  `json:"status"`
	TimeoutMS          int                     `json:"timeout_ms"`
	ClockMode          string                  `json:"clock_mode"`
	Protocol           FaultProtocolTrace      `json:"protocol"`
	Milestones         FaultMilestonesTrace    `json:"milestones"`
	Target             FaultTaskTrace          `json:"target"`
	UnaffectedTasks    []FaultTaskTrace        `json:"unaffected_tasks"`
	Publications       []FaultPublicationTrace `json:"publications"`
	DuplicateArtifacts int                     `json:"duplicate_artifacts"`
	Cleanup            FaultCleanupTrace       `json:"cleanup"`
	Recovery           FaultRecoveryTrace      `json:"recovery"`
	ObservedReasonCode string                  `json:"observed_reason_code"`
	ReasonCode         string                  `json:"reason_code"`
}

type FaultProtocolTrace struct {
	FaultMode         string `json:"fault_mode"`
	InjectionBoundary string `json:"injection_boundary"`
	IsolationMode     string `json:"isolation_mode"`
	ExpectedRecovery  string `json:"expected_recovery"`
}

type FaultMilestonesTrace struct {
	AgentReadyNS *int64 `json:"agent_ready_ns"`
	InjectedNS   *int64 `json:"injected_ns"`
	DetectedNS   *int64 `json:"detected_ns"`
	RequeuedNS   *int64 `json:"requeued_ns"`
	ResumedNS    *int64 `json:"resumed_ns"`
	RecoveredNS  *int64 `json:"recovered_ns"`
	CleanupNS    *int64 `json:"cleanup_ns"`
	FinishedNS   int64  `json:"finished_ns"`
}

type FaultTaskTrace struct {
	TaskID       string              `json:"task_id"`
	MaxAttempts  int                 `json:"max_attempts"`
	FinalStatus  domain.TaskStatus   `json:"final_status"`
	AttemptCount int                 `json:"attempt_count"`
	Attempts     []FaultAttemptTrace `json:"attempts"`
}

type FaultAttemptTrace struct {
	Number               int                  `json:"number"`
	Status               domain.AttemptStatus `json:"status"`
	ExitCode             *int                 `json:"exit_code"`
	TimedOut             bool                 `json:"timed_out"`
	Canceled             bool                 `json:"canceled"`
	ProcessWaitCompleted bool                 `json:"process_wait_completed"`
	RunElapsedNS         int64                `json:"run_elapsed_ns"`
	ReasonCode           string               `json:"reason_code"`
}

type FaultPublicationTrace struct {
	TaskID        string `json:"task_id"`
	ArtifactKey   string `json:"artifact_key"`
	ContentSHA256 string `json:"content_sha256"`
	ObservedCount int    `json:"observed_count"`
}

type FaultCleanupTrace struct {
	CleanupObserved       bool `json:"cleanup_observed"`
	ResourcesReleased     bool `json:"resources_released"`
	ChildProcessesStarted int  `json:"child_processes_started"`
	ChildProcessesReaped  int  `json:"child_processes_reaped"`
	OrphanProcesses       *int `json:"orphan_processes"`
	OrphanCgroups         *int `json:"orphan_cgroups"`
}

type FaultRecoveryTrace struct {
	CheckpointBackend       string `json:"checkpoint_backend"`
	CheckpointLoaded        bool   `json:"checkpoint_loaded"`
	RecoveryPerformedOnOpen bool   `json:"recovery_performed_on_open"`
	RuntimeReopened         bool   `json:"runtime_reopened"`
	Requeued                bool   `json:"requeued"`
	RetryCount              int    `json:"retry_count"`
}

type faultTrialMode struct {
	treatment string
	role      string
	faultMode string
}

type faultRunner func(context.Context, faultCaseWorkload, faultTrialMode, FaultAgentCommand) (FaultTrialTrace, error)

func cloneFaultTrialTrace(trace FaultTrialTrace) FaultTrialTrace {
	trace.Target.Attempts = append([]FaultAttemptTrace(nil), trace.Target.Attempts...)
	trace.UnaffectedTasks = append([]FaultTaskTrace(nil), trace.UnaffectedTasks...)
	for index := range trace.UnaffectedTasks {
		trace.UnaffectedTasks[index].Attempts = append([]FaultAttemptTrace(nil), trace.UnaffectedTasks[index].Attempts...)
	}
	trace.Publications = append([]FaultPublicationTrace(nil), trace.Publications...)
	return trace
}
