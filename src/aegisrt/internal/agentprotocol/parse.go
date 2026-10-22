package agentprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

var envelopeFields = map[string]struct{}{"version": {}, "artifacts": {}}
var artifactFields = map[string]struct{}{
	"name": {}, "content": {}, "visibility": {}, "recipients": {}, "pin": {}, "cold": {},
}

// Parse validates one complete Agent stdout envelope. Empty output means that
// the Agent produced no transferable artifacts.
func Parse(output []byte, truncated bool) (Envelope, error) {
	if truncated {
		return Envelope{}, errors.Join(ErrInvalidOutput, ErrOutputTruncated)
	}
	if len(output) > MaxOutputBytes {
		return Envelope{}, errors.Join(ErrInvalidOutput, ErrOutputTooLarge)
	}
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return Envelope{}, nil
	}
	if !utf8.Valid(output) {
		return Envelope{}, errors.Join(ErrInvalidOutput, ErrInvalidJSON)
	}
	root, err := decodeStrictObject(trimmed, envelopeFields)
	if err != nil {
		return Envelope{}, errors.Join(ErrInvalidOutput, ErrInvalidJSON, err)
	}
	version, ok := root["version"]
	if !ok || decodeString(version) != VersionV1 {
		return Envelope{}, errors.Join(ErrInvalidOutput, ErrUnsupportedVersion)
	}
	rawArtifacts, ok := root["artifacts"]
	if !ok || bytes.Equal(bytes.TrimSpace(rawArtifacts), []byte("null")) {
		return Envelope{}, errors.Join(ErrInvalidOutput, ErrInvalidEnvelope)
	}
	var items []json.RawMessage
	if err := decodeOne(rawArtifacts, &items); err != nil {
		return Envelope{}, errors.Join(ErrInvalidOutput, ErrInvalidEnvelope, err)
	}
	if len(items) > MaxArtifacts {
		return Envelope{}, errors.Join(ErrInvalidOutput, ErrInvalidEnvelope, ErrTooManyArtifacts)
	}
	envelope := Envelope{Version: VersionV1, Artifacts: make([]Artifact, 0, len(items))}
	for index, item := range items {
		artifact, artifactErr := parseArtifact(item)
		if artifactErr != nil {
			return Envelope{}, fmt.Errorf("artifact[%d]: %w", index, artifactErr)
		}
		envelope.Artifacts = append(envelope.Artifacts, artifact)
	}
	if err := ValidateArtifacts(envelope.Artifacts); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func parseArtifact(raw json.RawMessage) (Artifact, error) {
	fields, err := decodeStrictObject(raw, artifactFields)
	if err != nil {
		return Artifact{}, errors.Join(ErrInvalidOutput, ErrInvalidJSON, err)
	}
	artifact := Artifact{}
	if value, ok := fields["name"]; ok {
		_ = decodeOne(value, &artifact.Name)
	}
	if value, ok := fields["content"]; ok {
		_ = decodeOne(value, &artifact.Content)
	}
	if value, ok := fields["visibility"]; ok {
		_ = decodeOne(value, &artifact.Visibility)
	}
	if value, ok := fields["recipients"]; ok {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || decodeOne(value, &artifact.Recipients) != nil {
			return Artifact{}, errors.Join(ErrInvalidOutput, ErrInvalidArtifact, ErrInvalidRecipients)
		}
	}
	if value, ok := fields["pin"]; ok && decodeOne(value, &artifact.Pin) != nil {
		return Artifact{}, errors.Join(ErrInvalidOutput, ErrInvalidArtifact)
	}
	if value, ok := fields["cold"]; ok && decodeOne(value, &artifact.Cold) != nil {
		return Artifact{}, errors.Join(ErrInvalidOutput, ErrInvalidArtifact)
	}
	if err := validateArtifact(artifact); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

// ValidateEnvelope validates an in-memory envelope without JSON re-encoding.
func ValidateEnvelope(envelope Envelope) error {
	if envelope.Version != VersionV1 {
		return errors.Join(ErrInvalidOutput, ErrUnsupportedVersion)
	}
	if envelope.Artifacts == nil {
		return errors.Join(ErrInvalidOutput, ErrInvalidEnvelope)
	}
	return ValidateArtifacts(envelope.Artifacts)
}

// ValidateArtifacts applies protocol bounds to already-decoded artifacts.
func ValidateArtifacts(artifacts []Artifact) error {
	if len(artifacts) > MaxArtifacts {
		return errors.Join(ErrInvalidOutput, ErrInvalidEnvelope, ErrTooManyArtifacts)
	}
	seen := make(map[string]struct{}, len(artifacts))
	type retention struct{ pin, cold bool }
	retentionByPage := make(map[string]retention, len(artifacts))
	logicalBytes := len(VersionV1)
	for index, artifact := range artifacts {
		if err := validateArtifact(artifact); err != nil {
			return fmt.Errorf("artifact[%d]: %w", index, err)
		}
		if _, exists := seen[artifact.Name]; exists {
			return errors.Join(ErrInvalidOutput, ErrInvalidEnvelope, ErrDuplicateArtifactName)
		}
		seen[artifact.Name] = struct{}{}
		pageKey := artifact.Visibility + "\x00" + artifact.Content
		flags := retention{pin: artifact.Pin, cold: artifact.Cold}
		if existing, exists := retentionByPage[pageKey]; exists && existing != flags {
			return errors.Join(ErrInvalidOutput, ErrInvalidEnvelope, ErrConflictingRetention)
		}
		retentionByPage[pageKey] = flags
		logicalBytes += len(artifact.Name) + len(artifact.Content) + len(artifact.Visibility)
		for _, recipient := range artifact.Recipients {
			logicalBytes += len(recipient)
		}
		if logicalBytes > MaxOutputBytes {
			return errors.Join(ErrInvalidOutput, ErrOutputTooLarge)
		}
	}
	return nil
}

func validateArtifact(artifact Artifact) error {
	if !validName(artifact.Name) {
		return errors.Join(ErrInvalidOutput, ErrInvalidArtifact, ErrInvalidName)
	}
	if !validContent(artifact.Content) {
		return errors.Join(ErrInvalidOutput, ErrInvalidArtifact, ErrInvalidContent)
	}
	if artifact.Visibility != "workflow" && artifact.Visibility != "agent" {
		return errors.Join(ErrInvalidOutput, ErrInvalidArtifact, ErrInvalidVisibility)
	}
	if artifact.Recipients != nil && (len(artifact.Recipients) == 0 || !validRecipients(artifact.Recipients)) ||
		artifact.Visibility == "agent" && artifact.Recipients != nil {
		return errors.Join(ErrInvalidOutput, ErrInvalidArtifact, ErrInvalidRecipients)
	}
	if artifact.Pin && artifact.Cold {
		return errors.Join(ErrInvalidOutput, ErrInvalidArtifact, ErrConflictingRetention)
	}
	return nil
}

func decodeStrictObject(raw []byte, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("value must be an object")
	}
	result := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		token, tokenErr := decoder.Token()
		if tokenErr != nil {
			return nil, tokenErr
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("object key must be a string")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		if _, exists := allowed[name]; !exists {
			return nil, fmt.Errorf("unknown field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		result[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeOne(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeString(raw []byte) string {
	var value string
	_ = decodeOne(raw, &value)
	return value
}

func validName(value string) bool {
	if len(value) == 0 || len(value) > MaxNameBytes || !asciiAlphaNumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if !asciiAlphaNumeric(char) && char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func validContent(value string) bool {
	return len(value) <= MaxContentBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00') && strings.TrimSpace(value) != ""
}

func validRecipients(values []string) bool {
	if len(values) > MaxRecipients {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validIdentifier(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > MaxIdentifierBytes || !asciiAlphaNumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if !asciiAlphaNumeric(char) && char != '-' && char != '_' && char != '.' && char != ':' {
			return false
		}
	}
	return true
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}
