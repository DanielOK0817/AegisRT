package agentprotocol

import (
	"errors"
	"strings"
	"testing"
)

func TestParseEmptyOutput(t *testing.T) {
	for _, output := range [][]byte{nil, {}, []byte(" \r\n\t")} {
		envelope, err := Parse(output, false)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", output, err)
		}
		if envelope.Version != "" || len(envelope.Artifacts) != 0 {
			t.Fatalf("Parse(%q) = %+v, want empty envelope", output, envelope)
		}
	}
}

func TestParseValidEnvelope(t *testing.T) {
	output := []byte(`{
		"version":"aegisrt.agent-output/v1",
		"artifacts":[
			{"name":"scan.result-1","content":"安全结果","visibility":"workflow","recipients":["agent-1","team:review"],"pin":true},
			{"name":"private","content":"details","visibility":"agent","cold":true}
		]
	}`)
	envelope, err := Parse(output, false)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Version != VersionV1 || len(envelope.Artifacts) != 2 {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
	first := envelope.Artifacts[0]
	if first.Name != "scan.result-1" || first.Content != "安全结果" ||
		first.Visibility != "workflow" || !first.Pin || first.Cold || len(first.Recipients) != 2 {
		t.Fatalf("unexpected first artifact: %+v", first)
	}
	second := envelope.Artifacts[1]
	if second.Visibility != "agent" || !second.Cold || len(second.Recipients) != 0 {
		t.Fatalf("unexpected second artifact: %+v", second)
	}
}

func TestParseRejectsTruncatedOutputBeforeOtherChecks(t *testing.T) {
	for _, output := range [][]byte{nil, []byte("not JSON"), make([]byte, MaxOutputBytes+1)} {
		_, err := Parse(output, true)
		if !errors.Is(err, ErrOutputTruncated) {
			t.Fatalf("Parse(truncated %d bytes) error = %v, want ErrOutputTruncated", len(output), err)
		}
	}
}

func TestParseRejectsOversizedNonEmptyOutput(t *testing.T) {
	_, err := Parse([]byte(strings.Repeat("x", MaxOutputBytes+1)), false)
	if !errors.Is(err, ErrOutputTooLarge) || !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("error = %v, want ErrOutputTooLarge and ErrInvalidOutput", err)
	}
}

func TestParseRejectsOversizedWhitespaceOutput(t *testing.T) {
	_, err := Parse([]byte(strings.Repeat(" ", MaxOutputBytes+1)), false)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("error = %v, want ErrOutputTooLarge", err)
	}
}

func TestParseRejectsMalformedOrAmbiguousJSON(t *testing.T) {
	tests := []string{
		`{"version":"aegisrt.agent-output/v1","artifacts":[]`,
		`[]`,
		`{"version":"aegisrt.agent-output/v1","artifacts":[]} {}`,
		`{"version":"aegisrt.agent-output/v1","version":"aegisrt.agent-output/v1","artifacts":[]}`,
		`{"version":"aegisrt.agent-output/v1","artifacts":[],"extra":true}`,
		"{\"version\":\"aegisrt.agent-output/v1\",\"artifacts\":[{\"name\":\"a\",\"content\":\"\xff\",\"visibility\":\"workflow\"}]}",
	}
	for _, output := range tests {
		_, err := Parse([]byte(output), false)
		if !errors.Is(err, ErrInvalidJSON) || !errors.Is(err, ErrInvalidOutput) {
			t.Errorf("Parse(%q) error = %v, want ErrInvalidJSON and ErrInvalidOutput", output, err)
		}
	}
}

func TestParseRejectsMissingFieldsAndUnsupportedVersion(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   error
	}{
		{name: "missing version", output: `{"artifacts":[]}`, want: ErrUnsupportedVersion},
		{name: "null version", output: `{"version":null,"artifacts":[]}`, want: ErrUnsupportedVersion},
		{name: "wrong version", output: `{"version":"v2","artifacts":[]}`, want: ErrUnsupportedVersion},
		{name: "missing artifacts", output: `{"version":"aegisrt.agent-output/v1"}`, want: ErrInvalidEnvelope},
		{name: "null artifacts", output: `{"version":"aegisrt.agent-output/v1","artifacts":null}`, want: ErrInvalidEnvelope},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.output), false)
			if !errors.Is(err, test.want) || !errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("error = %v, want %v and ErrInvalidOutput", err, test.want)
			}
		})
	}
}

func TestParseRejectsTooManyArtifacts(t *testing.T) {
	artifacts := strings.Repeat(`{"name":"a","content":"x","visibility":"workflow"},`, MaxArtifacts)
	output := `{"version":"aegisrt.agent-output/v1","artifacts":[` + artifacts +
		`{"name":"last","content":"x","visibility":"workflow"}]}`
	_, err := Parse([]byte(output), false)
	if !errors.Is(err, ErrTooManyArtifacts) || !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("error = %v, want ErrTooManyArtifacts and ErrInvalidEnvelope", err)
	}
}
