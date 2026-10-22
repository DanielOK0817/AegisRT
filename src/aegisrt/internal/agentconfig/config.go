// Package agentconfig loads the daemon's immutable, local Agent registry.
package agentconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"aegisrt/internal/domain"
)

const (
	DefaultMaxFileBytes int64 = 1 << 20
	MaximumMaxFileBytes int64 = 16 << 20
)

var (
	ErrInvalidOptions       = errors.New("invalid agent config options")
	ErrInvalidConfig        = errors.New("invalid agent config")
	ErrFileTooLarge         = errors.New("agent config exceeds size limit")
	ErrNoAgents             = errors.New("agent config must contain at least one agent")
	ErrDuplicateAgent       = errors.New("duplicate agent")
	ErrUnsupportedRuntime   = errors.New("unsupported agent runtime")
	ErrInvalidEntrypoint    = errors.New("invalid agent entrypoint")
	ErrOutsideAgentRoot     = errors.New("agent entrypoint is outside agent root")
	ErrUnsafePermissions    = errors.New("unsafe file permissions")
	ErrUnsafeResourcePolicy = errors.New("unsafe agent resource policy")
)

type Options struct {
	MaxFileBytes      int64
	AgentRoot         string
	StrictPermissions bool
}

// Config owns an immutable registry snapshot. Agents returns deep copies so a
// caller cannot mutate later reads from the loaded configuration.
type Config struct {
	agents []domain.AgentSpec
}

type document struct {
	Agents []agentRecord `json:"agents"`
}

type agentRecord struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Runtime          string                `json:"runtime"`
	Entrypoint       string                `json:"entrypoint"`
	Args             []string              `json:"args,omitempty"`
	Capabilities     []string              `json:"capabilities,omitempty"`
	Resources        domain.ResourceVector `json:"resources"`
	MaxConcurrency   int                   `json:"max_concurrency"`
	ExecutionTimeout string                `json:"execution_timeout"`
}

type normalizedOptions struct {
	maxFileBytes      int64
	agentRoot         string
	strictPermissions bool
}

func Load(path string, options Options) (*Config, error) {
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	input, err := readConfigFile(path, normalized)
	if err != nil {
		return nil, err
	}
	var parsed document
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("%w: decode JSON: %v", ErrInvalidConfig, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("%w: trailing JSON: %v", ErrInvalidConfig, err)
	}
	if len(parsed.Agents) == 0 {
		return nil, ErrNoAgents
	}

	agents := make([]domain.AgentSpec, 0, len(parsed.Agents))
	seen := make(map[string]struct{}, len(parsed.Agents))
	for index, record := range parsed.Agents {
		agent, err := buildAgent(record, normalized)
		if err != nil {
			return nil, fmt.Errorf("%w: agents[%d]: %w", ErrInvalidConfig, index, err)
		}
		if _, exists := seen[agent.ID]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateAgent, agent.ID)
		}
		seen[agent.ID] = struct{}{}
		agents = append(agents, cloneAgent(agent))
	}
	return &Config{agents: agents}, nil
}

func (c *Config) Agents() []domain.AgentSpec {
	if c == nil {
		return nil
	}
	result := make([]domain.AgentSpec, len(c.agents))
	for index, agent := range c.agents {
		result[index] = cloneAgent(agent)
	}
	return result
}

