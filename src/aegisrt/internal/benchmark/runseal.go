package benchmark

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type artifactPayload struct {
	Name      string
	Kind      string
	MediaType string
	Records   int
	Data      []byte
}

type trialObservation struct {
	Phase      string
	Status     string
	CaseID     string
	PairID     string
	Seed       uint32
	Repetition int
	TrialOrder int
	Treatment  string
	StartedAt  time.Time
	FinishedAt time.Time
}

type experimentEvidence struct {
	Kind           string
	PlanID         string
	EvidenceScope  string
	CaseIDs        []string
	WorkloadSHA256 string
	InputSHA256    string
	Protocol       ManifestProtocol
	Artifacts      []artifactPayload
	Trials         []trialObservation
	RowCount       int
}

type evidenceBuilder func(context.Context, string, func() time.Time) (experimentEvidence, error)

func collectExperiment(
	ctx context.Context,
	outputParent string,
	deps collectorDeps,
	build evidenceBuilder,
) (RunSummary, error) {
	parent, err := validateOutputParent(outputParent)
	if err != nil {
		return RunSummary{}, err
	}
	if ctx == nil {
		return RunSummary{}, fmt.Errorf("context is required")
	}
	if err := ctx.Err(); err != nil {
		return RunSummary{}, err
	}
	runID, err := deps.runID()
	if err != nil {
		return RunSummary{}, err
	}
	target, staging, err := runPaths(parent, runID)
	if err != nil {
		return RunSummary{}, err
	}
	started := deps.now()
	evidence, err := build(ctx, runID, deps.now)
	if err != nil {
		return RunSummary{}, err
	}
	if err := validateExperimentEvidence(evidence); err != nil {
		return RunSummary{}, err
	}
	environment, err := deps.environment()
	if err != nil {
		return RunSummary{}, err
	}
	if err := validateEnvironmentForScope(evidence.EvidenceScope, environment); err != nil {
		return RunSummary{}, err
	}
	software, err := deps.software(evidence.InputSHA256)
	if err != nil {
		return RunSummary{}, err
	}
	runFinished := deps.now()
	payloads, err := appendCommonPayloads(runID, evidence, environment, started, runFinished)
	if err != nil {
		return RunSummary{}, err
	}
	if err := createStaging(staging); err != nil {
		return RunSummary{}, err
	}
	artifacts, artifactData, err := writeArtifactPayloads(staging, payloads)
	if err != nil {
		return RunSummary{}, retainedStagingError(staging, err)
	}
	finished := deps.now()
	manifest := newManifest(runID, evidence, environment, software, started.UTC(), finished.UTC(), artifacts)
	manifestData, err := marshalJSON(manifest)
	if err != nil {
		return RunSummary{}, retainedStagingError(staging, err)
	}
	if err := scanAnonymous(appendArtifactData(artifactData, manifestData)...); err != nil {
		return RunSummary{}, retainedStagingError(staging, fmt.Errorf("anonymity scan failed: %w", err))
	}
	if err := writeExclusive(filepath.Join(staging, "manifest.json"), manifestData); err != nil {
		return RunSummary{}, retainedStagingError(staging, err)
	}
	if err := writeChecksums(staging, checksumNames(payloads)); err != nil {
		return RunSummary{}, retainedStagingError(staging, err)
	}
	if err := ctx.Err(); err != nil {
		return RunSummary{}, retainedStagingError(staging, err)
	}
	publish := deps.publish
	if publish == nil {
		publish = publishStaging
	}
	if err := publish(staging, target); err != nil {
		return RunSummary{}, retainedStagingError(staging, err)
	}
	return RunSummary{RunID: runID, OutputDir: target, RowCount: evidence.RowCount}, nil
}

