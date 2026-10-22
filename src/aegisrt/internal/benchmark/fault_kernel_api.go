package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"time"

	"aegisrt/internal/domain"
)

const (
	faultKernelAPIResponseLimit  = 256 << 10
	faultKernelAPIRequestTimeout = 750 * time.Millisecond
	faultKernelAPIDialTimeout    = 250 * time.Millisecond
	faultKernelAPIMaxTasks       = 256
)

var (
	errFaultKernelAPIConfiguration = errors.New("fault-kernel runtime client configuration is invalid")
	errFaultKernelAPIRequest       = errors.New("fault-kernel runtime request failed")
	errFaultKernelAPIResponse      = errors.New("fault-kernel runtime response is invalid")
	errFaultKernelAPITaskGraph     = errors.New("fault-kernel task graph is invalid")
)

type faultKernelRuntimeClient interface {
	Ready(context.Context) error
	SubmitTargetAndSurvivor(context.Context, domain.TaskSpec, domain.TaskSpec) error
	TaskStatuses(context.Context) (map[string]domain.TaskStatus, error)
	OutputArtifactCount(context.Context, string) (int, error)
	Close()
}

type faultKernelAPIClient struct {
	baseURL    string
	token      []byte
	httpClient *http.Client
}

var _ faultKernelRuntimeClient = (*faultKernelAPIClient)(nil)

func newFaultKernelAPIClient(address string, token []byte) (*faultKernelAPIClient, error) {
	parsed, err := netip.ParseAddrPort(address)
	if err != nil || parsed.Port() == 0 || !parsed.Addr().IsLoopback() || parsed.Addr().Zone() != "" || !validBearerToken(token) {
		return nil, errFaultKernelAPIConfiguration
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: faultKernelAPIDialTimeout}).DialContext,
		DisableCompression:     true,
		MaxIdleConns:           2,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        5 * time.Second,
		ResponseHeaderTimeout:  faultKernelAPIRequestTimeout / 2,
		MaxResponseHeaderBytes: 16 << 10,
	}
	return &faultKernelAPIClient{
		baseURL: "http://" + address,
		token:   append([]byte(nil), token...),
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   faultKernelAPIRequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (client *faultKernelAPIClient) Ready(ctx context.Context) error {
	var response struct {
		Status *string `json:"status"`
	}
	if err := client.doJSON(ctx, http.MethodGet, "/readyz", nil, false, http.StatusOK, &response); err != nil {
		return err
	}
	if response.Status == nil || *response.Status != "ready" {
		return errFaultKernelAPIResponse
	}
	return nil
}

func (client *faultKernelAPIClient) SubmitTargetAndSurvivor(
	ctx context.Context, target, survivor domain.TaskSpec,
) error {
	if target.ID == survivor.ID || !validFaultKernelAPITask(target) || !validFaultKernelAPITask(survivor) {
		return errFaultKernelAPITaskGraph
	}
	request := faultKernelPatchRequest{Tasks: []faultKernelTaskPayload{
		newFaultKernelTaskPayload(target), newFaultKernelTaskPayload(survivor),
	}}
	var response faultKernelTasksResponse
	if err := client.doJSON(ctx, http.MethodPost, "/v1/task-graphs/patches", request, true,
		http.StatusCreated, &response); err != nil {
		return err
	}
	statuses, err := decodeFaultKernelTaskStatuses(response)
	if err != nil {
		return err
	}
	if _, exists := statuses[target.ID]; !exists {
		return errFaultKernelAPIResponse
	}
	if _, exists := statuses[survivor.ID]; !exists {
		return errFaultKernelAPIResponse
	}
	return nil
}

func (client *faultKernelAPIClient) TaskStatuses(ctx context.Context) (map[string]domain.TaskStatus, error) {
	var response faultKernelTasksResponse
	if err := client.doJSON(ctx, http.MethodGet, "/v1/tasks", nil, true, http.StatusOK, &response); err != nil {
		return nil, err
	}
	return decodeFaultKernelTaskStatuses(response)
}

func (client *faultKernelAPIClient) Close() {
	if client == nil {
		return
	}
	for index := range client.token {
		client.token[index] = 0
	}
	client.token = nil
	if client.httpClient != nil {
		client.httpClient.CloseIdleConnections()
	}
}

func (client *faultKernelAPIClient) doJSON(
	ctx context.Context, method, path string, input any, authenticated bool, expectedStatus int, output any,
) error {
	if ctx == nil || client == nil || client.httpClient == nil || authenticated && len(client.token) == 0 {
		return errFaultKernelAPIConfiguration
	}
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return errFaultKernelAPITaskGraph
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return errFaultKernelAPIRequest
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+string(client.token))
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errFaultKernelAPIRequest
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, faultKernelAPIResponseLimit+1))
	if readErr != nil || len(payload) > faultKernelAPIResponseLimit || response.StatusCode != expectedStatus {
		return errFaultKernelAPIResponse
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errFaultKernelAPIResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return errFaultKernelAPIResponse
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errFaultKernelAPIResponse
	}
	return nil
}

