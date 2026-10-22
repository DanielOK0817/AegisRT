package benchmark

import (
	"context"
	"fmt"

	"aegisrt/internal/contextvm"
)

func runC3Trial(ctx context.Context, workload contextCaseWorkload, treatment string) (contextTrialData, error) {
	store := contextvm.NewStore()
	instances := make([]contextPageInstance, 0, len(workload.Pages))
	for _, descriptor := range workload.Pages {
		if err := ctx.Err(); err != nil {
			return contextTrialData{}, err
		}
		instance, err := instantiateContextPage(workload, descriptor)
		if err != nil {
			return contextTrialData{}, err
		}
		instance, err = putContextPage(store, instance)
		if err != nil {
			return contextTrialData{}, err
		}
		instances = append(instances, instance)
	}
	var summarizer contextvm.Summarizer
	switch treatment {
	case "contextvm-drop":
	case "contextvm-compress":
		var err error
		summarizer, err = contextvm.NewExtractiveSummarizer(contextvm.WhitespaceCounter{})
		if err != nil {
			return contextTrialData{}, err
		}
	default:
		return contextTrialData{}, fmt.Errorf("unsupported C3 treatment %q", treatment)
	}
	manifest, err := contextvm.NewManifest(workload.WorkflowID, workload.Agents[0], refsForInstances(instances), nil)
	if err != nil {
		return contextTrialData{}, err
	}
	assembler, err := contextvm.NewAssembler(store, contextvm.WhitespaceCounter{}, summarizer)
	if err != nil {
		return contextTrialData{}, err
	}
	assembly, err := assembler.Assemble(ctx, contextvm.AssembleRequest{
		Manifest: manifest, Access: contextvm.AccessRequest{WorkflowID: workload.WorkflowID, AgentID: workload.Agents[0]},
		TokenBudget: workload.ComparisonBudget,
	})
	if err != nil {
		return contextTrialData{}, err
	}
	dispatch, err := contextDispatchObservation(
		"task-context", workload.Agents[0], "store-context", instances, assembly, workload.ComparisonBudget,
	)
	if err != nil {
		return contextTrialData{}, err
	}
	if !dispatch.PinnedPageRequired || !dispatch.PinnedPagePreserved {
		return contextTrialData{}, fmt.Errorf("pinned context page was not preserved")
	}
	return contextTrialData{
		stores:     []ContextStoreTrace{contextStoreObservation("store-context", store.Stats())},
		dispatches: []ContextDispatchTrace{dispatch}, manifestOperations: 1,
	}, nil
}
