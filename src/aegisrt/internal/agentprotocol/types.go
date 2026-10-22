// Package agentprotocol defines the bounded protocol emitted by local Agents.
package agentprotocol

import "errors"

const (
	VersionV1          = "aegisrt.agent-output/v1"
	MaxOutputBytes     = 256 << 10
	MaxArtifacts       = 32
	MaxNameBytes       = 64
	MaxContentBytes    = 64 << 10
	MaxRecipients      = 32
	MaxIdentifierBytes = 128
)

var (
	ErrInvalidOutput         = errors.New("invalid agent output")
	ErrOutputTruncated       = errors.New("agent output was truncated")
	ErrOutputTooLarge        = errors.New("agent output exceeds size limit")
	ErrInvalidJSON           = errors.New("invalid agent output JSON")
	ErrUnsupportedVersion    = errors.New("unsupported agent output version")
	ErrInvalidEnvelope       = errors.New("invalid agent output envelope")
	ErrTooManyArtifacts      = errors.New("too many agent output artifacts")
	ErrDuplicateArtifactName = errors.New("duplicate agent output artifact name")
	ErrInvalidArtifact       = errors.New("invalid agent output artifact")
	ErrInvalidName           = errors.New("invalid artifact name")
	ErrInvalidContent        = errors.New("invalid artifact content")
	ErrInvalidVisibility     = errors.New("invalid artifact visibility")
	ErrInvalidRecipients     = errors.New("invalid artifact recipients")
	ErrConflictingRetention  = errors.New("artifact cannot be both pinned and cold")
)

type Envelope struct {
	Version   string     `json:"version"`
	Artifacts []Artifact `json:"artifacts"`
}

type Artifact struct {
	Name       string   `json:"name"`
	Content    string   `json:"content"`
	Visibility string   `json:"visibility"`
	Recipients []string `json:"recipients,omitempty"`
	Pin        bool     `json:"pin,omitempty"`
	Cold       bool     `json:"cold,omitempty"`
}
