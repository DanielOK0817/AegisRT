package contextvm

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
)

func TestPrefixObserverMeasuresExactLogicalPrefix(t *testing.T) {
	observer := mustPrefixObserver(t, PrefixObserverConfig{MaxNamespaces: 2, MaxSegmentsPerPrompt: 4, MaxTokensPerObservation: 100})
	first := []PrefixSegment{prefixSegment("system", 10), prefixSegment("workflow", 7), prefixSegment("task-a", 3)}
	second := []PrefixSegment{prefixSegment("system", 10), prefixSegment("workflow", 7), prefixSegment("task-b", 4)}
	cold, err := observer.Observe("provider/model/tenant-a", first)
	if err != nil {
		t.Fatal(err)
	}
	if cold.LogicalHit || cold.ReusablePrefixTokens != 0 || cold.PhysicalCacheObserved || cold.PreviousFingerprint != "" {
		t.Fatalf("cold observation = %#v", cold)
	}
	warm, err := observer.Observe("provider/model/tenant-a", second)
	if err != nil {
		t.Fatal(err)
	}
	if !warm.LogicalHit || warm.ReusablePrefixSegments != 2 || warm.ReusablePrefixTokens != 17 ||
		warm.PromptTokens != 21 || warm.PreviousFingerprint != cold.CurrentFingerprint || warm.CurrentFingerprint == cold.CurrentFingerprint {
		t.Fatalf("warm observation = %#v", warm)
	}
	stats := observer.Stats()
	if stats.Namespaces != 1 || stats.Observations != 2 || stats.LogicalHits != 1 || stats.PromptTokens != 41 || stats.ReusablePrefixTokens != 17 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestPrefixObserverIsolatesAndEvictsNamespaces(t *testing.T) {
	observer := mustPrefixObserver(t, PrefixObserverConfig{MaxNamespaces: 2, MaxSegmentsPerPrompt: 2, MaxTokensPerObservation: 20})
	prompt := []PrefixSegment{prefixSegment("system", 5)}
	for _, namespace := range []string{"provider/model/a", "provider/model/b", "provider/model/a"} {
		if _, err := observer.Observe(namespace, prompt); err != nil {
			t.Fatal(err)
		}
	}
	added, err := observer.Observe("provider/model/c", prompt)
	if err != nil || !added.NamespaceEvicted {
		t.Fatalf("capacity observation = (%#v, %v)", added, err)
	}
	reintroduced, err := observer.Observe("provider/model/b", prompt)
	if err != nil || reintroduced.LogicalHit || !reintroduced.NamespaceEvicted {
		t.Fatalf("evicted namespace observation = (%#v, %v)", reintroduced, err)
	}
	if stats := observer.Stats(); stats.Namespaces != 2 || stats.Evictions != 2 || stats.Observations != 5 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestPrefixObserverRejectsInvalidInputWithoutMutation(t *testing.T) {
	observer := mustPrefixObserver(t, PrefixObserverConfig{MaxNamespaces: 1, MaxSegmentsPerPrompt: 1, MaxTokensPerObservation: 5})
	invalid := [][]PrefixSegment{
		nil,
		{prefixSegment("one", 6)},
		{{ContentSHA256: "not-a-digest", TokenCount: 1}},
	}
	for _, segments := range invalid {
		if _, err := observer.Observe("valid", segments); err == nil {
			t.Fatalf("invalid segments accepted: %#v", segments)
		}
	}
	if _, err := observer.Observe("contains space", []PrefixSegment{prefixSegment("one", 1)}); err == nil {
		t.Fatal("invalid namespace was accepted")
	}
	if stats := observer.Stats(); stats != (PrefixObserverStats{}) {
		t.Fatalf("invalid input mutated stats: %#v", stats)
	}
}

func TestPrefixObserverSerializesConcurrentUpdates(t *testing.T) {
	observer := mustPrefixObserver(t, PrefixObserverConfig{MaxNamespaces: 1, MaxSegmentsPerPrompt: 1, MaxTokensPerObservation: 5})
	prompt := []PrefixSegment{prefixSegment("shared", 5)}
	const workers = 32
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := observer.Observe("provider/model/shared", prompt)
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	stats := observer.Stats()
	if stats.Observations != workers || stats.LogicalHits != workers-1 || stats.ReusablePrefixTokens != (workers-1)*5 {
		t.Fatalf("concurrent stats = %#v", stats)
	}
}

func mustPrefixObserver(t *testing.T, config PrefixObserverConfig) *PrefixObserver {
	t.Helper()
	observer, err := NewPrefixObserver(config)
	if err != nil {
		t.Fatal(err)
	}
	return observer
}

func prefixSegment(value string, tokens int) PrefixSegment {
	digest := sha256.Sum256([]byte(value))
	return PrefixSegment{ContentSHA256: hex.EncodeToString(digest[:]), TokenCount: tokens}
}
