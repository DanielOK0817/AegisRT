package api

import (
	"errors"
	"net/http"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/runtimeapp"
)

type outputSummaryControl interface {
	InspectOutputSummary(string) (runtimeapp.OutputSummary, error)
}

func (s *Server) inspectOutputSummary(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAuthorization(writer, request) {
		return
	}
	control, ok := s.contextControl.(outputSummaryControl)
	if !ok {
		writeAPIError(writer, http.StatusServiceUnavailable, "output_unavailable",
			"durable output summary is unavailable")
		return
	}
	summary, err := control.InspectOutputSummary(request.PathValue("taskID"))
	if err != nil {
		status, code, message := http.StatusInternalServerError, "output_failed", "output summary failed"
		if errors.Is(err, contextstate.ErrOutputNotFound) {
			status, code, message = http.StatusNotFound, "output_not_found", "durable output is unavailable"
		} else if errors.Is(err, contextstate.ErrUnavailable) {
			status, code, message = http.StatusServiceUnavailable, "output_unavailable", "durable output summary is unavailable"
		}
		writeAPIError(writer, status, code, message)
		return
	}
	if summary.RecordCount != 1 || summary.ArtifactCount < 0 || summary.ArtifactCount > 32 {
		writeAPIError(writer, http.StatusInternalServerError, "output_failed", "output summary failed")
		return
	}
	writeJSON(writer, http.StatusOK, summary)
}
