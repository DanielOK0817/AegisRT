package api

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/taskgraph"
)

func TestPatchRejectsTimeoutOverflowBeforeControl(t *testing.T) {
	for _, timeout := range []int64{math.MaxInt64/int64(time.Millisecond) + 1, 18446744073710} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			control := &fakeControl{}
			body := strings.Replace(validPatch, `"timeout_ms":1000`, fmt.Sprintf(`"timeout_ms":%d`, timeout), 1)
			response := performJSONRequest(testControlHandler(control, apiToken, nil), http.MethodPost, "/v1/task-graphs/patches", body, apiToken)
			if response.Code != http.StatusBadRequest || control.patchCalls != 0 {
				t.Fatalf("overflow accepted: status=%d calls=%d body=%s", response.Code, control.patchCalls, response.Body)
			}
		})
	}
}

func TestPatchMapsGraphLimitsToUnprocessableEntity(t *testing.T) {
	control := &fakeControl{patchErr: fmt.Errorf("graph is full: %w", taskgraph.ErrGraphLimit)}
	response := performJSONRequest(testControlHandler(control, apiToken, nil), http.MethodPost, "/v1/task-graphs/patches", validPatch, apiToken)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"task_limit_exceeded"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}
