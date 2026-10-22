package benchmark

import "fmt"

const (
	FaultKernelProfileSmoke                     = "smoke"
	FaultKernelProfileFormal                    = "formal"
	FaultKernelSmokePlanID                      = "f4-daemon-sigkill-cgroup-smoke-v1"
	FaultKernelFormalPlanID                     = "f4-daemon-sigkill-cgroup-v1"
	FaultKernelSmokeCaseID                      = "F4-kernel-smoke"
	FaultKernelFormalCaseID                     = "F4"
	FaultKernelKind                             = "fault-kernel"
	FaultKernelWorkloadVersion                  = "f4-deterministic-agent-tree-v1"
	FaultKernelFixtureProtocol                  = "aegisbench-fault-kernel-agent-v1"
	FaultKernelEvidenceScope                    = "openeuler-kernel"
	FaultKernelClockMode                        = "harness-monotonic-v1"
	FaultKernelIsolationMode                    = "cgroup-v2-clone-placement-v1"
	FaultKernelDaemonControl                    = "direct-child-process-v1"
	FaultKernelPairing                          = "seed-and-repetition-v1"
	FaultKernelCounterbalance                   = "alternating-seed-index-v1"
	FaultKernelBootstrapSeed             uint32 = 424242
	FaultKernelBootstrapResamples               = 10000
	faultKernelMinimumTrialTimeoutMS            = 30000
	FaultKernelDefaultTrialTimeoutMS            = 60000
	FaultKernelInputSchemaV1                    = "aegisrt.experiment.fault-kernel-input/v1"
	FaultKernelCSVSchemaV1                      = "aegisrt.experiment.fault-kernel/v1"
	FaultKernelTrialTraceSchemaV1               = "aegisrt.experiment.fault-kernel-trial-trace/v1"
	FaultKernelCgroupSampleSchemaV1             = "aegisrt.experiment.cgroup-sample/v1"
	FaultKernelComponentManifestSchemaV1        = "aegisrt.experiment.component-manifest/v1"
	FaultKernelComponentHarness                 = "harness"
	FaultKernelComponentRuntimeDaemon           = "runtime-daemon"
	FaultKernelComponentAgentFixture            = "agent-fixture"
)

var (
	faultKernelFormalWarmupSeeds   = []uint32{104701, 104707, 104711}
	faultKernelFormalMeasuredSeeds = []uint32{
		104729, 104743, 104759, 104761, 104773, 104779, 104789, 104801, 104803, 104827,
		104831, 104849, 104851, 104869, 104879, 104891, 104911, 104917, 104933, 104947,
		104953, 104959, 104971, 104987, 104999, 105019, 105023, 105031, 105037, 105071,
	}
	faultKernelSuccessfulStages = map[string][]string{"baseline": {"attempt1_ready", "trial_terminal"}, "treatment": {"attempt1_ready", "post_daemon_exit", "post_runtime_restart", "attempt2_ready", "trial_terminal"}}
)

func FaultKernelFormalWarmupSeeds() []uint32 {
	return append([]uint32(nil), faultKernelFormalWarmupSeeds...)
}

func FaultKernelFormalMeasuredSeeds() []uint32 {
	return append([]uint32(nil), faultKernelFormalMeasuredSeeds...)
}

func NewFaultKernelSmokePlan(seeds []uint32, repetitions, timeoutMS int) (FaultKernelPlan, error) {
	plan := newFaultKernelPlan(FaultKernelProfileSmoke, FaultKernelSmokePlanID, FaultKernelSmokeCaseID,
		nil, seeds, repetitions, timeoutMS)
	return plan, ValidateFaultKernelPlan(plan)
}

func NewFaultKernelFormalPlan(timeoutMS int) (FaultKernelPlan, error) {
	plan := newFaultKernelPlan(FaultKernelProfileFormal, FaultKernelFormalPlanID, FaultKernelFormalCaseID,
		faultKernelFormalWarmupSeeds, faultKernelFormalMeasuredSeeds, 1, timeoutMS)
	return plan, ValidateFaultKernelPlan(plan)
}

func newFaultKernelPlan(profile, planID, caseID string, warmup, measured []uint32, repetitions, timeoutMS int) FaultKernelPlan {
	return FaultKernelPlan{Profile: profile, PlanID: planID, CaseID: caseID, Protocol: FaultKernelPlanProtocol{
		WarmupSeeds: append([]uint32(nil), warmup...), MeasuredSeeds: append([]uint32(nil), measured...),
		Repetitions: repetitions, Pairing: FaultKernelPairing, Counterbalance: FaultKernelCounterbalance,
		TrialTimeoutMS: timeoutMS, BootstrapSeed: FaultKernelBootstrapSeed, BootstrapResamples: FaultKernelBootstrapResamples,
	}}
}

