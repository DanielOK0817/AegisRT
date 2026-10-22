package contextstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"aegisrt/internal/contextvm"
)

func (r *Repository) Inspect(workflowID, agentID string) (Snapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return Snapshot{}, err
	}
	manifest, err := r.registry.Get(workflowID, agentID)
	if err != nil {
		return Snapshot{}, err
	}
	return inspectManifest(r.store, manifest)
}

func (r *Repository) Append(ctx context.Context, command AppendCommand, now time.Time) (AppendResult, error) {
	if ctx == nil {
		return AppendResult{}, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return AppendResult{}, err
	}
	fingerprint, err := validateAppendCommand(command, r.config)
	if err != nil {
		return AppendResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return AppendResult{}, err
	}
	candidate, err := r.cloneComponentsLocked()
	if err != nil {
		return AppendResult{}, r.poisonLocked(err)
	}
	result, changed, err := applyInputCandidate(&candidate, command, fingerprint, r.config)
	if errors.Is(err, errSequenceExhausted) {
		return AppendResult{}, r.poisonLocked(err)
	}
	if err != nil {
		return AppendResult{}, err
	}
	if !changed {
		return result, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := r.commitLocked(candidate, now); err != nil {
		return result, err
	}
	return result, nil
}

var errSequenceExhausted = errors.New("context idempotency sequence exhausted")

func applyAppendCandidate(candidate *components, command AppendCommand, fingerprint string, config Config) (AppendResult, bool, error) {
	key := ledgerKey{taskID: command.TaskID, key: command.IdempotencyKey}
	if record, exists := candidate.ledger[key]; exists {
		if record.Fingerprint != fingerprint {
			return AppendResult{}, false, ErrIdempotencyConflict
		}
		result := record.Result
		result.IdempotentReplay = true
		return result, false, nil
	}
	current, err := candidate.registry.Get(command.WorkflowID, command.AgentID)
	if err != nil {
		return AppendResult{}, false, err
	}
	if current.ID != command.ExpectedManifestID {
		return AppendResult{}, false, ErrPreconditionFailed
	}
	page, err := newCommandPage(command, config.Counter)
	if err != nil {
		return AppendResult{}, false, err
	}
	if page.TokenCount > config.Limits.MaxPageTokens || int64(page.TokenCount) > command.TaskTokenBudget {
		return AppendResult{}, false, ErrPageTokenLimit
	}
	ref := contextvm.ContextRef{PageID: page.ID, Pin: command.Pin, Cold: command.Cold}
	result := AppendResult{TaskID: command.TaskID, PageID: page.ID, TokenCount: page.TokenCount}
	next, changed, err := appendManifest(current, page, ref)
	if err != nil {
		return AppendResult{}, false, err
	}
	if changed {
		if err := validateManifestLimits(candidate.store, current, page, ref, config.Limits, command.PrincipalPinnedBudget); err != nil {
			return AppendResult{}, false, err
		}
		if _, _, err := candidate.store.Put(page); err != nil {
			return AppendResult{}, false, err
		}
		if err := next.Validate(candidate.store); err != nil {
			return AppendResult{}, false, err
		}
		candidate.history[next.ID] = cloneManifest(next)
		candidate.heads[principal{workflowID: command.WorkflowID, agentID: command.AgentID}] = next.ID
		if err := candidate.registry.Set(next); err != nil {
			return AppendResult{}, false, err
		}
		result.ManifestID, result.Revision, result.ManifestChanged = next.ID, next.Revision, true
	} else {
		result.ManifestID, result.Revision = current.ID, current.Revision
	}
	if candidate.nextSeq == math.MaxUint64 {
		return AppendResult{}, false, errSequenceExhausted
	}
	candidate.nextSeq++
	candidate.ledger[key] = idempotencyRecord{TaskID: command.TaskID, Key: command.IdempotencyKey,
		Fingerprint: fingerprint, Sequence: candidate.nextSeq, Result: result}
	evictLedger(candidate.ledger, config.Limits.MaxLedgerEntries)
	return result, true, nil
}

func validateAppendCommand(command AppendCommand, config Config) (string, error) {
	if !validIdempotencyKey(command.TaskID) {
		return "", ErrInvalidTaskBinding
	}
	if !validIdempotencyKey(command.IdempotencyKey) {
		return "", ErrInvalidIdempotencyKey
	}
	if command.ExpectedManifestID == "" || len(command.ExpectedManifestID) != 64 {
		return "", ErrPreconditionFailed
	}
	if _, err := hex.DecodeString(command.ExpectedManifestID); err != nil {
		return "", ErrPreconditionFailed
	}
	if command.TaskTokenBudget <= 0 {
		return "", ErrTaskContextDisabled
	}
	if command.PrincipalPinnedBudget < 0 {
		return "", ErrPinnedTokenLimit
	}
	if !utf8.ValidString(command.Content) || strings.ContainsRune(command.Content, '\x00') || strings.TrimSpace(command.Content) == "" {
		return "", ErrInvalidContent
	}
	if len(command.Content) > config.Limits.MaxPageBytes {
		return "", ErrPageTooLarge
	}
	if command.Visibility != VisibilityWorkflow && command.Visibility != VisibilityAgent {
		return "", ErrScopeForbidden
	}
	if command.Pin && command.Cold {
		return "", ErrInvalidContent
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%t\x00%t",
		command.TaskID, command.WorkflowID, command.AgentID, command.Visibility,
		command.Pin, command.Cold) + "\x00" + command.Content))
	return hex.EncodeToString(digest[:]), nil
}

