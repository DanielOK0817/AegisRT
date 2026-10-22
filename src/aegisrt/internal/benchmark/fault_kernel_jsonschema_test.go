package benchmark

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestFaultKernelTrialTraceJSONSchemaDraft2020(t *testing.T) {
	schema := compileFaultKernelDraft2020Schema(t, "fault-kernel-trial-trace-v1.schema.json")
	baseline, _ := validFaultKernelBaselineEvidence()
	treatment, _ := validFaultKernelTreatmentEvidence()

	for name, trace := range map[string]FaultKernelTrialTrace{
		"baseline":  baseline,
		"treatment": treatment,
	} {
		t.Run("valid_"+name, func(t *testing.T) {
			if err := validateFaultKernelJSONSchema(schema, trace); err != nil {
				t.Fatalf("valid %s trace rejected: %v", name, err)
			}
		})
	}

	t.Run("mixed_profile_plan_rejected", func(t *testing.T) {
		trace := baseline
		trace.PlanID, trace.CaseID = FaultKernelFormalPlanID, FaultKernelFormalCaseID
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("smoke_warmup_rejected", func(t *testing.T) {
		trace := baseline
		trace.Phase = "warmup"
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("formal_unknown_seed_rejected", func(t *testing.T) {
		trace := baseline
		trace.Profile, trace.PlanID, trace.CaseID =
			FaultKernelProfileFormal, FaultKernelFormalPlanID, FaultKernelFormalCaseID
		trace.Seed, trace.Repetition = 1, 0
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("fixed_formal_measurement_valid", func(t *testing.T) {
		trace := baseline
		trace.Profile, trace.PlanID, trace.CaseID =
			FaultKernelProfileFormal, FaultKernelFormalPlanID, FaultKernelFormalCaseID
		trace.Seed, trace.Repetition = FaultKernelFormalMeasuredSeeds()[0], 0
		if err := validateFaultKernelJSONSchema(schema, trace); err != nil {
			t.Fatalf("fixed formal measurement rejected: %v", err)
		}
	})

	t.Run("running_terminal_rejected", func(t *testing.T) {
		trace := failedBaselineSchemaTrace()
		trace.Target.Attempts[0].FinalStatus = "RUNNING"
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("unobserved_nonzero_cleanup_rejected", func(t *testing.T) {
		trace := failedTreatmentSchemaTrace()
		trace.Observations.OldProcessesAfterCleanupObserved = false
		trace.Observations.OldProcessesAfterCleanup = 1
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("baseline_treatment_reason_rejected", func(t *testing.T) {
		trace := failedBaselineSchemaTrace()
		trace.ReasonCode = "CHECKPOINT_NOT_LOADED"
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("duplicate_reason_without_count_rejected", func(t *testing.T) {
		trace := baseline
		trace.Status = "FAILED"
		trace.ReasonCode = "DUPLICATE_ARTIFACTS"
		trace.Observations.DuplicateArtifacts = 0
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	for _, test := range []struct {
		name   string
		reason string
		count  int
	}{
		{name: "isolation_reason_without_escape", reason: "ISOLATION_ESCAPE", count: 0},
		{name: "escape_without_isolation_reason", reason: "HARNESS_FAILURE", count: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			trace := failedTreatmentSchemaTrace()
			trace.ReasonCode = test.reason
			trace.Observations.IsolationEscapeCount = test.count
			assertFaultKernelJSONSchemaRejects(t, schema, trace)
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*FaultKernelTrialTrace)
	}{
		{name: "missing_startup_receipt", mutate: func(trace *FaultKernelTrialTrace) { trace.StartupReceipt = nil }},
		{name: "missing_injection", mutate: func(trace *FaultKernelTrialTrace) { trace.Milestones.InjectedNS = nil }},
		{name: "missing_daemon_exit", mutate: func(trace *FaultKernelTrialTrace) { trace.Milestones.DaemonExitedNS = nil }},
		{name: "sigkill_not_observed", mutate: func(trace *FaultKernelTrialTrace) { trace.Observations.DaemonSIGKILLObserved = false }},
		{name: "orphan_not_observed", mutate: func(trace *FaultKernelTrialTrace) { trace.Observations.OrphanObservedBeforeRestart = false }},
		{name: "checkpoint_not_loaded", mutate: func(trace *FaultKernelTrialTrace) { trace.Observations.CheckpointLoaded = false }},
		{name: "recovery_not_requeued", mutate: func(trace *FaultKernelTrialTrace) { trace.Observations.RecoveryRequeued = false }},
		{name: "restart_not_ready", mutate: func(trace *FaultKernelTrialTrace) { trace.Observations.RestartReady = false }},
	} {
		t.Run("attempt2_"+test.name+"_rejected", func(t *testing.T) {
			trace := treatment
			test.mutate(&trace)
			assertFaultKernelJSONSchemaRejects(t, schema, trace)
		})
	}

	t.Run("attempt1_recovery_receipt_rejected", func(t *testing.T) {
		trace := failedTreatmentAttemptOneSchemaTrace()
		receipt := FaultKernelStartupReceipt{1, true, true, 1, 0, 0}
		trace.StartupReceipt = &receipt
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("attempt1_requeue_rejected", func(t *testing.T) {
		trace := failedTreatmentAttemptOneSchemaTrace()
		trace.Observations.RecoveryRequeued = true
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	for _, test := range []struct {
		name, status, reason string
	}{
		{name: "daemon_exit_stage", status: "FAILED", reason: "DAEMON_EXIT_MISMATCH"},
		{name: "orphan_stage", status: "FAILED", reason: "ORPHAN_NOT_OBSERVED"},
		{name: "checkpoint_stage", status: "FAILED", reason: "CHECKPOINT_NOT_LOADED"},
		{name: "requeue_stage", status: "FAILED", reason: "RECOVERY_NOT_REQUEUED"},
		{name: "invalid_namespace_stage", status: "INVALID", reason: "HARNESS_FAILURE"},
	} {
		t.Run(test.name+"_valid", func(t *testing.T) {
			trace, _ := faultKernelReasonStageEvidence(test.status, test.reason)
			if err := validateFaultKernelJSONSchema(schema, trace); err != nil {
				t.Fatalf("valid reason stage rejected: %v", err)
			}
		})
	}

	t.Run("attempt1_retry_without_orphan_rejected", func(t *testing.T) {
		trace := failedTreatmentAttemptOneSchemaTrace()
		trace.ReasonCode = "RETRY_NOT_READY"
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("attempt2_retry_stage_valid", func(t *testing.T) {
		trace, _ := faultKernelAttemptTwoRetryEvidence()
		if err := validateFaultKernelJSONSchema(schema, trace); err != nil {
			t.Fatalf("valid attempt-two retry stage rejected: %v", err)
		}
	})

	t.Run("attempt2_retry_claiming_ready_rejected", func(t *testing.T) {
		trace, _ := faultKernelAttemptTwoRetryEvidence()
		trace.Target.Attempts[1].ReadyObserved = true
		readyNS := int64(60_000_000)
		trace.Milestones.Attempt2ReadyNS = &readyNS
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("attempt1_cleanup_rejected", func(t *testing.T) {
		trace := failedTreatmentAttemptOneSchemaTrace()
		trace.ReasonCode = "CLEANUP_INCOMPLETE"
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("invalid_attempt2_rejected", func(t *testing.T) {
		trace := failedTreatmentSchemaTrace()
		trace.Status = "INVALID"
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})

	t.Run("failed_target_duplicate_observation_rejected", func(t *testing.T) {
		trace := failedTreatmentSchemaTrace()
		trace.Observations.DuplicateArtifactsObserved = true
		assertFaultKernelJSONSchemaRejects(t, schema, trace)
	})
}

func TestFaultKernelCgroupSampleJSONSchemaDraft2020(t *testing.T) {
	schema := compileFaultKernelDraft2020Schema(t, "cgroup-sample-v1.schema.json")
	_, baselineSamples := validFaultKernelBaselineEvidence()
	_, treatmentSamples := validFaultKernelTreatmentEvidence()
	for index, sample := range append(baselineSamples, treatmentSamples...) {
		if err := validateFaultKernelJSONSchema(schema, sample); err != nil {
			t.Fatalf("valid cgroup sample %d rejected: %v", index, err)
		}
	}

	for _, test := range []struct {
		name   string
		sample FaultKernelCgroupSample
	}{
		{name: "attempt1_with_attempt2_number", sample: func() FaultKernelCgroupSample {
			value := treatmentSamples[0]
			value.AttemptNumber = 2
			return value
		}()},
		{name: "attempt1_absent", sample: func() FaultKernelCgroupSample {
			value := treatmentSamples[2]
			value.Stage = "attempt1_ready"
			return value
		}()},
		{name: "post_restart_present", sample: func() FaultKernelCgroupSample {
			value := treatmentSamples[0]
			value.Stage = "post_runtime_restart"
			return value
		}()},
		{name: "attempt2_with_attempt1_number", sample: func() FaultKernelCgroupSample {
			value := treatmentSamples[3]
			value.AttemptNumber = 1
			return value
		}()},
		{name: "terminal_present", sample: func() FaultKernelCgroupSample {
			value := treatmentSamples[3]
			value.Stage = "trial_terminal"
			return value
		}()},
	} {
		t.Run(test.name+"_rejected", func(t *testing.T) {
			assertFaultKernelJSONSchemaRejects(t, schema, test.sample)
		})
	}
}

func compileFaultKernelDraft2020Schema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "experiments", "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(name, document); err != nil {
		t.Fatalf("add %s: %v", name, err)
	}
	schema, err := compiler.Compile(name)
	if err != nil {
		t.Fatalf("compile %s as Draft 2020-12: %v", name, err)
	}
	return schema
}

func validateFaultKernelJSONSchema(schema *jsonschema.Schema, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	return schema.Validate(document)
}

func assertFaultKernelJSONSchemaRejects(t *testing.T, schema *jsonschema.Schema, value any) {
	t.Helper()
	if err := validateFaultKernelJSONSchema(schema, value); err == nil {
		t.Fatal("invalid document was accepted by the published JSON Schema")
	}
}

func failedBaselineSchemaTrace() FaultKernelTrialTrace {
	trace, _ := validFaultKernelBaselineEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "HARNESS_FAILURE"
	trace.Target.FinalStatus = "FAILED"
	trace.Target.Attempts[0].FinalStatus = "FAILED"
	trace.Target.Attempts[0].ArtifactObservations = 0
	trace.Target.Attempts[0].ReasonCode = "RUNTIME_INTERRUPTED"
	trace.Observations.DuplicateArtifactsObserved = false
	return trace
}

func failedTreatmentSchemaTrace() FaultKernelTrialTrace {
	trace, _ := validFaultKernelTreatmentEvidence()
	trace.Status, trace.ReasonCode = "FAILED", "HARNESS_FAILURE"
	trace.Target.FinalStatus = "FAILED"
	trace.Target.Attempts[1].FinalStatus = "FAILED"
	trace.Target.Attempts[1].ArtifactObservations = 0
	trace.Target.Attempts[1].ReasonCode = "RUNTIME_INTERRUPTED"
	trace.Observations.DuplicateArtifactsObserved = false
	return trace
}

func failedTreatmentAttemptOneSchemaTrace() FaultKernelTrialTrace {
	trace, _ := failedFaultKernelTreatmentBeforeRestartEvidence(0)
	trace.ReasonCode = "HARNESS_FAILURE"
	return trace
}
