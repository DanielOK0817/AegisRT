package runtimeapp

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
)

func TestRejectedPatchesDoNotAccumulateContextHistory(t *testing.T) {
	repository, err := contextstate.New(contextstate.Config{
		Fingerprint: "rollback-test-v1", RuntimeInstanceID: runtimeappTestInstanceID,
		Counter: contextvm.WhitespaceCounter{}, Limits: contextstate.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	patchErr := errors.New("patch rejected")
	control, err := NewContextControl(
		&fakeControl{resultErr: patchErr}, repository, []domain.AgentSpec{runtimeAgent("agent-1")},
	)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 100; index++ {
		task := runtimeTask(fmt.Sprintf("rejected-%d", index), "agent-1")
		task.WorkflowID = fmt.Sprintf("workflow-%d", index)
		if err := control.ApplyPatch([]domain.TaskSpec{task}, time.Now()); !errors.Is(err, patchErr) {
			t.Fatalf("ApplyPatch %d error = %v, want patch rejection", index, err)
		}
	}
	stats := repository.Stats()
	if stats.HeadCount != 0 || stats.ManifestCount != 0 || stats.Store.UniquePages != 0 {
		t.Fatalf("rejected patches retained Context state: %+v", stats)
	}
}
