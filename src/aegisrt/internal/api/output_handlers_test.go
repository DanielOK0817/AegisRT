package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/runtimeapp"
)

func TestOutputSummaryIsAuthenticatedContentFreeAndBounded(t *testing.T) {
	control := &fakeContextControl{outputResult: runtimeapp.OutputSummary{RecordCount: 1, ArtifactCount: 1}}
	handler := contextHandler(control, apiToken)
	unauthorized := contextRequest(handler, http.MethodGet, "/v1/tasks/task-1/output-summary", "", "", "", "")
	if unauthorized.Code != http.StatusUnauthorized || control.outputCalls != 0 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorized.Code, control.outputCalls)
	}
	response := contextRequest(handler, http.MethodGet, "/v1/tasks/task-1/output-summary", "", apiToken, "", "")
	if response.Code != http.StatusOK || control.outputCalls != 1 || control.lastTaskID != "task-1" {
		t.Fatalf("status=%d calls=%d task=%q body=%s", response.Code, control.outputCalls, control.lastTaskID, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"record_count":1`) || !strings.Contains(body, `"artifact_count":1`) {
		t.Fatalf("summary body = %s", body)
	}
	for _, forbidden := range []string{"content", "fingerprint", "recipient", "page_id", "published_at"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("summary leaked %q: %s", forbidden, body)
		}
	}
}

func TestOutputSummaryErrorsDoNotEchoTaskOrRepositoryError(t *testing.T) {
	secret := "secret-task-and-path"
	control := &fakeContextControl{outputErr: errors.Join(contextstate.ErrOutputNotFound, errors.New(secret))}
	response := contextRequest(contextHandler(control, apiToken), http.MethodGet,
		"/v1/tasks/"+secret+"/output-summary", "", apiToken, "", "")
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
