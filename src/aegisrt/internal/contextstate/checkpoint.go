package contextstate

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"aegisrt/internal/contextdispatch"
	"aegisrt/internal/contextvm"
)

const checkpointFormatVersion = 2

type checkpoint struct {
	FormatVersion      int                  `json:"format_version"`
	Generation         uint64               `json:"generation"`
	SavedAt            time.Time            `json:"saved_at"`
	ConfigFingerprint  string               `json:"config_fingerprint"`
	CounterIdentity    string               `json:"counter_identity"`
	SummarizerIdentity string               `json:"summarizer_identity"`
	RuntimeInstanceID  string               `json:"runtime_instance_id"`
	Store              contextvm.StoreState `json:"store"`
	Manifests          []contextvm.Manifest `json:"manifests,omitempty"`
	Heads              []manifestHead       `json:"heads,omitempty"`
	Idempotency        []idempotencyRecord  `json:"idempotency,omitempty"`
	Outputs            []OutputRecord       `json:"outputs,omitempty"`
	TaskInputs         []taskInputBinding   `json:"task_inputs,omitempty"`
	Summaries          []summaryRecord      `json:"summaries,omitempty"`
	NextSequence       uint64               `json:"next_sequence"`
}

type manifestHead struct {
	WorkflowID string `json:"workflow_id"`
	AgentID    string `json:"agent_id"`
	ManifestID string `json:"manifest_id"`
}

type principal struct {
	workflowID string
	agentID    string
}

type ledgerKey struct {
	taskID string
	key    string
}

type idempotencyRecord struct {
	TaskID      string       `json:"task_id"`
	Key         string       `json:"key"`
	Fingerprint string       `json:"fingerprint"`
	Sequence    uint64       `json:"sequence"`
	Result      AppendResult `json:"result"`
}

type components struct {
	store              *contextvm.Store
	registry           *contextdispatch.Registry
	resolver           *contextdispatch.Resolver
	history            map[string]contextvm.Manifest
	heads              map[principal]string
	ledger             map[ledgerKey]idempotencyRecord
	outputs            map[string]OutputRecord
	inputs             map[taskInputKey]taskInputBinding
	summaries          map[summaryKey]summaryRecord
	counterIdentity    string
	summarizerIdentity string
	nextSeq            uint64
}

func decodeCheckpoint(payload []byte) (checkpoint, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var stored checkpoint
	if err := decoder.Decode(&stored); err != nil {
		return checkpoint{}, fmt.Errorf("%w: decode: %v", ErrInvalidCheckpoint, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return checkpoint{}, fmt.Errorf("%w: trailing JSON value", ErrInvalidCheckpoint)
		}
		return checkpoint{}, fmt.Errorf("%w: trailing data: %v", ErrInvalidCheckpoint, err)
	}
	if stored.FormatVersion == 1 {
		return checkpoint{}, fmt.Errorf("%w: version 1 does not preserve task input ownership", ErrMigrationRequired)
	}
	if stored.FormatVersion != checkpointFormatVersion || stored.Generation == 0 || stored.SavedAt.IsZero() {
		return checkpoint{}, fmt.Errorf("%w: unsupported version or missing generation metadata", ErrInvalidCheckpoint)
	}
	if err := validateRuntimeBindingID(stored.RuntimeInstanceID); err != nil {
		return checkpoint{}, fmt.Errorf("%w: Runtime instance ID: %v", ErrInvalidCheckpoint, err)
	}
	return stored, nil
}

func buildComponents(config Config, stored checkpoint) (components, error) {
	if err := validateStoreLimits(stored.Store, config.Limits); err != nil {
		return components{}, err
	}
	store, err := contextvm.NewStoreFromState(config.Counter, stored.Store)
	if err != nil {
		return components{}, fmt.Errorf("%w: restore pages: %v", ErrInvalidCheckpoint, err)
	}
	return buildComponentsWithStore(config, stored, store)
}

