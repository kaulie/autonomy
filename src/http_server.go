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
		{"POST /api/broadcast", s.handleBroadcast},
		{"POST /api/tasks/{taskID}/stop", s.handleStopTask},
		{"GET /api/tasks/{taskID}", s.handleTaskProgress},
		{"GET /api/tasks/{taskID}/agents/{agentID}", s.handleAgentStatus},
		{"GET /api/tasks/{taskID}/agents/{agentID}/events", s.handleAgentStream},
		// The data API: the log of reason turns read as data — the evaluation side
		// (the benchmark tool) reads it here instead of opening the database file
		// (docs/http-api.md「数据 API」). All of it is read-only.
		{"GET /api/tasks", s.handleTaskList},
		{"GET /api/tasks/{taskID}/turns", s.handleTaskTurns},
		{"GET /api/reason-turns", s.handleReasonTurnList},
		{"GET /api/reason-turns/facets", s.handleReasonTurnFacets},
		{"GET /api/reason-turns/{turnID}", s.handleReasonTurn},
		{"GET /api/meta", s.handleMeta},
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

// healthResponse is what a liveness probe answers: this process is up and serving,
// and — because it is the one setting a caller cannot see from the outside — which
// LLM backend it will acquire its agents on, and the model that backend defaults
// to. Every run failing the same way is a question about this pair first: an
// account out of quota is a backend that can be switched (AUTONOMY_LLM_BACKEND,
// docs/llm-backend.md). llm_model is omitted when the backend resolves the model
// itself — a Cline bridge with no AUTONOMY_CLINE_MODEL uses the one saved by
// `cline auth`, which this process does not hold.
type healthResponse struct {
	Status     string `json:"status"`
	LLMBackend string `json:"llm_backend,omitempty"`
	LLMModel   string `json:"llm_model,omitempty"`
	// Turns is how many reason turns the store holds: a cheap way to see over HTTP
	// that the log a probe is talking to is the live one (the failure this API
	// exists to end was a reader holding a stale database file, docs/http-api.md).
	// It is best effort — liveness must not depend on the store being readable — so
	// a runtime that cannot count simply answers without it.
	Turns *int `json:"turns,omitempty"`
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
// service — and which LLM backend this runtime runs acquired agents on, so "which
// one am I on, and did the switch take?" is answerable over HTTP instead of from
// the process's environment.
//
// @Summary  健康检查（含当前 LLM 后端）
// @Tags     system
// @Produce  json
// @Success  200  {object}  healthResponse
// @Router   /health [get]
func (s *HTTPServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	backend := defaultAgentBackend()
	resp := healthResponse{
		Status:     "ok",
		LLMBackend: string(backend),
		LLMModel:   defaultAgentModel(backend),
	}
	if turns, err := s.Autonomy.TurnCount(); err == nil {
		resp.Turns = &turns
	}
	writeJSON(w, http.StatusOK, resp)
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

// handleBroadcast delivers one message to many agents at once: the agents of one
// project, or of every project. Every target gets the instruction message an
// accept would give it (one per target's task), and the answer is one line per
// target — delivered, or why it was not (docs/broadcast.md).
//
// @Summary  广播一条消息给一批 agent（某个 project 下的，或所有 project 下的）
// @Tags     agents
// @Accept   json
// @Produce  json
// @Param    request  body      autonomy.BroadcastRequest   true  "说的话与范围（project_id 指定一个 project，或 all_projects=true 所有 project）"
// @Success  200      {object}  autonomy.BroadcastResponse  "每个目标一条结果：delivered / skipped / failed，以及消息落在那只 agent 队列的哪里"
// @Failure  400      {object}  errResponse                 "请求不是合法 JSON，内容为空，或没说清范围（两样都写 / 两样都没写）"
// @Router   /api/broadcast [post]
func (s *HTTPServer) handleBroadcast(w http.ResponseWriter, req *http.Request) {
	var body BroadcastRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	resp, err := s.Autonomy.Broadcast(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
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
// A tool message's call (name / args / call_id) travels in normalized_content, and the
// runs this page touches travel with it as turns[] — a run's terminus is its own header
// (duration, status, error), so a timeline does not have to infer it from the last
// message it happens to see. An unknown task or agent is a 404 (there is no such
// conversation to poll); an agent bound to another task is a 400, as in /agents/{id}.
//
// @Summary  轮询对话流（增量）
// @Tags     agents
// @Produce  json
// @Param    taskID                  path      string  true  "task id"
// @Param    agentID                 path      integer true  "agent id（tasks.agent_id）"
// @Param    last_synced_message_seq  query     integer false "上次同步到的 message_seq（llm_messages.id）；首次传 0"
// @Success  200                     {object}  autonomy.AgentStreamResponse  "events（带 message_seq 与 normalized_content）+ turns（本页涉及的 run 头）+ next_poll_after_seq"
// @Failure  400                     {object}  errResponse                    "agent_id / last_synced_message_seq 不合法，或该 agent 属于别的 task"
// @Failure  404                     {object}  errResponse                    "没有这条 task，或这只 agent"
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
	switch {
	case errors.Is(err, errTaskNotFound), errors.Is(err, errAgentNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, errAgentOffTask):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stream)
}

// handleReasonTurnList is the log read: the reason turns, filtered, ordered and
// paged, for a list page or a whole-log search (docs/http-api.md「数据 API」). It is
// the endpoint the evaluation side reads the log through instead of mounting
// autonomy's database file, so the filters answer exactly what the facets offer and
// `total` is the count of the *filtered* rows — not of the table, and not of the
// page. `preview=1` cuts input / output down: a list page does not need whole
// prompts, and 50 of them are megabytes.
//
// @Summary  列 reason turn（过滤 / 排序 / 分页 / 预览）
// @Tags     data
// @Produce  json
// @Param    task_id   query     string  false  "精确匹配 task_id"
// @Param    mode      query     string  false  "精确匹配 mode（plan / agent）"
// @Param    model     query     string  false  "精确匹配 model"
// @Param    status    query     string  false  "精确匹配 status（finished / error / …）"
// @Param    agent     query     string  false  "精确匹配 agent 名字（agents.name，不是 id）"
// @Param    q         query     string  false  "在 input / raw_output 里按字面匹配（% 与 _ 是普通字符）"
// @Param    order     query     string  false  "id（默认）/ created_at / duration_ms / total_tokens；其他值回落 id"
// @Param    dir       query     string  false  "desc（默认）/ asc"
// @Param    limit     query     integer false  "每页条数（默认 50，上限 500）"
// @Param    offset    query     integer false  "跳过多少条（默认 0）"
// @Param    preview   query     boolean false  "只回 input / output 的前 truncate 个字符"
// @Param    truncate  query     integer false  "preview 时每种文本的字符数（默认 400）"
// @Success  200       {object}  autonomy.ReasonTurnListResponse  "turns + total（过滤后的总数）+ 实际生效的 limit / offset"
// @Failure  500       {object}  errResponse                      "store 读不了这份日志"
// @Router   /api/reason-turns [get]
func (s *HTTPServer) handleReasonTurnList(w http.ResponseWriter, req *http.Request) {
	query := req.URL.Query()
	// The search term is not trimmed: a space someone typed is a character someone
	// searched for. Everything else is an identifier, and identifiers travel
	// without padding.
	q := TurnQuery{
		TaskID: strings.TrimSpace(query.Get("task_id")),
		Mode:   ReasonMode(strings.TrimSpace(query.Get("mode"))),
		Model:  strings.TrimSpace(query.Get("model")),
		Status: strings.TrimSpace(query.Get("status")),
		Agent:  strings.TrimSpace(query.Get("agent")),
		Search: query.Get("q"),
		Order:  query.Get("order"),
		// The direction is the store's call: anything that is not "asc" reads as
		// the default, newest first.
		Dir:    query.Get("dir"),
		Limit:  turnListLimit(query.Get("limit")),
		Offset: queryInt(query.Get("offset"), 0, 0, 0),
	}
	turns, total, err := s.Autonomy.ListReasonTurns(q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if preview, chars := previewRequested(query); preview {
		turns = previewTurns(turns, chars)
	}
	if turns == nil {
		turns = []TurnRecord{}
	}
	writeJSON(w, http.StatusOK, ReasonTurnListResponse{Turns: turns, Total: total, Limit: q.Limit, Offset: q.Offset})
}

// handleReasonTurn is one turn in full: the whole prompt and the whole output, which
// is what a detail page shows (and what the list endpoint's preview does not).
//
// @Summary  单条 reason turn（全文）
// @Tags     data
// @Produce  json
// @Param    turnID  path      integer true  "reason_turns.id"
// @Success  200     {object}  autonomy.TurnRecord  "prompt / 原文输出 / 归一化输出 / 用量 / 时间"
// @Failure  400     {object}  errResponse          "turnID 不是数字"
// @Failure  404     {object}  errResponse          "没有这条 turn"
// @Failure  500     {object}  errResponse          "store 读不了这条 turn"
// @Router   /api/reason-turns/{turnID} [get]
func (s *HTTPServer) handleReasonTurn(w http.ResponseWriter, req *http.Request) {
	turnID, err := strconv.ParseInt(req.PathValue("turnID"), 10, 64)
	if err != nil || turnID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid turn id")
		return
	}
	turn, err := s.Autonomy.ReasonTurn(turnID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if turn == nil {
		writeErr(w, http.StatusNotFound, "turn not found")
		return
	}
	writeJSON(w, http.StatusOK, turn)
}

// handleReasonTurnFacets is the filter bar: every value each filter accepts, with
// how many turns carry it. All six facets in one answer, because the bar renders
// them together and six round trips to draw one form is five too many.
//
// @Summary  筛选下拉的取值（去重 + 计数）
// @Tags     data
// @Produce  json
// @Success  200  {object}  autonomy.TurnFacets  "tasks / agents / modes / models / providers / statuses，各按条数降序"
// @Failure  500  {object}  errResponse          "store 读不了这份日志"
// @Router   /api/reason-turns/facets [get]
func (s *HTTPServer) handleReasonTurnFacets(w http.ResponseWriter, _ *http.Request) {
	facets, err := s.Autonomy.ReasonTurnFacets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, facets)
}

// handleTaskList is the read side of the path POST /api/tasks writes to: the tasks
// that have a row, and the ones that only ever appear in the log, each with how many
// turns it has and when it last ran. It is the comparison page's selector — the
// candidates come from both places because a task whose row is gone still has turns
// worth comparing.
//
// @Summary  任务列表（对比页的选择器）
// @Tags     data
// @Produce  json
// It is also what the control plane's "Autonomy tasks" page reads, so `?project_id=`
// keeps the rows whose task definition names that project (`tasks.context_ref`): an id
// nobody ever accepted under is a `200` with an empty array, not a 404, because "that
// project has no tasks" is a fact about the filter, not about the request. With the
// parameter omitted the list is everything. The filter is applied here rather than in
// SQL so both engines answer it identically — a task that only ever appears in the log
// has no definition, so no project, so it is not one of that project's tasks.
//
// @Summary  任务列表（对比页的选择器 / 控制面 Autonomy 任务页；可按 project 过滤）
// @Tags     data
// @Produce  json
// @Param    project_id  query     string  false  "只返回这个 project 的 task（省略 = 全部；未知 id = 空数组）"
// @Success  200         {object}  autonomy.TaskOptionListResponse  "每个 task 一行：id / description / status / turns / last_at + project_id / agent_id / updated_at"
// @Failure  500         {object}  errResponse                      "store 读不了任务与日志"
// @Router   /api/tasks [get]
func (s *HTTPServer) handleTaskList(w http.ResponseWriter, req *http.Request) {
	tasks, err := s.Autonomy.TaskOptions()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if projectID := strings.TrimSpace(req.URL.Query().Get("project_id")); projectID != "" {
		filtered := make([]TaskOption, 0, len(tasks))
		for _, task := range tasks {
			if task.ProjectID == projectID {
				filtered = append(filtered, task)
			}
		}
		tasks = filtered
	}
	if tasks == nil {
		tasks = []TaskOption{}
	}
	writeJSON(w, http.StatusOK, TaskOptionListResponse{Tasks: tasks})
}

// handleTaskTurns is one task's execution series: its turns in the order they ran,
// which is the order a comparison page lines two runs up in — not id, and not
// newest-first, because "what happened, in order" is the question here. The series
// is read in full (no preview): comparing two runs needs the text, not its opening.
//
// @Summary  一个 task 的执行序列（按执行顺序）
// @Tags     data
// @Produce  json
// @Param    taskID  path      string  true  "task id"
// @Param    limit   query     integer false "最多几条（默认 1000，上限 1000）"
// @Success  200     {object}  autonomy.TaskTurnListResponse  "turns（created_at 升序，全文）+ total + capped（是否被 limit 截断）"
// @Failure  500     {object}  errResponse                    "store 读不了这个 task 的日志"
// @Router   /api/tasks/{taskID}/turns [get]
func (s *HTTPServer) handleTaskTurns(w http.ResponseWriter, req *http.Request) {
	taskID := req.PathValue("taskID")
	limit := queryInt(req.URL.Query().Get("limit"), DefaultTaskTurnLimit, 1, MaxTaskTurnLimit)
	turns, total, err := s.Autonomy.TaskTurns(taskID, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if turns == nil {
		turns = []TurnRecord{}
	}
	writeJSON(w, http.StatusOK, TaskTurnListResponse{
		TaskID: taskID,
		Turns:  turns,
		Total:  total,
		Capped: len(turns) < total,
	})
}

// handleMeta is the runtime describing itself to a data consumer: which build
// answered, what the turn columns are called, whether tasks have definitions, and
// how big the log is. It is what saves a consumer from probing the schema — the
// compatibility code that used to live in the reader this API replaces.
//
// The column names are this runtime's own (the engine's DDL, already migrated from
// the older `step` / `output`: src/sqlite_store.go), and has_tasks_table is true
// because that schema always has one. They are answers about *this* service, which
// is the point: nobody outside has to know how the file is shaped.
//
// @Summary  能力与版本自述（省掉 schema 探测）
// @Tags     data
// @Produce  json
// @Success  200  {object}  autonomy.MetaResponse  "service / version / reason_turns 列名 / has_tasks_table / turns 条数"
// @Failure  500  {object}  errResponse             "store 读不了日志条数"
// @Router   /api/meta [get]
func (s *HTTPServer) handleMeta(w http.ResponseWriter, _ *http.Request) {
	turns, err := s.Autonomy.TurnCount()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, MetaResponse{
		Service:       "autonomy",
		Version:       serviceVersion(),
		ReasonTurns:   ReasonTurnColumns{CycleColumn: "cycle", RawOutputColumn: "raw_output"},
		HasTasksTable: true,
		Turns:         turns,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errResponse{Error: msg})
}
