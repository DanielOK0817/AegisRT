package contextvm

import (
	"errors"
	"testing"
)

func TestTokenizerAdapterValidatesProviderBoundary(t *testing.T) {
	providerErr := errors.New("provider failure")
	tests := []struct {
		name    string
		text    string
		count   TokenizerCountFunc
		limits  TokenizerLimits
		want    int
		wantErr bool
	}{
		{name: "success", text: "hello", count: func(string) (int, error) { return 2, nil }, limits: TokenizerLimits{MaxInputBytes: 5, MaxTokens: 2}, want: 2},
		{name: "input limit", text: "hello!", count: func(string) (int, error) { return 2, nil }, limits: TokenizerLimits{MaxInputBytes: 5, MaxTokens: 2}, wantErr: true},
		{name: "token limit", text: "hello", count: func(string) (int, error) { return 3, nil }, limits: TokenizerLimits{MaxInputBytes: 5, MaxTokens: 2}, wantErr: true},
		{name: "negative", text: "hello", count: func(string) (int, error) { return -1, nil }, limits: TokenizerLimits{MaxInputBytes: 5, MaxTokens: 2}, wantErr: true},
		{name: "provider error", text: "hello", count: func(string) (int, error) { return 0, providerErr }, limits: TokenizerLimits{MaxInputBytes: 5, MaxTokens: 2}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := NewTokenizerAdapter("provider/model/encoding-v1", test.count, test.limits)
			if err != nil {
				t.Fatal(err)
			}
			got, err := adapter.Count(test.text)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("Count() = (%d, %v), want (%d, error=%v)", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestTokenizerAdapterRejectsInvalidConfiguration(t *testing.T) {
	valid := func(string) (int, error) { return 0, nil }
	if _, err := NewTokenizerAdapter("", valid, TokenizerLimits{MaxInputBytes: 1, MaxTokens: 1}); err == nil {
		t.Fatal("empty identity was accepted")
	}
	if _, err := NewTokenizerAdapter("valid", nil, TokenizerLimits{MaxInputBytes: 1, MaxTokens: 1}); err == nil {
		t.Fatal("nil tokenizer was accepted")
	}
	if _, err := NewTokenizerAdapter("valid", valid, TokenizerLimits{}); err == nil {
		t.Fatal("zero limits were accepted")
	}
}

func TestUnicodeSegmentCounterIsDeterministicAcrossScripts(t *testing.T) {
	counter := UnicodeSegmentCounter{}
	got, err := counter.Count("alpha_beta 42, 上下文! café")
	if err != nil {
		t.Fatal(err)
	}
	if got != 8 {
		t.Fatalf("Count() = %d, want 8", got)
	}
	if counter.Name() != "unicode-segment-v1" || (WhitespaceCounter{}).Name() != "whitespace-v1" {
		t.Fatal("counter identity is not stable")
	}
}
