package contextvm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

const LogicalPrefixMode = "logical-segment-prefix-v1"

type PrefixSegment struct {
	ContentSHA256 string `json:"content_sha256"`
	TokenCount    int    `json:"token_count"`
}

type PrefixObserverConfig struct {
	MaxNamespaces           int
	MaxSegmentsPerPrompt    int
	MaxTokensPerObservation int
}

type PrefixObservation struct {
	Mode                   string `json:"mode"`
	PhysicalCacheObserved  bool   `json:"physical_cache_observed"`
	CurrentFingerprint     string `json:"current_fingerprint"`
	PreviousFingerprint    string `json:"previous_fingerprint,omitempty"`
	PromptSegments         int    `json:"prompt_segments"`
	PromptTokens           int    `json:"prompt_tokens"`
	ReusablePrefixSegments int    `json:"reusable_prefix_segments"`
	ReusablePrefixTokens   int    `json:"reusable_prefix_tokens"`
	LogicalHit             bool   `json:"logical_hit"`
	NamespaceEvicted       bool   `json:"namespace_evicted"`
}

type PrefixObserverStats struct {
	Namespaces           int    `json:"namespaces"`
	Observations         uint64 `json:"observations"`
	LogicalHits          uint64 `json:"logical_hits"`
	PromptTokens         uint64 `json:"prompt_tokens"`
	ReusablePrefixTokens uint64 `json:"reusable_prefix_tokens"`
	Evictions            uint64 `json:"evictions"`
}

type prefixEntry struct {
	fingerprint string
	segments    []PrefixSegment
	lastUsed    uint64
}

// PrefixObserver measures exact reusable Context-page prefixes without storing
// raw prompt text. It observes logical candidates only; physical provider
// prompt/KV-cache hits require separate provider telemetry.
type PrefixObserver struct {
	mu      sync.Mutex
	config  PrefixObserverConfig
	entries map[string]prefixEntry
	clock   uint64
	stats   PrefixObserverStats
}

func NewPrefixObserver(config PrefixObserverConfig) (*PrefixObserver, error) {
	if config.MaxNamespaces <= 0 || config.MaxSegmentsPerPrompt <= 0 || config.MaxTokensPerObservation <= 0 {
		return nil, fmt.Errorf("positive prefix observer limits are required")
	}
	return &PrefixObserver{config: config, entries: make(map[string]prefixEntry)}, nil
}

func (o *PrefixObserver) Observe(namespace string, segments []PrefixSegment) (PrefixObservation, error) {
	if err := validatePrefixNamespace(namespace); err != nil {
		return PrefixObservation{}, err
	}
	validated, promptTokens, fingerprint, err := o.validateSegments(segments)
	if err != nil {
		return PrefixObservation{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	o.clock++
	observation := PrefixObservation{
		Mode: LogicalPrefixMode, CurrentFingerprint: fingerprint,
		PromptSegments: len(validated), PromptTokens: promptTokens,
	}
	if previous, exists := o.entries[namespace]; exists {
		observation.PreviousFingerprint = previous.fingerprint
		for index := 0; index < len(previous.segments) && index < len(validated); index++ {
			if previous.segments[index] != validated[index] {
				break
			}
			observation.ReusablePrefixSegments++
			observation.ReusablePrefixTokens += validated[index].TokenCount
		}
		observation.LogicalHit = observation.ReusablePrefixSegments > 0
	} else if len(o.entries) == o.config.MaxNamespaces {
		o.evictOldest()
		observation.NamespaceEvicted = true
	}
	o.entries[namespace] = prefixEntry{fingerprint: fingerprint, segments: validated, lastUsed: o.clock}
	o.stats.Observations++
	o.stats.PromptTokens += uint64(promptTokens)
	o.stats.ReusablePrefixTokens += uint64(observation.ReusablePrefixTokens)
	if observation.LogicalHit {
		o.stats.LogicalHits++
	}
	o.stats.Namespaces = len(o.entries)
	return observation, nil
}

func (o *PrefixObserver) Stats() PrefixObserverStats {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := o.stats
	result.Namespaces = len(o.entries)
	return result
}

func (o *PrefixObserver) validateSegments(segments []PrefixSegment) ([]PrefixSegment, int, string, error) {
	if len(segments) == 0 || len(segments) > o.config.MaxSegmentsPerPrompt {
		return nil, 0, "", fmt.Errorf("prefix segment count must be between 1 and %d", o.config.MaxSegmentsPerPrompt)
	}
	validated := append([]PrefixSegment(nil), segments...)
	tokens := 0
	hasher := sha256.New()
	for index, segment := range validated {
		decoded, err := hex.DecodeString(segment.ContentSHA256)
		if err != nil || len(decoded) != sha256.Size || segment.ContentSHA256 != strings.ToLower(segment.ContentSHA256) || segment.TokenCount <= 0 ||
			tokens > o.config.MaxTokensPerObservation-segment.TokenCount {
			return nil, 0, "", fmt.Errorf("invalid prefix segment %d", index)
		}
		tokens += segment.TokenCount
		hasher.Write(decoded)
		hasher.Write([]byte{0})
		hasher.Write([]byte(strconv.Itoa(segment.TokenCount)))
		hasher.Write([]byte{0})
	}
	return validated, tokens, hex.EncodeToString(hasher.Sum(nil)), nil
}

func (o *PrefixObserver) evictOldest() {
	oldestNamespace := ""
	var oldestUse uint64
	for namespace, entry := range o.entries {
		if oldestNamespace == "" || entry.lastUsed < oldestUse || entry.lastUsed == oldestUse && namespace < oldestNamespace {
			oldestNamespace, oldestUse = namespace, entry.lastUsed
		}
	}
	delete(o.entries, oldestNamespace)
	o.stats.Evictions++
}

func validatePrefixNamespace(value string) error {
	if len(value) == 0 || len(value) > 128 {
		return fmt.Errorf("prefix namespace length must be between 1 and 128")
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._:/-", character) {
			continue
		}
		return fmt.Errorf("prefix namespace contains unsupported characters")
	}
	return nil
}