type faultKernelPatchRequest struct {
	Tasks []faultKernelTaskPayload `json:"tasks"`
}

type faultKernelTaskPayload struct {
	ID             string                `json:"id"`
	WorkflowID     string                `json:"workflow_id"`
	AgentID        string                `json:"agent_id"`
	Goal           string                `json:"goal"`
	Dependencies   []string              `json:"dependencies,omitempty"`
	Priority       int                   `json:"priority"`
	Resources      domain.ResourceVector `json:"resources"`
	MaxAttempts    int                   `json:"max_attempts"`
	TimeoutMillis  int64                 `json:"timeout_ms"`
	IdempotencyKey string                `json:"idempotency_key"`
}

type faultKernelTaskView struct {
	Task   *faultKernelTaskResponse `json:"task"`
	Status *domain.TaskStatus       `json:"status"`
}

type faultKernelTaskResponse struct {
	ID             *string                `json:"id"`
	WorkflowID     *string                `json:"workflow_id"`
	AgentID        *string                `json:"agent_id"`
	Goal           *string                `json:"goal"`
	Dependencies   []string               `json:"dependencies,omitempty"`
	Priority       *int                   `json:"priority"`
	Resources      *domain.ResourceVector `json:"resources"`
	MaxAttempts    *int                   `json:"max_attempts"`
	TimeoutMillis  *int64                 `json:"timeout_ms"`
	IdempotencyKey *string                `json:"idempotency_key"`
}

type faultKernelTasksResponse struct {
	Tasks *[]faultKernelTaskView `json:"tasks"`
}

func newFaultKernelTaskPayload(task domain.TaskSpec) faultKernelTaskPayload {
	return faultKernelTaskPayload{ID: task.ID, WorkflowID: task.WorkflowID, AgentID: task.AgentID, Goal: task.Goal,
		Dependencies: append([]string(nil), task.Dependencies...), Priority: task.Priority, Resources: task.Resources.Clone(),
		MaxAttempts: task.MaxAttempts, TimeoutMillis: task.Timeout.Milliseconds(), IdempotencyKey: task.IdempotencyKey}
}

func newFaultKernelTaskView(task domain.TaskSpec, status domain.TaskStatus) faultKernelTaskView {
	payload := newFaultKernelTaskPayload(task)
	response := &faultKernelTaskResponse{ID: &payload.ID, WorkflowID: &payload.WorkflowID, AgentID: &payload.AgentID,
		Goal: &payload.Goal, Dependencies: payload.Dependencies, Priority: &payload.Priority, Resources: &payload.Resources,
		MaxAttempts: &payload.MaxAttempts, TimeoutMillis: &payload.TimeoutMillis, IdempotencyKey: &payload.IdempotencyKey}
	return faultKernelTaskView{Task: response, Status: &status}
}

func decodeFaultKernelTaskStatuses(response faultKernelTasksResponse) (map[string]domain.TaskStatus, error) {
	if response.Tasks == nil || len(*response.Tasks) > faultKernelAPIMaxTasks {
		return nil, errFaultKernelAPIResponse
	}
	statuses := make(map[string]domain.TaskStatus, len(*response.Tasks))
	for _, view := range *response.Tasks {
		task, err := view.taskSpec()
		if err != nil || view.Status == nil || view.Status.Validate() != nil {
			return nil, errFaultKernelAPIResponse
		}
		if _, duplicate := statuses[task.ID]; duplicate {
			return nil, errFaultKernelAPIResponse
		}
		statuses[task.ID] = *view.Status
	}
	return statuses, nil
}

func (view faultKernelTaskView) taskSpec() (domain.TaskSpec, error) {
	value := view.Task
	if value == nil || value.ID == nil || value.WorkflowID == nil || value.AgentID == nil || value.Goal == nil ||
		value.Priority == nil || value.Resources == nil || value.MaxAttempts == nil || value.TimeoutMillis == nil ||
		value.IdempotencyKey == nil || *value.TimeoutMillis <= 0 || *value.TimeoutMillis > math.MaxInt64/int64(time.Millisecond) {
		return domain.TaskSpec{}, errFaultKernelAPIResponse
	}
	task := domain.TaskSpec{ID: *value.ID, WorkflowID: *value.WorkflowID, AgentID: *value.AgentID, Goal: *value.Goal,
		Dependencies: append([]string(nil), value.Dependencies...), Priority: *value.Priority, Resources: value.Resources.Clone(),
		MaxAttempts: *value.MaxAttempts, Timeout: time.Duration(*value.TimeoutMillis) * time.Millisecond,
		IdempotencyKey: *value.IdempotencyKey}
	if err := task.Validate(); err != nil {
		return domain.TaskSpec{}, errFaultKernelAPIResponse
	}
	return task, nil
}

func validFaultKernelAPITask(task domain.TaskSpec) bool {
	return task.Validate() == nil && task.Timeout.Milliseconds() > 0
}

func validBearerToken(token []byte) bool {
	if len(token) == 0 || len(token) > 4096 {
		return false
	}
	for _, value := range token {
		if value <= 0x20 || value >= 0x7f {
			return false
		}
	}
	return true
}
