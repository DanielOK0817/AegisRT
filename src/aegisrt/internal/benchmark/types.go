package benchmark

import "time"

type SchedulerPlan struct {
	Profile     string   `json:"profile,omitempty"`
	Seeds       []uint32 `json:"seeds"`
	Repetitions int      `json:"repetitions"`
}

type CollectOptions struct {
	OutputDir string
	Plan      SchedulerPlan
}

type RunSummary struct {
	RunID     string
	OutputDir string
	RowCount  int
}

type SchedulerResult struct {
	Rows           []SchedulerRow
	WorkloadSHA256 string
	InputSHA256    string
	traces         []trialTrace
}

type trialTrace struct {
	rowIndex int
	started  time.Time
	finished time.Time
}

type SchedulerRow struct {
	SchemaVersion       string
	RunID               string
	Status              string
	EvidenceScope       string
	CaseID              string
	PairID              string
	Seed                uint32
	Repetition          int
	TrialOrder          int
	Treatment           string
	Role                string
	Policy              string
	WorkflowShape       string
	WorkloadSHA256      string
	WorkflowCount       int
	TaskCount           int
	CapacityCPUMillis   int64
	CapacityMemoryBytes int64
	CapacityTokens      int64
	CapacityToolSlots   int64
	MakespanMS          float64
	QueueP50MS          float64
	QueueP95MS          float64
	CriticalPathMS      float64
	DependencyUnlockMS  float64
	ThroughputTasksS    float64
	JainFairness        float64
	AdmissionRejections int
	StarvationCount     int
	DecisionCount       int
	TimeoutMS           int
	ReasonCode          string
}

type CgroupEnvironment struct {
	Version     string   `json:"version"`
	Controllers []string `json:"controllers"`
	Delegation  string   `json:"delegation"`
	PSI         string   `json:"psi"`
}

type Environment struct {
	EvidenceScope       string            `json:"evidence_scope"`
	OSFamily            string            `json:"os_family"`
	DistributionID      string            `json:"distribution_id"`
	DistributionVersion string            `json:"distribution_version"`
	KernelRelease       string            `json:"kernel_release"`
	Architecture        string            `json:"architecture"`
	LogicalCPUCount     int               `json:"logical_cpu_count"`
	MemoryTotalBytes    uint64            `json:"memory_total_bytes"`
	Cgroup              CgroupEnvironment `json:"cgroup"`
}

type Software struct {
	RuntimeCommit string `json:"runtime_commit"`
	RuntimeDirty  bool   `json:"runtime_dirty"`
	GoVersion     string `json:"go_version"`
	BinarySHA256  string `json:"binary_sha256"`
	ConfigSHA256  string `json:"config_sha256"`
}
