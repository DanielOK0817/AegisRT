package benchmark

import (
	"context"
	"net/http"
	"net/url"
)

type faultKernelOutputSummaryResponse struct {
	RecordCount   *int `json:"record_count"`
	ArtifactCount *int `json:"artifact_count"`
}

func (client *faultKernelAPIClient) OutputArtifactCount(ctx context.Context, taskID string) (int, error) {
	if !validLabel(taskID, false) {
		return 0, errFaultKernelAPITaskGraph
	}
	var response faultKernelOutputSummaryResponse
	path := "/v1/tasks/" + url.PathEscape(taskID) + "/output-summary"
	if err := client.doJSON(ctx, http.MethodGet, path, nil, true, http.StatusOK, &response); err != nil {
		return 0, err
	}
	if response.RecordCount == nil || *response.RecordCount != 1 ||
		response.ArtifactCount == nil || *response.ArtifactCount < 0 || *response.ArtifactCount > 32 {
		return 0, errFaultKernelAPIResponse
	}
	return *response.ArtifactCount, nil
}
