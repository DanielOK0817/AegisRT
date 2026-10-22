package benchmark

import "time"

type Manifest struct {
	SchemaVersion string             `json:"schema_version"`
	RunID         string             `json:"run_id"`
	Status        string             `json:"status"`
	Experiment    ManifestExperiment `json:"experiment"`
	Protocol      ManifestProtocol   `json:"protocol"`
	Environment   Environment        `json:"environment"`
	Software      Software           `json:"software"`
	StartedAt     time.Time          `json:"started_at"`
	FinishedAt    time.Time          `json:"finished_at"`
	Integrity     ManifestIntegrity  `json:"integrity"`
	Anonymization Anonymization      `json:"anonymization"`
	Artifacts     []Artifact         `json:"artifacts"`
}

type ManifestExperiment struct {
	Kind           string   `json:"kind"`
	PlanID         string   `json:"plan_id"`
	CaseIDs        []string `json:"case_ids"`
	WorkloadSHA256 string   `json:"workload_sha256"`
	InputSHA256    string   `json:"input_sha256"`
}

type ManifestProtocol struct {
	WarmupPairs        int      `json:"warmup_pairs"`
	MeasuredPairs      int      `json:"measured_pairs"`
	Seeds              []uint32 `json:"seeds"`
	Pairing            string   `json:"pairing"`
	Counterbalance     string   `json:"counterbalance"`
	TrialTimeoutMS     int      `json:"trial_timeout_ms"`
	BootstrapSeed      uint32   `json:"bootstrap_seed"`
	BootstrapResamples int      `json:"bootstrap_resamples"`
}

type ManifestIntegrity struct {
	DirectoryPolicy   string `json:"directory_policy"`
	TamperEvident     bool   `json:"tamper_evident"`
	WORMStorage       bool   `json:"worm_storage"`
	ChecksumAlgorithm string `json:"checksum_algorithm"`
	ChecksumFile      string `json:"checksum_file"`
}

type Anonymization struct {
	ContainsPersonalIdentifiers bool   `json:"contains_personal_identifiers"`
	ContainsCredentials         bool   `json:"contains_credentials"`
	ContainsAbsolutePaths       bool   `json:"contains_absolute_paths"`
	RawOutputCapture            bool   `json:"raw_output_capture"`
	ScanResult                  string `json:"scan_result"`
}

type Artifact struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Records   int    `json:"records"`
}

func newManifest(
	runID string,
	evidence experimentEvidence,
	environment Environment,
	software Software,
	started, finished time.Time,
	artifacts []Artifact,
) Manifest {
	protocol := evidence.Protocol
	protocol.Seeds = append([]uint32(nil), evidence.Protocol.Seeds...)
	return Manifest{
		SchemaVersion: manifestSchemaV1, RunID: runID, Status: "COMPLETED",
		Experiment: ManifestExperiment{
			Kind: evidence.Kind, PlanID: evidence.PlanID, CaseIDs: append([]string(nil), evidence.CaseIDs...),
			WorkloadSHA256: evidence.WorkloadSHA256, InputSHA256: evidence.InputSHA256,
		},
		Protocol:    protocol,
		Environment: environment, Software: software, StartedAt: started, FinishedAt: finished,
		Integrity: ManifestIntegrity{
			DirectoryPolicy: "create-only-atomic-publish", TamperEvident: true,
			WORMStorage: false, ChecksumAlgorithm: "SHA-256", ChecksumFile: "SHA256SUMS",
		},
		Anonymization: Anonymization{ScanResult: "PASSED"}, Artifacts: artifacts,
	}
}
