package benchmark

// FaultKernelComponentManifest binds every executable implementation that can
// affect one F4 run without publishing local executable paths.
type FaultKernelComponentManifest struct {
	SchemaVersion string                 `json:"schema_version"`
	Components    []FaultKernelComponent `json:"components"`
}

type FaultKernelComponent struct {
	Role           string  `json:"role"`
	ArtifactKind   string  `json:"artifact_kind"`
	Bytes          int64   `json:"bytes"`
	SHA256         string  `json:"sha256"`
	SourceRevision *string `json:"source_revision"`
}