func newCommandPage(command AppendCommand, counter contextvm.Counter) (contextvm.Page, error) {
	spec := contextvm.PageSpec{Content: command.Content, WorkflowID: command.WorkflowID}
	if command.Visibility == VisibilityWorkflow {
		spec.Scope = contextvm.ScopeWorkflow
	} else {
		spec.Scope = contextvm.ScopeAgent
		spec.AgentID = command.AgentID
	}
	return contextvm.NewPage(spec, counter)
}

func appendManifest(current contextvm.Manifest, page contextvm.Page, ref contextvm.ContextRef) (contextvm.Manifest, bool, error) {
	for _, existing := range current.Refs() {
		if existing.PageID != ref.PageID {
			continue
		}
		if existing.Pin == ref.Pin && existing.Cold == ref.Cold {
			return current, false, nil
		}
		return contextvm.Manifest{}, false, ErrReferenceConflict
	}
	next, err := current.Append(page, ref)
	if errors.Is(err, contextvm.ErrDuplicateReference) || errors.Is(err, contextvm.ErrReferenceFlagsConflict) {
		return contextvm.Manifest{}, false, ErrReferenceConflict
	}
	return next, err == nil, err
}

func validateManifestLimits(store *contextvm.Store, current contextvm.Manifest, page contextvm.Page,
	ref contextvm.ContextRef, limits Limits, pinnedBudget int64) error {
	snapshot, err := inspectManifest(store, current)
	if err != nil {
		return err
	}
	if snapshot.PageCount >= limits.MaxManifestPages {
		return ErrManifestPageLimit
	}
	if snapshot.LogicalTokens > limits.MaxManifestTokens-page.TokenCount {
		return ErrManifestTokenLimit
	}
	if ref.Pin && int64(snapshot.PinnedTokens) > pinnedBudget-int64(page.TokenCount) {
		return ErrPinnedTokenLimit
	}
	return nil
}

func inspectManifest(store *contextvm.Store, manifest contextvm.Manifest) (Snapshot, error) {
	request := contextvm.AccessRequest{WorkflowID: manifest.WorkflowID, AgentID: manifest.AgentID}
	result := Snapshot{ManifestID: manifest.ID, Revision: manifest.Revision, PageCount: len(manifest.Refs())}
	for _, ref := range manifest.Refs() {
		page, err := store.Get(ref, request)
		if err != nil {
			return Snapshot{}, err
		}
		if result.LogicalTokens > math.MaxInt-page.TokenCount {
			return Snapshot{}, ErrManifestTokenLimit
		}
		result.LogicalTokens += page.TokenCount
		if ref.Pin {
			if result.PinnedTokens > math.MaxInt-page.TokenCount {
				return Snapshot{}, ErrPinnedTokenLimit
			}
			result.PinnedTokens += page.TokenCount
		}
	}
	return result, nil
}

func evictLedger(ledger map[ledgerKey]idempotencyRecord, limit int) {
	for len(ledger) > limit {
		var oldestKey ledgerKey
		oldestSequence := uint64(math.MaxUint64)
		for key, record := range ledger {
			if record.Sequence < oldestSequence {
				oldestKey, oldestSequence = key, record.Sequence
			}
		}
		delete(ledger, oldestKey)
	}
}

func validIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
			index > 0 && (char == '-' || char == '_' || char == '.' || char == ':')
		if !valid {
			return false
		}
	}
	return true
}
