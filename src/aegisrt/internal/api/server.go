package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/domain"
	"aegisrt/internal/eventlog"
	"aegisrt/internal/runtimeapp"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/taskgraph"
)

type Control interface {
	ApplyPatch([]domain.TaskSpec, time.Time) error
	Cancel(string) (taskgraph.TaskSnapshot, error)
	Tasks() []taskgraph.TaskSnapshot
	Stats() runtimecore.Stats
}

// ContextControl is deliberately separate from task Control. HTTP handlers
// submit only task-bound content intent; ownership is derived by runtimeapp
// from the admitted immutable task specification.
type ContextControl interface {
	InspectContext(string) (runtimeapp.ContextSnapshot, error)
	AppendContext(context.Context, runtimeapp.AppendContextCommand) (runtimeapp.AppendContextResult, error)
}

// EventSource exposes one immutable event stream. Stream identity must not
// change during the source lifetime; create a new source for every new stream.
type EventSource interface {
	Stream() eventlog.Stream
	Since(uint64, int) (eventlog.Replay, error)
}

type contextStatsProvider interface {
	ContextStats() contextstate.Stats
}

type Config struct {
	Version        string
	StartedAt      time.Time
	Ready          func() bool
	Agents         []domain.AgentSpec
	Control        Control
	ContextControl ContextControl
	Events         EventSource
	AuthToken      []byte
	// Notify must return without blocking. runservice.Service.Notify satisfies
	// this contract by coalescing wakeups in a bounded channel.
	Notify func()
}

type Server struct {
	version        string
	startedAt      time.Time
	ready          func() bool
	agents         []agentDTO
	control        Control
	contextControl ContextControl
	events         EventSource
	eventStream    eventlog.Stream
	authTokenHash  [sha256.Size]byte
	authConfigured bool
	notify         func()
	handler        http.Handler
}

func NewServer(config Config) *Server {
	if config.Version == "" {
		config.Version = "dev"
	}
	eventStream := eventlog.Stream{}
	if config.Events != nil {
		eventStream = config.Events.Stream()
	}
	if validJSONTime(eventStream.StartedAt) {
		config.StartedAt = eventStream.StartedAt
	}
	if !validJSONTime(config.StartedAt) {
		config.StartedAt = time.Now().UTC()
	} else {
		config.StartedAt = config.StartedAt.UTC()
	}
	if config.Ready == nil {
		config.Ready = func() bool { return true }
	}
	server := &Server{
		version:        config.Version,
		startedAt:      config.StartedAt,
		ready:          config.Ready,
		agents:         projectAgents(config.Agents),
		control:        config.Control,
		contextControl: config.ContextControl,
		events:         config.Events,
		eventStream:    eventStream,
		authTokenHash:  sha256.Sum256(config.AuthToken),
		authConfigured: len(config.AuthToken) > 0,
		notify:         config.Notify,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.health)
	mux.HandleFunc("/readyz", server.readiness)
	mux.HandleFunc("/v1/runtime", server.runtimeInfo)
	mux.HandleFunc("/v1/agents", server.listAgents)
	mux.HandleFunc("POST /v1/task-graphs/patches", server.applyTaskGraphPatch)
	mux.HandleFunc("GET /v1/tasks", server.listTasks)
	mux.HandleFunc("POST /v1/tasks/{taskID}/cancel", server.cancelTask)
	mux.HandleFunc("GET /v1/tasks/{taskID}/output-summary", server.inspectOutputSummary)
	mux.HandleFunc("GET /v1/tasks/{taskID}/context", server.inspectContext)
	mux.HandleFunc("POST /v1/tasks/{taskID}/context/pages", server.appendContextPage)
	mux.HandleFunc("/v1/events", server.listEvents)
	server.handler = securityHeaders(mux)
	return server
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) health(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readiness(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	if !s.ready() {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) runtimeInfo(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	response := struct {
		Name         string              `json:"name"`
		Version      string              `json:"version"`
		StartedAt    time.Time           `json:"started_at"`
		Stats        any                 `json:"stats,omitempty"`
		ContextStats *contextstate.Stats `json:"context_stats,omitempty"`
		EventHistory *eventHistoryDTO    `json:"event_history,omitempty"`
	}{
		Name:         "aegisrt",
		Version:      s.version,
		StartedAt:    s.startedAt,
		EventHistory: s.eventHistory(),
	}
	if s.control != nil {
		response.Stats = s.control.Stats()
	}
	if provider, ok := s.contextControl.(contextStatsProvider); ok {
		stats := provider.ContextStats()
		response.ContextStats = &stats
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) listAgents(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	if !s.requireAuthorization(writer, request) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"agents": s.agents})
}

func (s *Server) requireAuthorization(writer http.ResponseWriter, request *http.Request) bool {
	values := request.Header.Values("Authorization")
	candidate := ""
	validFormat := 0
	if len(values) == 1 {
		scheme, value, found := strings.Cut(values[0], " ")
		if found && strings.EqualFold(scheme, "Bearer") && value != "" &&
			!strings.ContainsAny(value, " \t\r\n") {
			candidate = value
			validFormat = 1
		}
	}
	candidateHash := sha256.Sum256([]byte(candidate))
	configured := 0
	if s.authConfigured {
		configured = 1
	}
	if configured&validFormat&subtle.ConstantTimeCompare(candidateHash[:], s.authTokenHash[:]) == 1 {
		return true
	}
	writer.Header().Set("WWW-Authenticate", "Bearer")
	writeAPIError(writer, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
	return false
}

func (s *Server) notifyWork() {
	if s.notify != nil {
		s.notify()
	}
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
	})
}
