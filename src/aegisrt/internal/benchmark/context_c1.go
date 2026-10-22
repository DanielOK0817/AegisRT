package benchmark

import (
	"context"
	"errors"
	"fmt"

	"aegisrt/internal/contextvm"
)

func runC1Trial(ctx context.Context, workload contextCaseWorkload, treatment string) (contextTrialData, error) {
	switch treatment {
	case "copy-per-task":
		return runC1IsolatedStores(ctx, workload)
	case "contextvm-dedup":
		return runC1SharedStore(ctx, workload)
	default:
		return contextTrialData{}, fmt.Errorf("unsupported C1 treatment %q", treatment)
	}
}

func runC1IsolatedStores(ctx context.Context, workload contextCaseWorkload) (contextTrialData, error) {
	result := contextTrialData{}
	var probeStore *contextvm.Store
	var probeInstance contextPageInstance
	for index, descriptor := range workload.Pages {
		if err := ctx.Err(); err != nil {
			return contextTrialData{}, err
		}
		storeID := fmt.Sprintf("store-%d", index)
		store := contextvm.NewStore()
		instance, err := instantiateContextPage(workload, descriptor)
		if err != nil {
			return contextTrialData{}, err
		}
		instance, err = putContextPage(store, instance)
		if err != nil {
			return contextTrialData{}, err
		}
		dispatch, err := assembleC1(ctx, workload, store, storeID, instance)
		if err != nil {
			return contextTrialData{}, err
		}
		result.manifestOperations++
		result.dispatches = append(result.dispatches, dispatch)
		result.stores = append(result.stores, contextStoreObservation(storeID, store.Stats()))
		if index == 0 {
			probeStore, probeInstance = store, instance
		}
	}
	probe, err := runC1ACLProbe(probeStore, probeInstance, workload.Agents[1], workload.WorkflowID)
	if err != nil {
		return contextTrialData{}, err
	}
	result.aclProbes = []ContextACLProbe{probe}
	return result, nil
}

func runC1SharedStore(ctx context.Context, workload contextCaseWorkload) (contextTrialData, error) {
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
	result := contextTrialData{}
	for _, instance := range instances {
		dispatch, err := assembleC1(ctx, workload, store, "store-shared", instance)
		if err != nil {
			return contextTrialData{}, err
		}
		result.manifestOperations++
		result.dispatches = append(result.dispatches, dispatch)
	}
	probe, err := runC1ACLProbe(store, instances[0], workload.Agents[1], workload.WorkflowID)
	if err != nil {
		return contextTrialData{}, err
	}
	result.aclProbes = []ContextACLProbe{probe}
	result.stores = []ContextStoreTrace{contextStoreObservation("store-shared", store.Stats())}
	return result, nil
}

func assembleC1(ctx context.Context, workload contextCaseWorkload, store *contextvm.Store, storeID string, instance contextPageInstance) (ContextDispatchTrace, error) {
	manifest, err := contextvm.NewManifest(workload.WorkflowID, instance.descriptor.AgentID, nil, []contextvm.ContextRef{instance.ref})
	if err != nil {
		return ContextDispatchTrace{}, err
	}
	assembler, err := contextvm.NewAssembler(store, contextvm.WhitespaceCounter{}, nil)
	if err != nil {
		return ContextDispatchTrace{}, err
	}
	assembly, err := assembler.Assemble(ctx, contextvm.AssembleRequest{
		Manifest:    manifest,
		Access:      contextvm.AccessRequest{WorkflowID: workload.WorkflowID, AgentID: instance.descriptor.AgentID},
		TokenBudget: workload.ComparisonBudget,
	})
	if err != nil {
		return ContextDispatchTrace{}, err
	}
	return contextDispatchObservation("task-"+instance.descriptor.AgentID, instance.descriptor.AgentID, storeID, []contextPageInstance{instance}, assembly, workload.ComparisonBudget)
}

func runC1ACLProbe(store *contextvm.Store, instance contextPageInstance, requester, workflowID string) (ContextACLProbe, error) {
	_, err := store.Get(instance.ref, contextvm.AccessRequest{WorkflowID: workflowID, AgentID: requester})
	if !errors.Is(err, contextvm.ErrAccessDenied) {
		return ContextACLProbe{}, fmt.Errorf("cross-agent ACL probe returned %v", err)
	}
	return ContextACLProbe{
		PageID: string(instance.page.ID), OwnerAgentID: instance.descriptor.AgentID,
		RequestAgentID: requester, ResultCode: "ACCESS_DENIED",
	}, nil
}
