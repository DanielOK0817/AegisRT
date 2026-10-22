package benchmark

import (
	"fmt"
	"time"
)

// CommunicationPlan fixes the local workload; these are observations, not SLOs.
type CommunicationPlan struct {
	PayloadBytes []int `json:"payload_bytes"`
	Recipients   []int `json:"recipients"`
	Concurrency  []int `json:"concurrency"`
	Operations   int   `json:"operations_per_trial"`
	Repetitions  int   `json:"repetitions"`
	ReadPasses   int   `json:"read_passes_per_load_sample"`
}

func NewCommunicationSmokePlan() CommunicationPlan {
	return CommunicationPlan{PayloadBytes: []int{256, 4096, 32768}, Recipients: []int{1, 4},
		Concurrency: []int{1, 4}, Operations: 4, Repetitions: 3, ReadPasses: 32}
}

func (p CommunicationPlan) validate() error {
	if p.ReadPasses != 32 {
		return fmt.Errorf("communication profile requires 32 read passes")
	}
	if p.Operations < 1 || p.Operations > 32 || p.Repetitions < 1 || p.Repetitions > 10 {
		return fmt.Errorf("communication operations or repetitions outside bounded range")
	}
	for _, values := range [][]int{p.PayloadBytes, p.Recipients, p.Concurrency} {
		if len(values) == 0 || len(values) > 8 {
			return fmt.Errorf("communication dimensions must contain 1..8 values")
		}
		seen := make(map[int]bool)
		for _, value := range values {
			if value < 1 || seen[value] {
				return fmt.Errorf("communication dimensions must be positive and unique")
			}
			seen[value] = true
		}
	}
	for _, n := range p.PayloadBytes {
		if n > 32768 {
			return fmt.Errorf("payload exceeds 32768 bytes")
		}
	}
	for _, n := range p.Recipients {
		if n > 8 {
			return fmt.Errorf("recipients exceed 8")
		}
	}
	for _, n := range p.Concurrency {
		if n > p.Operations {
			return fmt.Errorf("concurrency exceeds operation count")
		}
	}
	return nil
}

type CommunicationOptions struct {
	OutputDir    string
	SourceSHA256 string
	Plan         CommunicationPlan
}

type CommunicationCase struct {
	Operation    string `json:"operation"`
	PayloadBytes int    `json:"payload_bytes"`
	Recipients   int    `json:"recipients"`
	Concurrency  int    `json:"concurrency"`
	ReadPasses   int    `json:"read_passes"`
}

type CommunicationSample struct {
	Index          int   `json:"index"`
	LatencyNS      int64 `json:"latency_ns"`
	DeliveredBytes int   `json:"delivered_bytes"`
}

type CommunicationTrial struct {
	Case                          CommunicationCase     `json:"case"`
	Repetition                    int                   `json:"repetition"`
	StartedAt                     time.Time             `json:"started_at"`
	WallNS                        int64                 `json:"wall_ns"`
	Samples                       []CommunicationSample `json:"samples"`
	P50NS                         int64                 `json:"p50_ns"`
	P95NS                         int64                 `json:"p95_ns"`
	TotalAllocBytes               uint64                `json:"total_alloc_bytes"`
	HeapAllocBeforeBytes          uint64                `json:"heap_alloc_before_bytes"`
	HeapAllocAfterBytes           uint64                `json:"heap_alloc_after_bytes"`
	Mallocs                       uint64                `json:"mallocs"`
	GCCycles                      uint32                `json:"gc_cycles"`
	CheckpointBeforeBytes         int64                 `json:"checkpoint_before_bytes"`
	CheckpointAfterBytes          int64                 `json:"checkpoint_after_bytes"`
	CheckpointSaveCalls           uint64                `json:"checkpoint_save_calls"`
	CheckpointPayloadWrittenBytes uint64                `json:"checkpoint_payload_written_bytes"`
	PhysicalBeforeBytes           uint64                `json:"physical_before_bytes"`
	PhysicalAfterBytes            uint64                `json:"physical_after_bytes"`
	ReplayStable                  bool                  `json:"replay_stable"`
	ReloadVerified                bool                  `json:"reload_verified"`
	IsolationVerified             bool                  `json:"isolation_verified"`
}

type CommunicationReport struct {
	SchemaVersion int                  `json:"schema_version"`
	Status        string               `json:"status"`
	RunID         string               `json:"run_id"`
	StartedAt     time.Time            `json:"started_at"`
	FinishedAt    time.Time            `json:"finished_at"`
	SourceSHA256  string               `json:"build_source_sha256"`
	Software      Software             `json:"software"`
	Environment   Environment          `json:"environment"`
	Plan          CommunicationPlan    `json:"plan"`
	Counter       string               `json:"counter"`
	GOMAXPROCS    int                  `json:"gomaxprocs"`
	Scope         []string             `json:"scope"`
	Trials        []CommunicationTrial `json:"trials"`
	Failure       string               `json:"failure,omitempty"`
}
