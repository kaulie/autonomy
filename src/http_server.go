package autonomy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPServer serves Autonomy's external task API.
type HTTPServer struct {
	Autonomy *Autonomy
	Mux      *http.ServeMux
}

// NewHTTPServer registers the task / progress / agent / stream routes.
func NewHTTPServer(a *Autonomy) *HTTPServer {
	s := &HTTPServer{Autonomy: a, Mux: http.NewServeMux()}
	s.Mux.HandleFunc("POST /api/tasks", s.handleAcceptTask)
	s.Mux.HandleFunc("GET /api/tasks/{taskID}", s.handleTaskProgress)
	s.Mux.HandleFunc("GET /api/tasks/{taskID}/agents/{agentID}", s.handleAgentStatus)
	s.Mux.HandleFunc("GET /api/tasks/{taskID}/agents/{agentID}/events", s.handleAgentStream)
	// /health is the path the deployment platform probes for every service.
	// /healthz stays as an alias for callers that already used it.
	health := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
	s.Mux.HandleFunc("GET /health", health)
	s.Mux.HandleFunc("GET /healthz", health)
	return s
}

// Handler returns the root HTTP handler.
func (s *HTTPServer) Handler() http.Handler {
	return s.Mux
}

// ListenAndServe starts the HTTP API on addr (e.g. ":4230").
func (s *HTTPServer) ListenAndServe(addr string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("[autonomy] http listening on %s\n", addr)
	return server.ListenAndServe()
}

func (s *HTTPServer) handleAcceptTask(w http.ResponseWriter, req *http.Request) {
	var body AcceptTaskRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	resp, err := s.Autonomy.AcceptTask(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *HTTPServer) handleTaskProgress(w http.ResponseWriter, req *http.Request) {
	taskID := req.PathValue("taskID")
	progress, err := s.Autonomy.TaskProgress(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if progress == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

func (s *HTTPServer) handleAgentStatus(w http.ResponseWriter, req *http.Request) {
	taskID := req.PathValue("taskID")
	agentID, err := strconv.ParseInt(req.PathValue("agentID"), 10, 64)
	if err != nil || agentID == 0 {
		writeErr(w, http.StatusBadRequest, "invalid agent_id")
		return
	}
	status, err := s.Autonomy.AgentStatus(taskID, agentID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if status == nil {
		writeErr(w, http.StatusNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *HTTPServer) handleAgentStream(w http.ResponseWriter, req *http.Request) {
	taskID := req.PathValue("taskID")
	agentID, err := strconv.ParseInt(req.PathValue("agentID"), 10, 64)
	if err != nil || agentID == 0 {
		writeErr(w, http.StatusBadRequest, "invalid agent_id")
		return
	}
	after := int64(0)
	if raw := strings.TrimSpace(req.URL.Query().Get("last_synced_message_seq")); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			writeErr(w, http.StatusBadRequest, "invalid last_synced_message_seq")
			return
		}
	}
	stream, err := s.Autonomy.AgentStream(taskID, agentID, after)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stream)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
