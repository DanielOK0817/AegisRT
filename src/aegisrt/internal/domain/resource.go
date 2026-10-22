package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrInsufficientResource  = errors.New("insufficient resources")
	ErrResourceOverflow      = errors.New("resource arithmetic overflow")
	ErrInvalidResourceVector = errors.New("invalid resource vector")
)

// ResourceVector describes both operating-system and AI-specific resources.
// CPU is measured in millicores and memory in MiB.
type ResourceVector struct {
	CPUMilli    int64            `json:"cpu_milli"`
	MemoryMiB   int64            `json:"memory_mib"`
	LLMSlots    int64            `json:"llm_slots"`
	TokenBudget int64            `json:"token_budget"`
	ToolSlots   map[string]int64 `json:"tool_slots,omitempty"`
}

func (r ResourceVector) Validate() error {
	values := []struct {
		name  string
		value int64
	}{
		{"cpu_milli", r.CPUMilli},
		{"memory_mib", r.MemoryMiB},
		{"llm_slots", r.LLMSlots},
		{"token_budget", r.TokenBudget},
	}
	for _, item := range values {
		if item.value < 0 {
			return fmt.Errorf("%s must be non-negative", item.name)
		}
	}
	for name, value := range r.ToolSlots {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("tool slot name %q is invalid", name)
		}
		if err := validateIdentifier("tool slot name", name); err != nil {
			return err
		}
		if value < 0 {
			return fmt.Errorf("tool slot %q must be non-negative", name)
		}
	}
	return nil
}

// Clone returns a copy safe for use across API boundaries.
func (r ResourceVector) Clone() ResourceVector {
	result := r
	if r.ToolSlots != nil {
		result.ToolSlots = make(map[string]int64, len(r.ToolSlots))
		for name, value := range r.ToolSlots {
			result.ToolSlots[name] = value
		}
	}
	return result
}

// Add returns the component-wise sum. Callers handling untrusted or
// potentially extreme values should use AddChecked to detect int64 overflow.
func (r ResourceVector) Add(other ResourceVector) ResourceVector {
	result := ResourceVector{
		CPUMilli:    r.CPUMilli + other.CPUMilli,
		MemoryMiB:   r.MemoryMiB + other.MemoryMiB,
		LLMSlots:    r.LLMSlots + other.LLMSlots,
		TokenBudget: r.TokenBudget + other.TokenBudget,
		ToolSlots:   cloneToolSlots(r.ToolSlots),
	}
	for name, value := range other.ToolSlots {
		if result.ToolSlots == nil {
			result.ToolSlots = make(map[string]int64)
		}
		result.ToolSlots[name] += value
	}
	return result
}

// AddChecked validates both operands and detects component overflow.
func (r ResourceVector) AddChecked(other ResourceVector) (ResourceVector, error) {
	if err := r.Validate(); err != nil {
		return ResourceVector{}, fmt.Errorf("%w: left operand: %v", ErrInvalidResourceVector, err)
	}
	if err := other.Validate(); err != nil {
		return ResourceVector{}, fmt.Errorf("%w: right operand: %v", ErrInvalidResourceVector, err)
	}
	result := ResourceVector{ToolSlots: cloneToolSlots(r.ToolSlots)}
	var err error
	if result.CPUMilli, err = addNonNegative(r.CPUMilli, other.CPUMilli); err != nil {
		return ResourceVector{}, fmt.Errorf("%w: cpu_milli", err)
	}
	if result.MemoryMiB, err = addNonNegative(r.MemoryMiB, other.MemoryMiB); err != nil {
		return ResourceVector{}, fmt.Errorf("%w: memory_mib", err)
	}
	if result.LLMSlots, err = addNonNegative(r.LLMSlots, other.LLMSlots); err != nil {
		return ResourceVector{}, fmt.Errorf("%w: llm_slots", err)
	}
	if result.TokenBudget, err = addNonNegative(r.TokenBudget, other.TokenBudget); err != nil {
		return ResourceVector{}, fmt.Errorf("%w: token_budget", err)
	}
	for name, value := range other.ToolSlots {
		if result.ToolSlots == nil {
			result.ToolSlots = make(map[string]int64)
		}
		result.ToolSlots[name], err = addNonNegative(result.ToolSlots[name], value)
		if err != nil {
			return ResourceVector{}, fmt.Errorf("%w: tool slot %q", err, name)
		}
	}
	return result, nil
}

// Sub returns the component-wise difference, or ErrInsufficientResource when
// any requested dimension exceeds the receiver.
func (r ResourceVector) Sub(other ResourceVector) (ResourceVector, error) {
	if err := r.Validate(); err != nil {
		return ResourceVector{}, fmt.Errorf("%w: minuend: %v", ErrInvalidResourceVector, err)
	}
	if err := other.Validate(); err != nil {
		return ResourceVector{}, fmt.Errorf("%w: subtrahend: %v", ErrInvalidResourceVector, err)
	}
	if !other.FitsWithin(r) {
		return ResourceVector{}, ErrInsufficientResource
	}
	result := ResourceVector{
		CPUMilli:    r.CPUMilli - other.CPUMilli,
		MemoryMiB:   r.MemoryMiB - other.MemoryMiB,
		LLMSlots:    r.LLMSlots - other.LLMSlots,
		TokenBudget: r.TokenBudget - other.TokenBudget,
		ToolSlots:   cloneToolSlots(r.ToolSlots),
	}
	for name, value := range other.ToolSlots {
		result.ToolSlots[name] -= value
	}
	return result, nil
}

// FitsWithin reports whether every requested dimension fits the capacity.
func (r ResourceVector) FitsWithin(capacity ResourceVector) bool {
	if r.CPUMilli > capacity.CPUMilli || r.MemoryMiB > capacity.MemoryMiB ||
		r.LLMSlots > capacity.LLMSlots || r.TokenBudget > capacity.TokenBudget {
		return false
	}
	for name, value := range r.ToolSlots {
		if value > capacity.ToolSlots[name] {
			return false
		}
	}
	return true
}

// DominantShare returns the largest fraction consumed in any dimension.
// A positive request against zero capacity has an infinite share.
func (r ResourceVector) DominantShare(capacity ResourceVector) float64 {
	dominant := 0.0
	shares := []float64{
		resourceRatio(r.CPUMilli, capacity.CPUMilli),
		resourceRatio(r.MemoryMiB, capacity.MemoryMiB),
		resourceRatio(r.LLMSlots, capacity.LLMSlots),
		resourceRatio(r.TokenBudget, capacity.TokenBudget),
	}
	for name, value := range r.ToolSlots {
		shares = append(shares, resourceRatio(value, capacity.ToolSlots[name]))
	}
	for _, share := range shares {
		if share > dominant {
			dominant = share
		}
	}
	return dominant
}

func (r ResourceVector) IsZero() bool {
	if r.CPUMilli != 0 || r.MemoryMiB != 0 || r.LLMSlots != 0 || r.TokenBudget != 0 {
		return false
	}
	for _, value := range r.ToolSlots {
		if value != 0 {
			return false
		}
	}
	return true
}

func cloneToolSlots(source map[string]int64) map[string]int64 {
	if source == nil {
		return nil
	}
	result := make(map[string]int64, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func addNonNegative(left, right int64) (int64, error) {
	if left > math.MaxInt64-right {
		return 0, ErrResourceOverflow
	}
	return left + right, nil
}

func resourceRatio(value, capacity int64) float64 {
	if value == 0 {
		return 0
	}
	if capacity == 0 {
		return math.Inf(1)
	}
	return float64(value) / float64(capacity)
}