func normalizeOptions(options Options) (normalizedOptions, error) {
	if options.MaxFileBytes < 0 {
		return normalizedOptions{}, fmt.Errorf("%w: max file bytes must not be negative", ErrInvalidOptions)
	}
	if options.MaxFileBytes > MaximumMaxFileBytes {
		return normalizedOptions{}, fmt.Errorf("%w: max file bytes must not exceed %d", ErrInvalidOptions, MaximumMaxFileBytes)
	}
	maxFileBytes := options.MaxFileBytes
	if maxFileBytes == 0 {
		maxFileBytes = DefaultMaxFileBytes
	}
	result := normalizedOptions{
		maxFileBytes:      maxFileBytes,
		strictPermissions: options.StrictPermissions,
	}
	if options.AgentRoot == "" {
		return result, nil
	}
	if strings.TrimSpace(options.AgentRoot) != options.AgentRoot {
		return normalizedOptions{}, fmt.Errorf("%w: agent root contains surrounding whitespace", ErrInvalidOptions)
	}
	absolute, err := filepath.Abs(options.AgentRoot)
	if err != nil {
		return normalizedOptions{}, fmt.Errorf("%w: resolve agent root: %v", ErrInvalidOptions, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return normalizedOptions{}, fmt.Errorf("%w: resolve agent root symlinks: %v", ErrInvalidOptions, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return normalizedOptions{}, fmt.Errorf("%w: stat agent root: %v", ErrInvalidOptions, err)
	}
	if !info.IsDir() {
		return normalizedOptions{}, fmt.Errorf("%w: agent root is not a directory", ErrInvalidOptions)
	}
	result.agentRoot = filepath.Clean(resolved)
	return result, nil
}

func readConfigFile(path string, options normalizedOptions) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: config path is required", ErrInvalidConfig)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open agent config: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat agent config: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: config is not a regular file", ErrInvalidConfig)
	}
	if options.strictPermissions {
		if err := checkWritePermissions(info, "config"); err != nil {
			return nil, err
		}
	}
	if info.Size() > options.maxFileBytes {
		return nil, fmt.Errorf("%w: got %d bytes, maximum %d", ErrFileTooLarge, info.Size(), options.maxFileBytes)
	}
	content, err := io.ReadAll(io.LimitReader(file, options.maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read agent config: %w", err)
	}
	if int64(len(content)) > options.maxFileBytes {
		return nil, fmt.Errorf("%w: maximum %d", ErrFileTooLarge, options.maxFileBytes)
	}
	return content, nil
}

func buildAgent(record agentRecord, options normalizedOptions) (domain.AgentSpec, error) {
	if record.Runtime != "process" {
		return domain.AgentSpec{}, fmt.Errorf("%w: %q", ErrUnsupportedRuntime, record.Runtime)
	}
	duration, err := time.ParseDuration(record.ExecutionTimeout)
	if err != nil {
		return domain.AgentSpec{}, fmt.Errorf("execution_timeout: %w", err)
	}
	entrypoint, err := resolveEntrypoint(record.Entrypoint, options)
	if err != nil {
		return domain.AgentSpec{}, err
	}
	agent := domain.AgentSpec{
		ID:               record.ID,
		Name:             record.Name,
		Runtime:          record.Runtime,
		Entrypoint:       entrypoint,
		Args:             append([]string(nil), record.Args...),
		Capabilities:     append([]string(nil), record.Capabilities...),
		Resources:        record.Resources.Clone(),
		MaxConcurrency:   record.MaxConcurrency,
		ExecutionTimeout: duration,
	}
	if err := agent.Validate(); err != nil {
		return domain.AgentSpec{}, err
	}
	if agent.Resources.CPUMilli <= 0 || agent.Resources.MemoryMiB <= 0 {
		return domain.AgentSpec{}, fmt.Errorf("%w: cpu_milli and memory_mib must be positive", ErrUnsafeResourcePolicy)
	}
	return agent, nil
}

func resolveEntrypoint(path string, options normalizedOptions) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be absolute", ErrInvalidEntrypoint)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("%w: resolve symlinks: %v", ErrInvalidEntrypoint, err)
	}
	resolved = filepath.Clean(resolved)
	if options.agentRoot != "" {
		relative, err := filepath.Rel(options.agentRoot, resolved)
		if err != nil || filepath.IsAbs(relative) || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%w: %q", ErrOutsideAgentRoot, path)
		}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: stat: %v", ErrInvalidEntrypoint, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: path is not a regular file", ErrInvalidEntrypoint)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%w: file is not executable", ErrInvalidEntrypoint)
	}
	if options.strictPermissions {
		if err := checkWritePermissions(info, "entrypoint"); err != nil {
			return "", err
		}
	}
	return resolved, nil
}

func checkWritePermissions(info os.FileInfo, kind string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: %s is writable by group or others", ErrUnsafePermissions, kind)
	}
	return nil
}

func cloneAgent(agent domain.AgentSpec) domain.AgentSpec {
	result := agent
	result.Args = append([]string(nil), agent.Args...)
	result.Capabilities = append([]string(nil), agent.Capabilities...)
	result.Resources = agent.Resources.Clone()
	return result
}
