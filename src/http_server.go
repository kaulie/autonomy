package autonomy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPServer serves Autonomy's external task API.
//
// Its contract is not a hand-written spec: every route is annotated where it is
// handled (the swag comments below, read by scripts/register-contract.sh → swag
// init → the service registry), and src/contract_test.go keeps those annotations
// and this server's route table one and the same. Nothing here imports swag:
// annotations cost the runtime nothing (docs/http-api.md).
type HTTPServer struct {
	Autonomy *Autonomy
	Mux      *http.ServeMux
}

// httpsRoute is one route this server serves: the pattern the mux is given, and the
// handler for it.
type httpsRoute struct {
	Pattern string
	Handler http.HandlerFunc
}

// routes is every route this server serves, in registration order. It is a table
// because the annotations have to be checked against what is really served
// (src/contract_test.go).
func (s *HTTPServer) routes() []httpsRoute {
	return []httpsRoute{
		{"POST /api/tasks", s.handleAcceptTask},
		{"POST /api/tasks/{taskID}/stop", s.handleStopTask},
		{"GET /api/tasks/{taskID}", s.handleTaskProgress},
		{"GET /api/tasks/{taskID}/agents/{agentID}", s.handleAgentStatus},
		{"GET /api/tasks/{taskID}/agents/{agentID}/events", s.handleAgentStream},
		// /health is the path the deployment platform probes for every service;
		// /healthz stays as an alias for callers that already used it.
		{"GET /health", s.handleHealth},
		{"GET /healthz", s.handleHealthAlias},
	}
}

// NewHTTPServer registers the task / progress / agent / stream routes.
func NewHTTPServer(a *Autonomy) *HTTPServer {
	s := &HTTPServer{Autonomy: a, Mux: http.NewServeMux()}
	for _, route := range s.routes() {
		s.Mux.HandleFunc(route.Pattern, route.Handler)
	}
	return s
}

// Handler returns the root HTTP handler.
func (s *HTTPServer) Handler() http.Handler {
	return s.Mux
}

// ListenAndServe starts the HTTP API on addr (e.g. ":4300", this service's port
// in the deployment contract; scripts/start.sh passes it in AUTONOMY_HTTP_ADDR).
func (s *HTTPServer) ListenAndServe(addr string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("[autonomy] http listening on %s\n", addr)
	return server.ListenAndServe()
}

// healthResponse is what a liveness probe answers: this process is up and serving.
type healthResponse struct {
	Status string `json:"status"`
}

// stopTaskResponse is what a stop answers: the task, and the status it was left in.
type stopTaskResponse struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

// errResponse is the shape of every refusal: the runtime's own words (writeErr).
type errResponse struct {
	Error string `json:"error"`
}

// handleHealth reports liveness — the probe the deployment platform polls for every
// service.
//
// @Summary  健康检查
// @Tags     system
// @Produce  json
// @Success  200  {object}  healthResponse
// @Router   /health [get]
func (s *HTTPServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// handleHealthAlias is the same probe under the name callers used before the
// contract settled on /health.
//
// @Summary  健康检查（/health 的别名）
// @Tags     system
// @Produce  json
// @Success  200  {object}  healthResponse
// @Router   /healthz [get]
func (s *HTTPServer) handleHealthAlias(w http.ResponseWriter, r *http.Request) {
	s.handleHealth(w, r)
}

// handleAcceptTask accepts one instruction: the task it is about, the agent that
// will do it, and where this instruction stands in that agent's queue. It answers as
// soon as the message is queued — the run happens on this runtime — so a busy agent
// is not a refusal (docs/inbox.md).
//
// @Summary  接受一条任务指令
// @Tags     tasks
// @Accept   json
// @Produce  json
// @Param    request  body      autonomy.AcceptTaskRequest  true  "指令与它所属的 task（task_id 可省：运行时自己生成一个）"
// @Success  202      {object}  autonomy.AcceptTaskResponse  "已受理：task / agent / message，以及这条指令前面还有几条"
// @Failure  400      {object}  errResponse                  "请求不是合法 JSON，或这条 task 无法受理"
// @Router   /api/tasks [post]
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

// handleStopTask stops what a task's agent is on right now: the message being
// processed is cancelled, and the stop is recorded as a message in its place in the
// queue (docs/inbox.md).
//
// @Summary  停掉任务正在处理的那条消息
// @Tags     tasks
// @Produce  json
// @Param    taskID  path      string  true  "task id"
// @Success  200     {object}  stopTaskResponse  "已停止：task 与它被留下的状态"
// @Failure  404     {object}  errResponse       "没有这条 task"
// @Failure  409     {object}  errResponse       "task 不在运行中（agent 手上没有正在处理的消息）"
// @Failure  500     {object}  errResponse       "store 读不了这条 task"
// @Router   /api/tasks/{taskID}/stop [post]
func (s *HTTPServer) handleStopTask(w http.ResponseWriter, req *http.Request) {
	task, err := s.Autonomy.StopTask(req.PathValue("taskID"))
	if errors.Is(err, errTaskNotFound) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, errTaskNotRunning) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stopTaskResponse{TaskID: task.ID, Status: task.Status})
}

// handleTaskProgress is the task detail: what the task is — its goal type, the
// context it names, and that context resolved (the project it belongs to and the
// organization that project belongs to, src/context_builder) — plus how far it got,
// plan by plan (docs/http-api.md).
//
// @Summary  任务详情（状态 / 计划 / 所属 project 与组织）
// @Tags     tasks
// @Produce  json
// @Param    taskID  path      string  true  "task id"
// @Success  200     {object}  autonomy.TaskProgress  "状态、错误、每一份 plan 与每一步的执行情况"
// @Failure  404     {object}  errResponse            "没有这条 task"
// @Failure  500     {object}  errResponse            "store 读不了这条 task"
// @Router   /api/tasks/{taskID} [get]
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

// handleAgentStatus reports whether the agent is working, and the provider run it is
// on when it is (docs/agent.md).
//
// @Summary  这只 agent 现在的工作状态
// @Tags     agents
// @Produce  json
// @Param    taskID   path      string  true  "task id"
// @Param    agentID  path      integer true  "agent id（tasks.agent_id）"
// @Success  200      {object}  autonomy.AgentWorkStatus  "state / working，工作时带当前 provider run id"
// @Failure  400      {object}  errResponse               "agent_id 不是数字，或这只 agent 不属于这条 task"
// @Failure  404      {object}  errResponse               "没有这只 agent"
// @Router   /api/tasks/{taskID}/agents/{agentID} [get]
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

// handleAgentStream is the conversation's incremental poll: the messages written
// after a cursor (llm_messages.id), thinking / tool / assistant (docs/llm-message.md).
//
// @Summary  轮询对话流（增量）
// @Tags     agents
// @Produce  json
// @Param    taskID                  path      string  true  "task id"
// @Param    agentID                 path      integer true  "agent id（tasks.agent_id）"
// @Param    last_synced_message_seq  query     integer false "上次同步到的 message_seq（llm_messages.id）；首次传 0"
// @Success  200                     {object}  autonomy.AgentStreamResponse  "events（带 message_seq）与 next_poll_after_seq"
// @Failure  400                     {object}  errResponse                    "agent_id 或 last_synced_message_seq 不合法"
// @Router   /api/tasks/{taskID}/agents/{agentID}/events [get]
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
	writeJSON(w, code, errResponse{Error: msg})
}