func buildComponentsWithStore(config Config, stored checkpoint, store *contextvm.Store) (components, error) {
	history := make(map[string]contextvm.Manifest, len(stored.Manifests))
	for _, manifest := range stored.Manifests {
		if _, exists := history[manifest.ID]; exists {
			return components{}, fmt.Errorf("%w: duplicate manifest %q", ErrInvalidCheckpoint, manifest.ID)
		}
		if err := manifest.Validate(store); err != nil {
			return components{}, fmt.Errorf("%w: manifest %q: %v", ErrInvalidCheckpoint, manifest.ID, err)
		}
		if err := validateRestoredManifestLimits(store, manifest, config.Limits); err != nil {
			return components{}, err
		}
		history[manifest.ID] = cloneManifest(manifest)
	}
	for id, manifest := range history {
		if manifest.ParentID == "" {
			if manifest.Revision != 1 {
				return components{}, fmt.Errorf("%w: root manifest %q revision is not one", ErrInvalidCheckpoint, id)
			}
			continue
		}
		parent, exists := history[manifest.ParentID]
		if !exists || parent.WorkflowID != manifest.WorkflowID || parent.AgentID != manifest.AgentID ||
			parent.Revision == ^uint64(0) || manifest.Revision != parent.Revision+1 {
			return components{}, fmt.Errorf("%w: manifest %q has invalid parent lineage", ErrInvalidCheckpoint, id)
		}
	}
	registry := contextdispatch.NewRegistry()
	heads := make(map[principal]string, len(stored.Heads))
	for _, head := range stored.Heads {
		key := principal{workflowID: head.WorkflowID, agentID: head.AgentID}
		if _, exists := heads[key]; exists {
			return components{}, fmt.Errorf("%w: duplicate manifest head", ErrInvalidCheckpoint)
		}
		manifest, exists := history[head.ManifestID]
		if !exists || manifest.WorkflowID != head.WorkflowID || manifest.AgentID != head.AgentID {
			return components{}, fmt.Errorf("%w: invalid manifest head", ErrInvalidCheckpoint)
		}
		if err := registry.Set(manifest); err != nil {
			return components{}, fmt.Errorf("%w: restore manifest head: %v", ErrInvalidCheckpoint, err)
		}
		heads[key] = head.ManifestID
	}
	if len(stored.Idempotency) > config.Limits.MaxLedgerEntries {
		return components{}, fmt.Errorf("%w: idempotency ledger exceeds configured limit", ErrInvalidCheckpoint)
	}
	ledger := make(map[ledgerKey]idempotencyRecord, len(stored.Idempotency))
	sequences := make(map[uint64]struct{}, len(stored.Idempotency))
	var maxSequence uint64
	for _, record := range stored.Idempotency {
		key := ledgerKey{taskID: record.TaskID, key: record.Key}
		fingerprint, digestErr := hex.DecodeString(record.Fingerprint)
		if !validIdempotencyKey(record.TaskID) || !validIdempotencyKey(record.Key) ||
			digestErr != nil || len(fingerprint) != 32 || record.Sequence == 0 {
			return components{}, fmt.Errorf("%w: malformed idempotency record", ErrInvalidCheckpoint)
		}
		if _, exists := ledger[key]; exists {
			return components{}, fmt.Errorf("%w: duplicate idempotency record", ErrInvalidCheckpoint)
		}
		if _, exists := sequences[record.Sequence]; exists {
			return components{}, fmt.Errorf("%w: duplicate idempotency sequence", ErrInvalidCheckpoint)
		}
		sequences[record.Sequence] = struct{}{}
		resultManifest, exists := history[record.Result.ManifestID]
		if !exists {
			return components{}, fmt.Errorf("%w: idempotency result references missing manifest", ErrInvalidCheckpoint)
		}
		if err := validateStoredResult(store, resultManifest, record); err != nil {
			return components{}, err
		}
		ledger[key] = record
		if record.Sequence > maxSequence {
			maxSequence = record.Sequence
		}
	}
	if stored.NextSequence != maxSequence {
		return components{}, fmt.Errorf("%w: idempotency sequence is not canonical", ErrInvalidCheckpoint)
	}
	if len(stored.Outputs) > config.Limits.MaxLedgerEntries {
		return components{}, fmt.Errorf("%w: output records exceed configured limit", ErrInvalidCheckpoint)
	}
	outputs := make(map[string]OutputRecord, len(stored.Outputs))
	for _, record := range stored.Outputs {
		if _, exists := outputs[record.TaskID]; exists {
			return components{}, fmt.Errorf("%w: duplicate output record", ErrInvalidCheckpoint)
		}
		if err := validateStoredOutput(store, history, record); err != nil {
			return components{}, err
		}
		outputs[record.TaskID] = cloneOutputRecord(record)
	}
	summaries, err := restoreSummaries(config, store, stored.Summaries)
	if err != nil {
		return components{}, err
	}
	var summarizer contextvm.Summarizer
	if config.Summarizer != nil {
		summarizer = &cachedSummarizer{config: config, store: store, records: summaries}
	}
	assembler, err := contextvm.NewAssembler(store, config.Counter, summarizer)
	if err != nil {
		return components{}, fmt.Errorf("%w: create assembler: %v", ErrInvalidCheckpoint, err)
	}
	resolver, err := contextdispatch.NewResolver(registry, assembler)
	if err != nil {
		return components{}, fmt.Errorf("%w: create resolver: %v", ErrInvalidCheckpoint, err)
	}
	value := components{store: store, registry: registry, resolver: resolver, history: history,
		heads: heads, ledger: ledger, outputs: outputs, summaries: summaries, nextSeq: stored.NextSequence}
	value.counterIdentity, value.summarizerIdentity = componentIdentities(config)
	if err := restoreTaskInputs(&value, stored.TaskInputs); err != nil {
		return components{}, err
	}
	return value, nil
}