func validateExperimentEvidence(evidence experimentEvidence) error {
	if !validLabel(evidence.Kind, true) || !validLabel(evidence.PlanID, true) ||
		!validLabel(evidence.EvidenceScope, true) || len(evidence.CaseIDs) == 0 {
		return fmt.Errorf("experiment evidence identity is invalid")
	}
	for _, caseID := range evidence.CaseIDs {
		if !validLabel(caseID, false) {
			return fmt.Errorf("experiment case id is invalid")
		}
	}
	if len(evidence.WorkloadSHA256) != 64 || !isLowerHex(evidence.WorkloadSHA256) ||
		len(evidence.InputSHA256) != 64 || !isLowerHex(evidence.InputSHA256) {
		return fmt.Errorf("experiment evidence hashes are invalid")
	}
	if evidence.RowCount <= 0 || len(evidence.Artifacts) == 0 {
		return fmt.Errorf("experiment evidence is empty")
	}
	for index, trial := range evidence.Trials {
		if trial.Status != "" && trial.Status != "SUCCEEDED" && trial.Status != "FAILED" &&
			trial.Status != "TIMED_OUT" && trial.Status != "INVALID" {
			return fmt.Errorf("experiment trial %d has an invalid status", index)
		}
	}
	return nil
}

func appendCommonPayloads(
	runID string,
	evidence experimentEvidence,
	environment Environment,
	started, finished time.Time,
) ([]artifactPayload, error) {
	events := buildEvents(runID, evidence.Kind, evidence.Trials, started, finished)
	var eventBuffer bytes.Buffer
	if err := writeEvents(&eventBuffer, events); err != nil {
		return nil, err
	}
	environmentData, err := marshalJSON(environment)
	if err != nil {
		return nil, err
	}
	payloads := append([]artifactPayload(nil), evidence.Artifacts...)
	payloads = append(payloads,
		artifactPayload{Name: "events.jsonl", Kind: "event-jsonl", MediaType: "application/x-ndjson", Records: len(events), Data: eventBuffer.Bytes()},
		artifactPayload{Name: "environment.json", Kind: "environment-json", MediaType: "application/json", Records: 1, Data: environmentData},
	)
	return payloads, nil
}

func writeArtifactPayloads(staging string, payloads []artifactPayload) ([]Artifact, [][]byte, error) {
	if err := validateArtifactPayloads(payloads); err != nil {
		return nil, nil, err
	}
	data := make([][]byte, len(payloads))
	for index := range payloads {
		data[index] = payloads[index].Data
	}
	if err := scanAnonymous(data...); err != nil {
		return nil, nil, err
	}
	artifacts := make([]Artifact, 0, len(payloads))
	for _, payload := range payloads {
		if err := writeExclusive(filepath.Join(staging, payload.Name), payload.Data); err != nil {
			return nil, nil, err
		}
		artifacts = append(artifacts, artifactFor(payload.Kind, payload.Name, payload.MediaType, payload.Records, payload.Data))
	}
	return artifacts, data, nil
}

func validateArtifactPayloads(payloads []artifactPayload) error {
	if len(payloads) == 0 {
		return fmt.Errorf("artifact payloads are empty")
	}
	seen := make(map[string]struct{}, len(payloads))
	for index, payload := range payloads {
		if !safeArtifactName(payload.Name) {
			return fmt.Errorf("artifact payload %d has unsafe filename", index)
		}
		lowerName := strings.ToLower(payload.Name)
		if lowerName == "manifest.json" || lowerName == "sha256sums" {
			return fmt.Errorf("artifact payload %d uses a reserved filename", index)
		}
		if _, exists := seen[lowerName]; exists {
			return fmt.Errorf("artifact payload %d duplicates filename", index)
		}
		seen[lowerName] = struct{}{}
		if !validLabel(payload.Kind, true) || strings.TrimSpace(payload.MediaType) == "" ||
			payload.Records < 0 || len(payload.Data) == 0 {
			return fmt.Errorf("artifact payload %d is invalid", index)
		}
	}
	return nil
}

func safeArtifactName(name string) bool {
	if len(name) == 0 || len(name) > 128 || strings.ContainsAny(name, `/\`) || strings.HasSuffix(name, ".") {
		return false
	}
	for index, char := range name {
		letter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		digit := char >= '0' && char <= '9'
		if !(letter || digit || index > 0 && (char == '.' || char == '_' || char == '-')) {
			return false
		}
	}
	base, _, _ := strings.Cut(strings.ToUpper(name), ".")
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return false
	}
	return !(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9')
}

func checksumNames(payloads []artifactPayload) []string {
	names := make([]string, 0, len(payloads)+1)
	for _, payload := range payloads {
		names = append(names, payload.Name)
	}
	return append(names, "manifest.json")
}
