package benchmark

import "testing"

func TestSchedulerTaskTracesMatchPublishedSchema(t *testing.T) {
	schema := compileFaultKernelDraft2020Schema(t, "scheduler-task-trace-v1.schema.json")
	for name, plan := range map[string]SchedulerPlan{
		"smoke":  {Seeds: []uint32{7}, Repetitions: 1},
		"formal": NewSchedulerFormalPlan(),
	} {
		t.Run(name, func(t *testing.T) {
			evidence, err := SimulateSchedulerEvidence(plan)
			if err != nil {
				t.Fatal(err)
			}
			for index, trace := range evidence.TaskTraceRecords() {
				trace.RunID = "run-1234567890abcdef12345678"
				if err := validateFaultKernelJSONSchema(schema, trace); err != nil {
					t.Fatalf("valid trace %d rejected: %v", index, err)
				}
			}
		})
	}
}

func TestSchedulerTaskTraceSchemaRejectsMixedCaseIdentity(t *testing.T) {
	schema := compileFaultKernelDraft2020Schema(t, "scheduler-task-trace-v1.schema.json")
	evidence, err := SimulateSchedulerEvidence(NewSchedulerFormalPlan())
	if err != nil {
		t.Fatal(err)
	}
	trace := evidence.TaskTraceRecords()[0]
	trace.RunID = "run-1234567890abcdef12345678"
	trace.PlanID = schedulerSmokePlanID
	if err := validateFaultKernelJSONSchema(schema, trace); err == nil {
		t.Fatal("mixed formal case and smoke plan was accepted")
	}
}

func TestSchedulerAnalysisMatchesPublishedSchema(t *testing.T) {
	schema := compileFaultKernelDraft2020Schema(t, "scheduler-analysis-v1.schema.json")
	result, err := SimulateScheduler(NewSchedulerFormalPlan())
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := analyzeSchedulerRows(schedulerFormalPlanID, result.Rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFaultKernelJSONSchema(schema, analysis); err != nil {
		t.Fatalf("valid scheduler analysis rejected: %v", err)
	}
}
