package contextvm

import (
	"fmt"
	"strings"
	"unicode"
)

// Counter makes token accounting deterministic and provider-independent.
type Counter interface {
	Count(text string) (int, error)
}

// NamedCounter exposes the stable identity that must be bound to persisted
// token counts and experiment evidence.
type NamedCounter interface {
	Counter
	Name() string
}

// TokenizerCountFunc adapts a provider or model tokenizer without coupling
// ContextVM to one SDK.
type TokenizerCountFunc func(text string) (int, error)

type TokenizerLimits struct {
	MaxInputBytes int
	MaxTokens     int
}

// TokenizerAdapter validates the boundary around a provider tokenizer. The
// name should include the provider/model/encoding revision used by callers.
type TokenizerAdapter struct {
	name   string
	count  TokenizerCountFunc
	limits TokenizerLimits
}

func NewTokenizerAdapter(name string, count TokenizerCountFunc, limits TokenizerLimits) (*TokenizerAdapter, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("tokenizer name is required")
	}
	if count == nil {
		return nil, fmt.Errorf("tokenizer count function is required")
	}
	if limits.MaxInputBytes <= 0 || limits.MaxTokens <= 0 {
		return nil, fmt.Errorf("positive tokenizer limits are required")
	}
	return &TokenizerAdapter{name: name, count: count, limits: limits}, nil
}

func (a *TokenizerAdapter) Name() string { return a.name }

func (a *TokenizerAdapter) Count(text string) (int, error) {
	if len(text) > a.limits.MaxInputBytes {
		return 0, fmt.Errorf("tokenizer input exceeds %d bytes", a.limits.MaxInputBytes)
	}
	tokens, err := a.count(text)
	if err != nil {
		return 0, fmt.Errorf("tokenizer %q: %w", a.name, err)
	}
	if tokens < 0 || tokens > a.limits.MaxTokens {
		return 0, fmt.Errorf("tokenizer %q returned invalid count %d", a.name, tokens)
	}
	return tokens, nil
}

// WhitespaceCounter is a deterministic standard-library counter intended for
// tests, local simulation, and providers without a tokenizer adapter.
type WhitespaceCounter struct{}

func (WhitespaceCounter) Count(text string) (int, error) {
	return len(strings.Fields(text)), nil
}

func (WhitespaceCounter) Name() string { return "whitespace-v1" }

// UnicodeSegmentCounter is a deterministic reference counter for portable
// tests. It is not claimed to match any provider tokenizer: Latin words are
// grouped, Han characters and non-space symbols count individually.
type UnicodeSegmentCounter struct{}

func (UnicodeSegmentCounter) Name() string { return "unicode-segment-v1" }

func (UnicodeSegmentCounter) Count(text string) (int, error) {
	tokens, inWord := 0, false
	for _, value := range text {
		switch {
		case unicode.IsSpace(value):
			inWord = false
		case unicode.In(value, unicode.Han):
			tokens++
			inWord = false
		case unicode.IsLetter(value) || unicode.IsDigit(value) || unicode.IsMark(value) || value == '_':
			if !inWord {
				tokens++
				inWord = true
			}
		default:
			tokens++
			inWord = false
		}
	}
	return tokens, nil
}
