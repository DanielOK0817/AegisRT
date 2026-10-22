package benchmark

import (
	"context"
	"os"
	"testing"

	"aegisrt/internal/domain"
	"aegisrt/internal/faultfixture"
)

func TestFaultSmokeRunsProductionExitTimeoutAndServiceRestart(t *testing.T) {
	evidence, err := SimulateFaultEvidence(context.Background(), FaultPlan{
		Seeds: []uint32{7}, Repetitions: 1,
	}, testFaultAgentCommand())
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Result.Rows) != 6 || len(evidence.TrialTraceRecords()) != 6 {
		t.Fatalf("fault evidence rows/traces = %d/%d", len(evidence.Result.Rows), len(evidence.TrialTraceRecords()))
	}

	f1 := findFaultTrace(t, evidence.TrialTraceRecords(), "F1-smoke", "treatment")
	if f1.Target.FinalStatus != domain.TaskStatusFailed || len(f1.Target.Attempts) != 1 ||
		f1.Target.Attempts[0].ExitCode == nil || *f1.Target.Attempts[0].ExitCode != 7 ||
		f1.Target.Attempts[0].TimedOut || f1.UnaffectedTasks[0].FinalStatus != domain.TaskStatusSucceeded {
		t.Fatalf("F1 treatment trace = %#v", f1)
	}

	f2 := findFaultTrace(t, evidence.TrialTraceRecords(), "F2-smoke", "treatment")
	if f2.Target.FinalStatus != domain.TaskStatusFailed || len(f2.Target.Attempts) != 1 ||
		!f2.Target.Attempts[0].TimedOut || f2.Target.Attempts[0].Canceled ||
		f2.UnaffectedTasks[0].FinalStatus != domain.TaskStatusSucceeded {
		t.Fatalf("F2 treatment trace = %#v", f2)
	}

	f3 := findFaultTrace(t, evidence.TrialTraceRecords(), "F3-service-smoke", "treatment")
	if f3.Target.FinalStatus != domain.TaskStatusSucceeded || len(f3.Target.Attempts) != 2 ||
		f3.Target.Attempts[0].Status != domain.AttemptStatusLost ||
		f3.Target.Attempts[1].Status != domain.AttemptStatusSucceeded ||
		!f3.Recovery.RuntimeReopened || !f3.Recovery.Requeued || f3.Recovery.RetryCount != 1 ||
		f3.UnaffectedTasks[0].FinalStatus != domain.TaskStatusSucceeded {
		t.Fatalf("F3 treatment trace = %#v", f3)
	}
	if f3.Milestones.AgentReadyNS == nil || f3.Milestones.InjectedNS == nil ||
		*f3.Milestones.AgentReadyNS > *f3.Milestones.InjectedNS {
		t.Fatalf("F3 injection did not follow Agent readiness: %#v", f3.Milestones)
	}
	f3Row := findFaultRow(t, evidence.Result.Rows, "F3-service-smoke", "treatment")
	wantRequeueMS := float64(*f3.Milestones.RequeuedNS-*f3.Milestones.InjectedNS) / 1e6
	if f3Row.RequeueMS == nil || *f3Row.RequeueMS <= 0 || *f3Row.RequeueMS != wantRequeueMS {
		t.Fatalf("F3 requeue_ms = %v, want injection-to-READY %f", f3Row.RequeueMS, wantRequeueMS)
	}
	for _, trace := range evidence.TrialTraceRecords() {
		if !trace.Cleanup.CleanupObserved || !trace.Cleanup.ResourcesReleased ||
			trace.Cleanup.OrphanProcesses == nil || *trace.Cleanup.OrphanProcesses != 0 ||
			trace.Cleanup.OrphanCgroups != nil || trace.DuplicateArtifacts != 0 {
			t.Fatalf("fault cleanup/publication evidence = %#v / duplicates=%d", trace.Cleanup, trace.DuplicateArtifacts)
		}
	}
}

func TestFaultAgentHelperProcess(t *testing.T) {
	marker := -1
	for index, argument := range os.Args {
		if argument == "fault-fixture-marker" {
			marker = index
			break
		}
	}
	if marker < 0 {
		return
	}
	os.Exit(faultfixture.Run(context.Background(), os.Args[marker+1:], os.Stdin, os.Stdout, os.Stderr))
}

func testFaultAgentCommand() FaultAgentCommand {
	return FaultAgentCommand{
		Entrypoint: os.Args[0],
		ArgsPrefix: []string{"-test.run=TestFaultAgentHelperProcess", "--", "fault-fixture-marker"},
	}
}

func findFaultTrace(t *testing.T, traces []FaultTrialTrace, caseID, role string) FaultTrialTrace {
	t.Helper()
	for _, trace := range traces {
		if trace.CaseID == caseID && trace.Role == role {
			return trace
		}
	}
	t.Fatalf("fault trace %s/%s not found", caseID, role)
	return FaultTrialTrace{}
}

func findFaultRow(t *testing.T, rows []FaultRow, caseID, role string) FaultRow {
	t.Helper()
	for _, row := range rows {
		if row.CaseID == caseID && row.Role == role {
			return row
		}
	}
	t.Fatalf("fault row %s/%s not found", caseID, role)
	return FaultRow{}
}
