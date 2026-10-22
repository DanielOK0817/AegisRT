package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/domain"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/taskgraph"
)

const (
	maxRequestBytes = 1 << 20
	maxPatchTasks   = taskgraph.MaxPatchTasks
)

func (s *Server) applyTaskGraphPatch(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAuthorization(writer, request) || !requireJSONContentType(writer, request) || !s.requireControl(writer) {
		return
	}
	var input struct {
		Tasks []taskDTO `json:"tasks"`
	}
	if err := decodeJSON(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if len(input.Tasks) > maxPatchTasks {
		writeAPIError(writer, http.StatusUnprocessableEntity, "task_limit_exceeded",
			fmt.Sprintf("a task patch may contain at most %d tasks", maxPatchTasks))
		return
	}
	tasks := make([]domain.TaskSpec, 0, len(input.Tasks))
	for index, dto := range input.Tasks {
		task, err := dto.domain()
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, "invalid_task", fmt.Sprintf("tasks[%d]: %v", index, err))
			return
		}
		tasks = append(tasks, task)
	}
	if err := s.control.ApplyPatch(tasks, time.Now().UTC()); err != nil {
		status := http.StatusBadRequest
		code := "patch_rejected"
		message := err.Error()
		if errors.Is(err, taskgraph.ErrGraphLimit) {
			status = http.StatusUnprocessableEntity
			code = "task_limit_exceeded"
		} else if errors.Is(err, runtimecore.ErrTaskExceedsCapacity) {
			status = http.StatusUnprocessableEntity
		} else if errors.Is(err, contextstate.ErrCheckpointCapacity) {
			status = http.StatusInsufficientStorage
			code = "context_capacity"
			message = "Context checkpoint capacity is exhausted"
		} else if errors.Is(err, contextstate.ErrUnavailable) {
			status = http.StatusServiceUnavailable
			code = "context_unavailable"
			message = "durable Context state is unavailable"
			s.notifyWork()
		} else if errors.Is(err, runtimecore.ErrCheckpointUnavailable) ||
			errors.Is(err, runtimecore.ErrRuntimeUnavailable) {
			status = http.StatusServiceUnavailable
			code = "runtime_unavailable"
			message = "durable Runtime state is unavailable"
		}
		writeAPIError(writer, status, code, message)
		return
	}
	s.notifyWork()
	writeJSON(writer, http.StatusCreated, map[string]any{"tasks": taskViews(s.control.Tasks())})
}

func (s *Server) listTasks(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAuthorization(writer, request) || !s.requireControl(writer) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"tasks": taskViews(s.control.Tasks())})
}

func (s *Server) cancelTask(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAuthorization(writer, request) || !requireJSONContentType(writer, request) || !s.requireControl(writer) {
		return
	}
	var input map[string]json.RawMessage
	if err := decodeJSON(writer, request, &input); err != nil || input == nil || len(input) != 0 {
		message := "request body must be an empty JSON object"
		if err != nil {
			message = err.Error()
		}
		writeAPIError(writer, http.StatusBadRequest, "invalid_json", message)
		return
	}
	taskID := request.PathValue("taskID")
	snapshot, err := s.control.Cancel(taskID)
	if err != nil {
		status := http.StatusInternalServerError
		code := "cancel_failed"
		switch {
		case errors.Is(err, taskgraph.ErrTaskNotFound):
			status = http.StatusNotFound
			code = "task_not_found"
		case errors.Is(err, runtimecore.ErrTaskNotCancelable):
			status = http.StatusConflict
			code = "task_not_cancelable"
		case errors.Is(err, runtimecore.ErrCheckpointUnavailable),
			errors.Is(err, runtimecore.ErrRuntimeUnavailable):
			status = http.StatusServiceUnavailable
			code = "runtime_unavailable"
			err = errors.New("durable Runtime state is unavailable")
		}
		writeAPIError(writer, status, code, err.Error())
		return
	}
	s.notifyWork()
	status := http.StatusAccepted
	if snapshot.Status == domain.TaskStatusCanceled {
		status = http.StatusOK
	}
	writeJSON(writer, status, map[string]any{"task": view(snapshot)})
}

func (s *Server) requireControl(writer http.ResponseWriter) bool {
	if s.control == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "runtime_unavailable", "runtime kernel is not configured")
		return false
	}
	return true
}

func requireJSONContentType(writer http.ResponseWriter, request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	return true
}

func taskViews(snapshots []taskgraph.TaskSnapshot) []taskView {
	result := make([]taskView, 0, len(snapshots))
	for _, snapshot := range snapshots {
		result = append(result, view(snapshot))
	}
	return result
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request must contain exactly one JSON object")
		}
		return err
	}
	return nil
}

func writeAPIError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
