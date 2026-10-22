package benchmark

import "testing"

func TestContextV2TrialTracesMatchPublishedSchema(t *testing.T) {
	schema := compileFaultKernelDraft2020Schema(t, "context-trial-trace-v2.schema.json")
	evidence, err := SimulateContextEvidence(ContextPlan{Seeds: []uint32{7}, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	traces := evidence.TrialTraceRecords()
	for index := range traces {
		traces[index].RunID = "run-1234567890abcdef12345678"
		if err := validateFaultKernelJSONSchema(schema, traces[index]); err != nil {
			t.Fatalf("valid context trace %d rejected: %v", index, err)
		}
	}

	t.Run("summary_without_provenance_rejected", func(t *testing.T) {
		for _, trace := range traces {
			if trace.Treatment != "contextvm-compress" {
				continue
			}
			trace.Dispatches[0].Pages[0].SummaryPageID = ""
			if err := validateFaultKernelJSONSchema(schema, trace); err == nil {
				t.Fatal("summary without page provenance was accepted")
			}
			return
		}
		t.Fatal("compression trace not found")
	})

	t.Run("compression_with_disabled_summarizer_rejected", func(t *testing.T) {
		for _, trace := range traces {
			if trace.Treatment != "contextvm-compress" {
				continue
			}
			trace.SummarizerMode = "disabled"
			if err := validateFaultKernelJSONSchema(schema, trace); err == nil {
				t.Fatal("compression trace with disabled summarizer was accepted")
			}
			return
		}
		t.Fatal("compression trace not found")
	})
}