func validateStoreLimits(state contextvm.StoreState, limits Limits) error {
	for _, blob := range state.Blobs {
		if len(blob.Content) > limits.MaxPageBytes || blob.TokenCount > limits.MaxPageTokens {
			return fmt.Errorf("%w: stored page exceeds configured limits", ErrInvalidCheckpoint)
		}
	}
	return nil
}

func validateRestoredManifestLimits(store *contextvm.Store, manifest contextvm.Manifest, limits Limits) error {
	refs := manifest.Refs()
	if len(refs) > limits.MaxManifestPages {
		return fmt.Errorf("%w: manifest %q exceeds page limit", ErrInvalidCheckpoint, manifest.ID)
	}
	tokens := 0
	for _, ref := range refs {
		page, err := store.Lookup(ref.PageID)
		if err != nil || tokens > limits.MaxManifestTokens-page.TokenCount {
			return fmt.Errorf("%w: manifest %q exceeds token limit", ErrInvalidCheckpoint, manifest.ID)
		}
		tokens += page.TokenCount
	}
	return nil
}

func validateStoredResult(store *contextvm.Store, manifest contextvm.Manifest, record idempotencyRecord) error {
	result := record.Result
	if result.TaskID != record.TaskID || result.Revision != manifest.Revision || result.IdempotentReplay {
		return fmt.Errorf("%w: inconsistent idempotency result metadata", ErrInvalidCheckpoint)
	}
	matchingRef := contextvm.ContextRef{}
	found := false
	for _, ref := range manifest.Refs() {
		if ref.PageID == result.PageID {
			matchingRef, found = ref, true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: idempotency result page is absent from manifest", ErrInvalidCheckpoint)
	}
	page, err := store.Lookup(matchingRef.PageID)
	if err != nil || page.TokenCount != result.TokenCount {
		return fmt.Errorf("%w: idempotency result page metadata is invalid", ErrInvalidCheckpoint)
	}
	return nil
}

func checkpointFromComponents(
	value components,
	fingerprint string,
	runtimeInstanceID string,
	generation uint64,
	savedAt time.Time,
) checkpoint {
	manifests := make([]contextvm.Manifest, 0, len(value.history))
	for _, manifest := range value.history {
		manifests = append(manifests, cloneManifest(manifest))
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
	heads := make([]manifestHead, 0, len(value.heads))
	for key, manifestID := range value.heads {
		heads = append(heads, manifestHead{WorkflowID: key.workflowID, AgentID: key.agentID, ManifestID: manifestID})
	}
	sort.Slice(heads, func(i, j int) bool {
		if heads[i].WorkflowID != heads[j].WorkflowID {
			return heads[i].WorkflowID < heads[j].WorkflowID
		}
		return heads[i].AgentID < heads[j].AgentID
	})
	ledger := make([]idempotencyRecord, 0, len(value.ledger))
	for _, record := range value.ledger {
		ledger = append(ledger, record)
	}
	sort.Slice(ledger, func(i, j int) bool {
		if ledger[i].TaskID != ledger[j].TaskID {
			return ledger[i].TaskID < ledger[j].TaskID
		}
		return ledger[i].Key < ledger[j].Key
	})
	outputs := make([]OutputRecord, 0, len(value.outputs))
	for _, record := range value.outputs {
		outputs = append(outputs, cloneOutputRecord(record))
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].TaskID < outputs[j].TaskID })
	return checkpoint{FormatVersion: checkpointFormatVersion, Generation: generation, SavedAt: savedAt.UTC(),
		ConfigFingerprint: fingerprint, RuntimeInstanceID: runtimeInstanceID,
		CounterIdentity: value.counterIdentity, SummarizerIdentity: value.summarizerIdentity, Summaries: sortedSummaries(value),
		Store: value.store.State(), Manifests: manifests,
		Heads: heads, Idempotency: ledger, Outputs: outputs, TaskInputs: sortedTaskInputs(value), NextSequence: value.nextSeq}
}

func cloneManifest(manifest contextvm.Manifest) contextvm.Manifest {
	manifest.SharedPages = append([]contextvm.ContextRef(nil), manifest.SharedPages...)
	manifest.PrivatePages = append([]contextvm.ContextRef(nil), manifest.PrivatePages...)
	return manifest
}
