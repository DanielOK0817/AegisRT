package benchmark

import "testing"

func TestSchedulerCounterbalancesOddSeedIndex(t *testing.T) {
	result, err := SimulateScheduler(SchedulerPlan{Seeds: []uint32{7, 11}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, row := range result.Rows {
		if row.Seed != 11 {
			continue
		}
		seen++
		if row.Role == "treatment" && row.TrialOrder != 0 {
			t.Fatalf("odd-index treatment order = %d, want 0: %#v", row.TrialOrder, row)
		}
		if row.Role == "baseline" && row.TrialOrder != 1 {
			t.Fatalf("odd-index baseline order = %d, want 1: %#v", row.TrialOrder, row)
		}
	}
	if seen != 4 {
		t.Fatalf("odd-index rows = %d, want 4", seen)
	}
}
