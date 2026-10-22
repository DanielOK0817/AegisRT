package domain

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestResourceVectorValidate(t *testing.T) {
	tests := []struct {
		name    string
		vector  ResourceVector
		wantErr bool
	}{
		{name: "zero vector", vector: ResourceVector{}},
		{name: "all dimensions", vector: ResourceVector{CPUMilli: 250, MemoryMiB: 128, LLMSlots: 1, TokenBudget: 4096, ToolSlots: map[string]int64{"shell": 1}}},
		{name: "negative cpu", vector: ResourceVector{CPUMilli: -1}, wantErr: true},
		{name: "negative memory", vector: ResourceVector{MemoryMiB: -1}, wantErr: true},
		{name: "negative llm", vector: ResourceVector{LLMSlots: -1}, wantErr: true},
		{name: "negative tokens", vector: ResourceVector{TokenBudget: -1}, wantErr: true},
		{name: "negative tool", vector: ResourceVector{ToolSlots: map[string]int64{"shell": -1}}, wantErr: true},
		{name: "blank tool", vector: ResourceVector{ToolSlots: map[string]int64{" ": 1}}, wantErr: true},
		{name: "invalid tool", vector: ResourceVector{ToolSlots: map[string]int64{"shell/read": 1}}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.vector.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestResourceVectorCloneAndFitsWithin(t *testing.T) {
	original := ResourceVector{CPUMilli: 500, MemoryMiB: 256, LLMSlots: 1, TokenBudget: 1000, ToolSlots: map[string]int64{"shell": 1}}
	clone := original.Clone()
	clone.ToolSlots["shell"] = 9
	if original.ToolSlots["shell"] != 1 {
		t.Fatal("Clone shares ToolSlots with source")
	}

	tests := []struct {
		name     string
		request  ResourceVector
		capacity ResourceVector
		want     bool
	}{
		{name: "exact", request: original, capacity: original, want: true},
		{name: "smaller", request: ResourceVector{CPUMilli: 1}, capacity: original, want: true},
		{name: "cpu exceeds", request: ResourceVector{CPUMilli: 501}, capacity: original},
		{name: "missing tool capacity", request: ResourceVector{ToolSlots: map[string]int64{"browser": 1}}, capacity: original},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.request.FitsWithin(test.capacity); got != test.want {
				t.Fatalf("FitsWithin() = %v, want %v", got, test.want)
			}
		})
	}

	zeroWithNamedSlots := ResourceVector{ToolSlots: map[string]int64{"shell": 0}}
	if !zeroWithNamedSlots.IsZero() || !reflect.DeepEqual(ResourceVector{}.Clone(), ResourceVector{}) {
		t.Fatal("zero or nil-map semantics are incorrect")
	}
}

func TestResourceVectorArithmetic(t *testing.T) {
	left := ResourceVector{CPUMilli: 600, MemoryMiB: 256, LLMSlots: 1, TokenBudget: 1000, ToolSlots: map[string]int64{"shell": 1}}
	right := ResourceVector{CPUMilli: 400, MemoryMiB: 256, LLMSlots: 2, TokenBudget: 500, ToolSlots: map[string]int64{"shell": 2, "browser": 1}}
	want := ResourceVector{CPUMilli: 1000, MemoryMiB: 512, LLMSlots: 3, TokenBudget: 1500, ToolSlots: map[string]int64{"shell": 3, "browser": 1}}
	if got := left.Add(right); !reflect.DeepEqual(got, want) {
		t.Fatalf("Add() = %#v, want %#v", got, want)
	}
	got, err := left.AddChecked(right)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("AddChecked() = (%#v, %v), want (%#v, nil)", got, err, want)
	}
	if got.ToolSlots["shell"] = 99; left.ToolSlots["shell"] != 1 {
		t.Fatal("arithmetic result aliases an operand")
	}
	restored, err := want.Sub(right)
	if err != nil || restored.CPUMilli != left.CPUMilli || restored.MemoryMiB != left.MemoryMiB ||
		restored.LLMSlots != left.LLMSlots || restored.TokenBudget != left.TokenBudget ||
		restored.ToolSlots["shell"] != left.ToolSlots["shell"] || restored.ToolSlots["browser"] != 0 {
		t.Fatalf("Sub() = (%#v, %v), want original values and a zero browser slot", restored, err)
	}
	if _, err := left.Sub(right); !errors.Is(err, ErrInsufficientResource) {
		t.Fatalf("Sub() error = %v, want ErrInsufficientResource", err)
	}
	if _, err := (ResourceVector{CPUMilli: math.MaxInt64}).AddChecked(ResourceVector{CPUMilli: 1}); !errors.Is(err, ErrResourceOverflow) {
		t.Fatalf("AddChecked() error = %v, want ErrResourceOverflow", err)
	}
	if _, err := (ResourceVector{CPUMilli: -1}).AddChecked(ResourceVector{}); !errors.Is(err, ErrInvalidResourceVector) {
		t.Fatalf("AddChecked() error = %v, want ErrInvalidResourceVector", err)
	}
}

func TestResourceVectorDominantShare(t *testing.T) {
	capacity := ResourceVector{CPUMilli: 1000, MemoryMiB: 1000, LLMSlots: 4, TokenBudget: 10000, ToolSlots: map[string]int64{"shell": 2}}
	request := ResourceVector{CPUMilli: 200, MemoryMiB: 500, LLMSlots: 1, TokenBudget: 1000, ToolSlots: map[string]int64{"shell": 2}}
	if got := request.DominantShare(capacity); got != 1 {
		t.Fatalf("DominantShare() = %v, want 1", got)
	}
	if got := (ResourceVector{}).DominantShare(ResourceVector{}); got != 0 {
		t.Fatalf("zero DominantShare() = %v, want 0", got)
	}
	if got := (ResourceVector{LLMSlots: 1}).DominantShare(ResourceVector{}); !math.IsInf(got, 1) {
		t.Fatalf("zero-capacity DominantShare() = %v, want +Inf", got)
	}
}