func NewFaultKernelInput(plan FaultKernelPlan, workloadHash, componentHash, configHash string) (FaultKernelInput, error) {
	input := FaultKernelInput{
		SchemaVersion: FaultKernelInputSchemaV1, Kind: FaultKernelKind, Profile: plan.Profile,
		PlanID: plan.PlanID, CaseID: plan.CaseID, WorkloadVersion: FaultKernelWorkloadVersion,
		FixtureProtocol: FaultKernelFixtureProtocol, EvidenceScope: FaultKernelEvidenceScope,
		ClockMode: FaultKernelClockMode, IsolationMode: FaultKernelIsolationMode, DaemonControl: FaultKernelDaemonControl,
		Protocol: plan.Protocol, Modes: []FaultKernelMode{
			{Treatment: "no-injection", Role: "baseline", FaultMode: "none", Injection: "matched-attempt-control", ExpectedRecovery: "attempt1-normal-completion"},
			{Treatment: "daemon-sigkill", Role: "treatment", FaultMode: "daemon_sigkill", Injection: "attempt1-ready-and-populated", ExpectedRecovery: "restart-cleanup-requeue-attempt2"},
		}, WorkloadSHA256: workloadHash, ComponentManifestSHA256: componentHash, RuntimeConfigSHA256: configHash,
	}
	input.Protocol.WarmupSeeds = append([]uint32(nil), input.Protocol.WarmupSeeds...)
	input.Protocol.MeasuredSeeds = append([]uint32(nil), input.Protocol.MeasuredSeeds...)
	return input, ValidateFaultKernelInput(input)
}

type FaultKernelPlanProtocol struct {
	WarmupSeeds        []uint32 `json:"warmup_seeds"`
	MeasuredSeeds      []uint32 `json:"measured_seeds"`
	Repetitions        int      `json:"repetitions"`
	Pairing            string   `json:"pairing"`
	Counterbalance     string   `json:"counterbalance"`
	TrialTimeoutMS     int      `json:"trial_timeout_ms"`
	BootstrapSeed      uint32   `json:"bootstrap_seed"`
	BootstrapResamples int      `json:"bootstrap_resamples"`
}

type FaultKernelPlan struct {
	Profile  string                  `json:"profile"`
	PlanID   string                  `json:"plan_id"`
	CaseID   string                  `json:"case_id"`
	Protocol FaultKernelPlanProtocol `json:"protocol"`
}

type FaultKernelMode struct {
	Treatment        string `json:"treatment"`
	Role             string `json:"role"`
	FaultMode        string `json:"fault_mode"`
	Injection        string `json:"injection_boundary"`
	ExpectedRecovery string `json:"expected_recovery"`
}

type FaultKernelInput struct {
	SchemaVersion           string                  `json:"schema_version"`
	Kind                    string                  `json:"kind"`
	Profile                 string                  `json:"profile"`
	PlanID                  string                  `json:"plan_id"`
	CaseID                  string                  `json:"case_id"`
	WorkloadVersion         string                  `json:"workload_version"`
	FixtureProtocol         string                  `json:"fixture_protocol"`
	EvidenceScope           string                  `json:"evidence_scope"`
	ClockMode               string                  `json:"clock_mode"`
	IsolationMode           string                  `json:"isolation_mode"`
	DaemonControl           string                  `json:"daemon_control"`
	Protocol                FaultKernelPlanProtocol `json:"protocol"`
	Modes                   []FaultKernelMode       `json:"modes"`
	WorkloadSHA256          string                  `json:"workload_sha256"`
	ComponentManifestSHA256 string                  `json:"component_manifest_sha256"`
	RuntimeConfigSHA256     string                  `json:"runtime_config_sha256"`
}

type FaultKernelTrialTrace struct {
	SchemaVersion         string                     `json:"schema_version"`
	RunID                 string                     `json:"run_id"`
	EvidenceScope         string                     `json:"evidence_scope"`
	Profile               string                     `json:"profile"`
	PlanID                string                     `json:"plan_id"`
	CaseID                string                     `json:"case_id"`
	PairID                string                     `json:"pair_id"`
	Seed                  uint32                     `json:"seed"`
	Repetition            int                        `json:"repetition"`
	TrialOrder            int                        `json:"trial_order"`
	Phase                 string                     `json:"phase"`
	Treatment             string                     `json:"treatment"`
	Role                  string                     `json:"role"`
	WorkloadSHA256        string                     `json:"workload_sha256"`
	InputSHA256           string                     `json:"input_sha256"`
	Status                string                     `json:"status"`
	TimeoutMS             int                        `json:"timeout_ms"`
	ClockMode             string                     `json:"clock_mode"`
	Protocol              FaultKernelTraceProtocol   `json:"protocol"`
	Milestones            FaultKernelMilestones      `json:"milestones"`
	StartupReceipt        *FaultKernelStartupReceipt `json:"startup_receipt"`
	Target                FaultKernelTargetTrace     `json:"target"`
	Survivor              FaultKernelSurvivorTrace   `json:"survivor"`
	CgroupSampleSequences []int                      `json:"cgroup_sample_sequences"`
	Observations          FaultKernelObservations    `json:"observations"`
	ReasonCode            string                     `json:"reason_code"`
}

type FaultKernelTraceProtocol struct {
	FaultMode         string `json:"fault_mode"`
	InjectionBoundary string `json:"injection_boundary"`
	IsolationMode     string `json:"isolation_mode"`
	DaemonControl     string `json:"daemon_control"`
	ExpectedRecovery  string `json:"expected_recovery"`
}

