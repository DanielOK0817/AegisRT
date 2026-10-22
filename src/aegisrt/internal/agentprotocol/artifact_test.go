package agentprotocol

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseRejectsInvalidArtifactFields(t *testing.T) {
	tests := []struct {
		name     string
		artifact string
		want     error
	}{
		{name: "unknown field", artifact: `{"name":"a","content":"x","visibility":"workflow","path":"secret"}`, want: ErrInvalidJSON},
		{name: "missing name", artifact: `{"content":"x","visibility":"workflow"}`, want: ErrInvalidName},
		{name: "empty name", artifact: `{"name":"","content":"x","visibility":"workflow"}`, want: ErrInvalidName},
		{name: "bad first name character", artifact: `{"name":"_x","content":"x","visibility":"workflow"}`, want: ErrInvalidName},
		{name: "bad name character", artifact: `{"name":"a:b","content":"x","visibility":"workflow"}`, want: ErrInvalidName},
		{name: "long name", artifact: `{"name":"` + strings.Repeat("a", MaxNameBytes+1) + `","content":"x","visibility":"workflow"}`, want: ErrInvalidName},
		{name: "missing content", artifact: `{"name":"a","visibility":"workflow"}`, want: ErrInvalidContent},
		{name: "empty content", artifact: `{"name":"a","content":"","visibility":"workflow"}`, want: ErrInvalidContent},
		{name: "nul content", artifact: `{"name":"a","content":"x\u0000y","visibility":"workflow"}`, want: ErrInvalidContent},
		{name: "long content", artifact: `{"name":"a","content":"` + strings.Repeat("x", MaxContentBytes+1) + `","visibility":"workflow"}`, want: ErrInvalidContent},
		{name: "missing visibility", artifact: `{"name":"a","content":"x"}`, want: ErrInvalidVisibility},
		{name: "bad visibility", artifact: `{"name":"a","content":"x","visibility":"public"}`, want: ErrInvalidVisibility},
		{name: "pin and cold", artifact: `{"name":"a","content":"x","visibility":"workflow","pin":true,"cold":true}`, want: ErrConflictingRetention},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := `{"version":"aegisrt.agent-output/v1","artifacts":[` + test.artifact + `]}`
			_, err := Parse([]byte(output), false)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.want != ErrInvalidJSON && !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("error = %v, want ErrInvalidArtifact", err)
			}
		})
	}
}

func TestParseRejectsInvalidRecipients(t *testing.T) {
	tests := []struct {
		name       string
		recipients string
	}{
		{name: "too many", recipients: recipientArray(MaxRecipients + 1)},
		{name: "explicit empty", recipients: `[]`},
		{name: "duplicate", recipients: `["agent-1","agent-1"]`},
		{name: "empty", recipients: `[""]`},
		{name: "bad first character", recipients: `["-agent"]`},
		{name: "bad character", recipients: `["agent/1"]`},
		{name: "too long", recipients: `["` + strings.Repeat("a", MaxIdentifierBytes+1) + `"]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := `{"name":"a","content":"x","visibility":"workflow","recipients":` + test.recipients + `}`
			output := `{"version":"aegisrt.agent-output/v1","artifacts":[` + artifact + `]}`
			_, err := Parse([]byte(output), false)
			if !errors.Is(err, ErrInvalidRecipients) || !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("error = %v, want ErrInvalidRecipients and ErrInvalidArtifact", err)
			}
		})
	}
}

func TestParseRejectsDuplicateArtifactNames(t *testing.T) {
	output := []byte(`{"version":"aegisrt.agent-output/v1","artifacts":[` +
		`{"name":"result","content":"first","visibility":"workflow"},` +
		`{"name":"result","content":"second","visibility":"workflow"}]}`)
	_, err := Parse(output, false)
	if !errors.Is(err, ErrDuplicateArtifactName) || !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("error = %v, want ErrDuplicateArtifactName and ErrInvalidEnvelope", err)
	}
}

func TestValidateArtifactsDoesNotReencodeContent(t *testing.T) {
	artifacts := []Artifact{{
		Name: "boundary", Content: strings.Repeat("<", MaxContentBytes), Visibility: "workflow",
	}}
	if err := ValidateArtifacts(artifacts); err != nil {
		t.Fatalf("ValidateArtifacts error = %v", err)
	}
}

func TestValidateArtifactsEnforcesLogicalOutputBudget(t *testing.T) {
	artifacts := make([]Artifact, 5)
	for index := range artifacts {
		artifacts[index] = Artifact{
			Name: fmt.Sprintf("part-%d", index), Content: strings.Repeat("x", MaxContentBytes), Visibility: "workflow",
		}
	}
	if err := ValidateArtifacts(artifacts); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("ValidateArtifacts error = %v, want ErrOutputTooLarge", err)
	}
}

func TestParseRejectsConflictingRetentionForSamePage(t *testing.T) {
	output := []byte(`{"version":"aegisrt.agent-output/v1","artifacts":[` +
		`{"name":"hot","content":"same","visibility":"workflow","pin":true},` +
		`{"name":"cold","content":"same","visibility":"workflow","cold":true}]}`)
	_, err := Parse(output, false)
	if !errors.Is(err, ErrConflictingRetention) || !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("error = %v, want ErrConflictingRetention and ErrInvalidEnvelope", err)
	}
}

func TestParseRejectsRecipientsForAgentVisibility(t *testing.T) {
	output := []byte(`{"version":"aegisrt.agent-output/v1","artifacts":[{"name":"a","content":"x","visibility":"agent","recipients":["agent-2"]}]}`)
	_, err := Parse(output, false)
	if !errors.Is(err, ErrInvalidRecipients) {
		t.Fatalf("error = %v, want ErrInvalidRecipients", err)
	}
}

func TestParseAcceptsArtifactBoundaries(t *testing.T) {
	name := "a" + strings.Repeat("_", MaxNameBytes-1)
	wide := string(rune(0x754c))
	content := strings.Repeat(wide, MaxContentBytes/len(wide)) + strings.Repeat("x", MaxContentBytes%len(wide))
	recipients := recipientArray(MaxRecipients)
	artifact := `{"name":"` + name + `","content":"` + content +
		`","visibility":"workflow","recipients":` + recipients + `}`
	output := `{"version":"aegisrt.agent-output/v1","artifacts":[` + artifact + `]}`
	envelope, err := Parse([]byte(output), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope.Artifacts) != 1 || len(envelope.Artifacts[0].Content) != MaxContentBytes {
		t.Fatalf("unexpected envelope: artifacts=%d content_bytes=%d", len(envelope.Artifacts), len(envelope.Artifacts[0].Content))
	}
}

func recipientArray(count int) string {
	values := make([]string, count)
	for index := range values {
		values[index] = fmt.Sprintf(`"agent-%d"`, index)
	}
	return "[" + strings.Join(values, ",") + "]"
}
