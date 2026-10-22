package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/runtimeapp"
	"aegisrt/internal/taskgraph"
)

const contextRequestBytes int64 = 128 << 10

type contextPageInput struct {
	Content    string                  `json:"content"`
	Visibility contextstate.Visibility `json:"visibility"`
	Pin        bool                    `json:"pin,omitempty"`
	Cold       bool                    `json:"cold,omitempty"`
}

func (s *Server) inspectContext(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAuthorization(writer, request) || !s.requireContextControl(writer) {
		return
	}
	snapshot, err := s.contextControl.InspectContext(request.PathValue("taskID"))
	if err != nil {
		s.writeContextError(writer, err)
		return
	}
	if !validManifestID(snapshot.Manifest.ManifestID) {
		writeAPIError(writer, http.StatusInternalServerError, "context_failed", "context operation failed")
		return
	}
	writer.Header().Set("ETag", strongETag(snapshot.Manifest.ManifestID))
	writeJSON(writer, http.StatusOK, snapshot)
}

func (s *Server) appendContextPage(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAuthorization(writer, request) || !requireJSONContentType(writer, request) ||
		!s.requireContextControl(writer) {
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(writer, request)
	if !ok {
		return
	}
	expectedManifestID, ok := requireManifestPrecondition(writer, request)
	if !ok {
		return
	}
	input, err := decodeContextPageInput(writer, request)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(writer, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 128 KiB")
			return
		}
		writeAPIError(writer, http.StatusBadRequest, "invalid_json", "request body must be one strict JSON object")
		return
	}
	result, err := s.contextControl.AppendContext(request.Context(), runtimeapp.AppendContextCommand{
		TaskID: request.PathValue("taskID"), ExpectedManifestID: expectedManifestID,
		IdempotencyKey: idempotencyKey, Content: input.Content, Visibility: input.Visibility,
		Pin: input.Pin, Cold: input.Cold,
	})
	if err != nil {
		s.writeContextError(writer, err)
		return
	}
	if !validManifestID(result.ManifestID) {
		writeAPIError(writer, http.StatusInternalServerError, "context_failed", "context operation failed")
		return
	}
	writer.Header().Set("ETag", strongETag(result.ManifestID))
	status := http.StatusCreated
	if result.IdempotentReplay || !result.ManifestChanged {
		status = http.StatusOK
	}
	writeJSON(writer, status, result)
}

func (s *Server) requireContextControl(writer http.ResponseWriter) bool {
	if s.contextControl == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "context_unavailable", "context service is unavailable")
		return false
	}
	return true
}

func requireIdempotencyKey(writer http.ResponseWriter, request *http.Request) (string, bool) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) == 0 || len(values) == 1 && values[0] == "" {
		writeAPIError(writer, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required")
		return "", false
	}
	if len(values) != 1 {
		writeAPIError(writer, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be a single valid value")
		return "", false
	}
	return values[0], true
}

func requireManifestPrecondition(writer http.ResponseWriter, request *http.Request) (string, bool) {
	values := request.Header.Values("If-Match")
	if len(values) == 0 || len(values) == 1 && values[0] == "" {
		writeAPIError(writer, http.StatusPreconditionRequired, "manifest_precondition_required", "one strong If-Match ETag is required")
		return "", false
	}
	if len(values) != 1 || len(values[0]) != 66 || values[0][0] != '"' || values[0][65] != '"' {
		writeAPIError(writer, http.StatusBadRequest, "invalid_precondition", "If-Match must contain one strong manifest ETag")
		return "", false
	}
	manifestID := values[0][1:65]
	if !validManifestID(manifestID) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_precondition", "If-Match must contain one strong manifest ETag")
		return "", false
	}
	return manifestID, true
}

func decodeContextPageInput(writer http.ResponseWriter, request *http.Request) (*contextPageInput, error) {
	request.Body = http.MaxBytesReader(writer, request.Body, contextRequestBytes)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(payload) {
		return nil, errors.New("request body is not valid UTF-8")
	}
	if err := rejectDuplicateTopLevelKeys(payload); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var input *contextPageInput
	if err := decoder.Decode(&input); err != nil || input == nil {
		if err == nil {
			err = errors.New("request body must be an object")
		}
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("request contains multiple JSON values")
		}
		return nil, err
	}
	return input, nil
}

func rejectDuplicateTopLevelKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return errors.New("request body must be an object")
	}
	seen := make(map[string]struct{}, 4)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("request object key is invalid")
		}
		if _, exists := seen[key]; exists {
			return errors.New("request object contains a duplicate key")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return errors.New("request object is not closed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func validManifestID(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func strongETag(manifestID string) string { return `"` + manifestID + `"` }

func (s *Server) writeContextError(writer http.ResponseWriter, err error) {
	if errors.Is(err, contextstate.ErrUnavailable) {
		s.notifyWork()
	}
	status, code, message := contextErrorResponse(err)
	writeAPIError(writer, status, code, message)
}

func contextErrorResponse(err error) (int, string, string) {
	switch {
	case errors.Is(err, taskgraph.ErrTaskNotFound):
		return http.StatusNotFound, "task_not_found", "task does not exist"
	case errors.Is(err, contextstate.ErrIdempotencyConflict):
		return http.StatusConflict, "idempotency_conflict", "Idempotency-Key was used for different content"
	case errors.Is(err, contextstate.ErrReferenceConflict):
		return http.StatusConflict, "context_reference_conflict", "context page is already attached with different flags"
	case errors.Is(err, contextstate.ErrPreconditionFailed):
		return http.StatusPreconditionFailed, "manifest_precondition_failed", "context manifest has changed"
	case errors.Is(err, contextstate.ErrScopeForbidden):
		return http.StatusForbidden, "context_scope_forbidden", "context visibility is not permitted"
	case errors.Is(err, contextstate.ErrPageTooLarge):
		return http.StatusRequestEntityTooLarge, "context_content_too_large", "context content exceeds the page byte limit"
	case errors.Is(err, contextstate.ErrInvalidContent):
		return http.StatusUnprocessableEntity, "invalid_context", "context content is invalid"
	case errors.Is(err, contextstate.ErrManifestPageLimit):
		return http.StatusUnprocessableEntity, "context_page_limit", "context manifest page limit exceeded"
	case errors.Is(err, contextstate.ErrPinnedTokenLimit):
		return http.StatusUnprocessableEntity, "pinned_token_limit", "pinned context token limit exceeded"
	case errors.Is(err, contextstate.ErrPageTokenLimit), errors.Is(err, contextstate.ErrManifestTokenLimit),
		errors.Is(err, contextstate.ErrTaskContextDisabled):
		return http.StatusUnprocessableEntity, "context_token_limit", "context token limit exceeded"
	case errors.Is(err, contextstate.ErrInvalidIdempotencyKey):
		return http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be a valid 1-128 byte identifier"
	case errors.Is(err, contextstate.ErrInvalidTaskBinding):
		return http.StatusBadRequest, "invalid_task_binding", "task binding is invalid"
	case errors.Is(err, contextstate.ErrCheckpointCapacity):
		return http.StatusInsufficientStorage, "context_capacity", "context checkpoint capacity is exhausted"
	case errors.Is(err, contextstate.ErrUnavailable), errors.Is(err, contextstate.ErrInvalidCheckpoint),
		errors.Is(err, contextstate.ErrMigrationRequired):
		return http.StatusServiceUnavailable, "context_unavailable", "context service is unavailable"
	default:
		return http.StatusInternalServerError, "context_failed", "context operation failed"
	}
}