type FaultKernelMilestones struct {
	StartedNS         int64  `json:"started_ns"`
	Attempt1ReadyNS   int64  `json:"attempt1_ready_ns"`
	InjectedNS        *int64 `json:"injected_ns"`
	DaemonExitedNS    *int64 `json:"daemon_exited_ns"`
	CleanupCompleteNS *int64 `json:"cleanup_complete_ns"`
	Attempt2ReadyNS   *int64 `json:"attempt2_ready_ns"`
	RecoveredNS       *int64 `json:"recovered_ns"`
	FinishedNS        int64  `json:"finished_ns"`
}

type FaultKernelStartupReceipt struct {
	SchemaVersion     int  `json:"schema_version"`
	StateLoaded       bool `json:"state_loaded"`
	RecoveryPerformed bool `json:"recovery_performed"`
	RequeuedCount     int  `json:"requeued_count"`
	FailedCount       int  `json:"failed_count"`
	CanceledCount     int  `json:"canceled_count"`
}

type FaultKernelAttemptTrace struct {
	Number               int    `json:"number"`
	ReadyObserved        bool   `json:"ready_observed"`
	FinalStatus          string `json:"final_status"`
	ArtifactObservations int    `json:"artifact_observations"`
	ReasonCode           string `json:"reason_code"`
}

type FaultKernelTargetTrace struct {
	AttemptBefore int                       `json:"attempt_before"`
	AttemptAfter  int                       `json:"attempt_after"`
	Attempts      []FaultKernelAttemptTrace `json:"attempts"`
	FinalStatus   string                    `json:"final_status"`
}

type FaultKernelSurvivorTrace struct {
	CompletedBeforeInjection bool   `json:"completed_before_injection"`
	FinalStatus              string `json:"final_status"`
	ArtifactObservations     int    `json:"artifact_observations"`
}

type FaultKernelObservations struct {
	DaemonSIGKILLObserved            bool `json:"daemon_sigkill_observed"`
	OrphanObservedBeforeRestart      bool `json:"orphan_observed_before_restart"`
	CheckpointLoaded                 bool `json:"checkpoint_loaded"`
	RecoveryRequeued                 bool `json:"recovery_requeued"`
	RestartReady                     bool `json:"restart_ready"`
	OldProcessesAfterCleanupObserved bool `json:"old_processes_after_cleanup_observed"`
	OldProcessesAfterCleanup         int  `json:"old_processes_after_cleanup"`
	OldCgroupsAfterCleanupObserved   bool `json:"old_cgroups_after_cleanup_observed"`
	OldCgroupsAfterCleanup           int  `json:"old_cgroups_after_cleanup"`
	IsolationEscapeCount             int  `json:"isolation_escape_count"`
	DuplicateArtifactsObserved       bool `json:"duplicate_artifacts_observed"`
	DuplicateArtifacts               int  `json:"duplicate_artifacts"`
}

type FaultKernelCgroupSample struct {
	SchemaVersion          string                        `json:"schema_version"`
	RunID                  string                        `json:"run_id"`
	PairID                 string                        `json:"pair_id"`
	TrialOrder             int                           `json:"trial_order"`
	Phase                  string                        `json:"phase"`
	SampleSequence         int                           `json:"sample_sequence"`
	Stage                  string                        `json:"stage"`
	MonotonicNS            int64                         `json:"monotonic_ns"`
	AttemptNumber          int                           `json:"attempt_number"`
	Exists                 bool                          `json:"exists"`
	Populated              *bool                         `json:"populated"`
	MemberCount            *int                          `json:"member_count"`
	EscapedDescendantCount int                           `json:"escaped_descendant_count"`
	ControllerReadback     FaultKernelControllerReadback `json:"controller_readback"`
}

type FaultKernelControllerReadback struct {
	CPUMax    *string `json:"cpu_max"`
	MemoryMax *string `json:"memory_max"`
	PIDsMax   *string `json:"pids_max"`
}

type FaultKernelMetrics struct {
	DaemonExitMS *float64
	CleanupMS    *float64
	RetryStartMS *float64
	RecoveryMS   *float64
	ElapsedMS    float64
	Retries      int
}

func validateFaultKernelSeeds(groups ...[]uint32) error {
	seen := make(map[uint32]struct{})
	for _, group := range groups {
		for _, seed := range group {
			if _, exists := seen[seed]; exists {
				return fmt.Errorf("duplicate fault-kernel seed %d", seed)
			}
			seen[seed] = struct{}{}
		}
	}
	return nil
}

func faultKernelTraceIdentity(profile string) (string, string, bool) {
	if profile == FaultKernelProfileSmoke {
		return FaultKernelSmokePlanID, FaultKernelSmokeCaseID, true
	}
	if profile == FaultKernelProfileFormal {
		return FaultKernelFormalPlanID, FaultKernelFormalCaseID, true
	}
	return "", "", false
}

func validFaultKernelHash(value string) bool { return len(value) == 64 && isLowerHex(value) }

func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}
