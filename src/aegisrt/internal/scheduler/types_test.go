package scheduler

import "testing"

func TestResourceVectorFitsAndDominantShare(t *testing.T) {
	capacity := ResourceVector{
		CPUMilli:    1000,
		MemoryMiB:   2048,
		LLMSlots:    2,
		TokenBudget: 10000,
		ToolSlots:   map[string]int64{"shell": 2},
	}
	request := ResourceVector{
		CPUMilli:    500,
		MemoryMiB:   256,
		LLMSlots:    1,
		TokenBudget: 1000,
		ToolSlots:   map[string]int64{"shell": 1},
	}
	if !request.FitsWithin(capacity) {
		t.Fatal("expected request to fit")
	}
	if got := request.DominantShare(capacity); got != 0.5 {
		t.Fatalf("dominant share = %v, want 0.5", got)
	}
}

func TestResourceVectorRejectsMissingToolCapacity(t *testing.T) {
	request := ResourceVector{ToolSlots: map[string]int64{"browser": 1}}
	if request.FitsWithin(ResourceVector{}) {
		t.Fatal("request must not fit when tool capacity is absent")
	}
}

func TestResourceVectorSubDoesNotUnderflow(t *testing.T) {
	_, err := (ResourceVector{CPUMilli: 100}).Sub(ResourceVector{CPUMilli: 200})
	if err != ErrInsufficientResource {
		t.Fatalf("error = %v, want %v", err, ErrInsufficientResource)
	}
}
