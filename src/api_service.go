package autonomy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// AcceptTaskRequest is the HTTP body for creating a task.
type AcceptTaskRequest struct {
	ID          string            `json:"task_id,omitempty"`
	Description string            `json:"description"`
	Domain      string            `json:"domain,omitempty"`
	GoalType    string            `json:"goal_type,omitempty"`
	ContextRef  map[string]string `json:"context_ref,omitempty"`
	// Mode is how a follow-up message is handled: "command" (default) is a
	// normal instruction that may replan; "chat" talks to the planner and
	// must not change an already written plan; "approve" confirms a plan the
	// agent is waiting on (same as Approve).
	Mode string `json:"mode,omitempty"`
	// Approve confirms a plan the agent is waiting on (Agent.RequirePlanApproval):
	// it makes this message a confirmation (inbox kind = approval), so the run
	// releases the plan it paused on (status awaiting_approval) and implements it —
	// implementation starts only after the plan is approved.
	Approve bool `json:"approve,omitempty"`
	// AccountID names the pool account this task's agent runs on (src/accounts.go):
	// the harness, vendor, model, workspace root and credential all come from it. Empty
	// leaves the choice to the pool (the harness's default account). It is how a caller
	// configures which account an agent works with, per task.
	AccountID string `json:"account_id,omitempty"`
}

// AcceptTaskResponse is returned as soon as the instruction is accepted: its task
// exists, its agent is known, and the instruction is a message in that agent's
// inbox, waiting its turn (src/inbox.go).
type AcceptTaskResponse struct {
	TaskID  string `json:"task_id"`
	AgentID int64  `json:"agent_id"`
	Status  string `json:"status"`
	// MessageID is the inbox message this instruction was accepted as, and Queued
	// is how many messages the agent still has in front of it: the ones that arrived
	// before this instruction and have not finished, including the one being
	// processed right now. 0 means this instruction is what the agent is doing, or is
	// about to take next; n means n messages come first.
	MessageID int64 `json:"message_id,omitempty"`
	Queued    int   `json:"queued"`
}

// TaskProgress is a snapshot of how far a task has got.
type TaskProgress struct {
	TaskID      string    `json:"task_id"`
	Description string    `json:"description"`
	Domain      string    `json:"domain"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	AgentID     int64     `json:"agent_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// What the task is: the goal type it was accepted as, the context references it
	// carries, and that context resolved — the project it belongs to and the
	// organization that project belongs to (TaskProject, src/task_project.go).
	GoalType   string             `json:"goal_type,omitempty"`
	ContextRef map[string]string  `json:"context_ref,omitempty"`
	Project    *TaskProject       `json:"project,omitempty"`
	Plans      []TaskPlanProgress `json:"plans"`
	// Verification is the engine's own side of "is it done?": the Completion Contract
	// the first answer pinned and every verdict judged against it since
	// (docs/verification.md). It is the answer to "why does this task say unverified",
	// which a status word alone cannot carry.
	//
	// Absent (not empty) for a task that never verified anything — accepted but never
	// run, still planning, or run before the contract was pinned. An absent field is
	// the honest shape there; `{"contract":[],"verdicts":[]}` would claim the engine
	// looked and found nothing.
	Verification *TaskVerificationProgress `json:"verification,omitempty"`
	// State is where the task stands: its newest round, what the contract still misses,
	// and — when the runtime is what stopped the run — that it was cut and where
	// (src/task_record.go, the same object a continuing run gets as its briefing's
	// state). It answers "was this stopped by a person or by a restart, and what is
	// left?" without the reader having to reconstruct it from the plans below.
	State *TaskState `json:"state,omitempty"`
}

// TaskVerificationProgress is a task's completion contract and its verdict log.
type TaskVerificationProgress struct {
	// Contract is the pinned contract in criterion order: `[]` when nothing was ever
	// pinned (a `done` then has nothing to be verified against — which is itself a
	// verdict the verdict log records).
	Contract []TaskContractCriterion `json:"contract"`
	// Verdicts is every verdict, oldest first, one row per criterion per judged cycle.
	Verdicts []TaskVerdictProgress `json:"verdicts"`
}

// TaskContractCriterion is one fact that must hold for the task to be done. Criterion
// is the JSON the first answer declared, verbatim — the words the task is judged by,
// kept raw so a reader sees the requirement, its evidence slot and its expectation the
// way they were written rather than through this side's idea of them.
type TaskContractCriterion struct {
	Idx       int             `json:"idx"`
	Name      string          `json:"name,omitempty"`
	Criterion json.RawMessage `json:"criterion,omitempty" swaggertype:"object"`
}

// TaskVerdictProgress is one verdict on one criterion: what the contract said must
// hold, what the authoritative source answered, and where that answer came from.
//
// Evidence is the slot the criterion bound and what it resolved to, as JSON
// (`{"slot": …, "reference": …}`); Method is who was asked
// (`world_model` / `registry:<capability>` / `declared:<capability>` / `-` for "nobody
// authoritative exists, so this cannot be verified"). Result is pass | fail |
// inconclusive — only pass holds a `done` up.
type TaskVerdictProgress struct {
	ID        int64     `json:"id"`
	PlanID    int64     `json:"plan_id,omitempty"`
	Cycle     int       `json:"cycle"`
	Criterion string    `json:"criterion,omitempty"`
	Result    string    `json:"result"`
	Method    string    `json:"method,omitempty"`
	Evidence  string    `json:"evidence,omitempty"`
	Expected  string    `json:"expected,omitempty"`
	Observed  string    `json:"observed,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// TaskPlanProgress summarises one decision cycle's plan and its execution.
type TaskPlanProgress struct {
	PlanID       int64                  `json:"plan_id"`
	Cycle        int                    `json:"cycle"`
	DecisionType string                 `json:"decision_type"`
	Reason       string                 `json:"reason,omitempty"`
	Need         string                 `json:"need,omitempty"`
	StepCount    int                    `json:"step_count"`
	Executed     int                    `json:"executed"`
	Outcome      string                 `json:"outcome,omitempty"`
	Steps        []TaskPlanStepProgress `json:"steps"`
}

// TaskPlanStepProgress is one planned step and, when it has run, what happened.
// Status is pending until an execution_step row exists, then that row's ok|failed.
//
// The three raw fields are arbitrary JSON (a step's planned input, and what it was
// actually called with and answered). swaggertype says so to swag, which cannot infer
// a definition for json.RawMessage; the tag is inert at runtime — nothing imports swag.
type TaskPlanStepProgress struct {
	Idx            int             `json:"idx"`
	Name           string          `json:"name,omitempty"`
	Capability     string          `json:"capability,omitempty"`
	ExpectedEffect string          `json:"expected_effect,omitempty"`
	PlannedInput   json.RawMessage `json:"planned_input,omitempty" swaggertype:"object"`
	Status         string          `json:"status"`
	Input          json.RawMessage `json:"input,omitempty" swaggertype:"object"`
	Output         json.RawMessage `json:"output,omitempty" swaggertype:"object"`
	Error          string          `json:"error,omitempty"`
	DurationMS     int64           `json:"duration_ms,omitempty"`
}

// AgentWorkStatus is the live status of one agent on a task.
type AgentWorkStatus struct {
	TaskID      string `json:"task_id"`
	AgentID     int64  `json:"agent_id"`
	Name        string `json:"name,omitempty"`
	State       string `json:"state"`
	Working     bool   `json:"working"`
	AgentRunID  string `json:"agent_run_id,omitempty"`
	TurnID      int64  `json:"turn_id,omitempty"`
	Cycle       int    `json:"cycle,omitempty"`
	LLMProvider string `json:"llm_provider,omitempty"`
	Model       string `json:"model,omitempty"`
	// Account says which pool entry this agent's credentials come from
	// (src/accounts.go): a status that names the model but not who pays for it is
	// half an answer.
	Account string `json:"account,omitempty"`
}

// StreamEvent is one incremental conversation item for poll clients.
// MessageSeq is llm_messages.id — the monotonic cursor across turns (per-turn
// seq resets every run and is exposed separately as TurnSeq).
//
// Content is the message's text: an input prompt, a thinking block, an assistant
// return, or — for a tool message — the tool's *result*. A tool message's call is in
// NormalizedContent, which is where the name, the arguments and the call id live
// (src/llm_message.go): one row carries both halves of one call, and a consumer that
// wants to render "tool(name, args) → result" needs both fields.
type StreamEvent struct {
	MessageSeq int64          `json:"message_seq"`
	TurnID     int64          `json:"turn_id"`
	TurnSeq    int            `json:"turn_seq"`
	Cycle      int            `json:"cycle"`
	Role       LLMMessageRole `json:"role"`
	Content    string         `json:"content"`
	// NormalizedContent is the message's structured side: for a tool message the
	// call — `{"name": …, "call_id": …, "args": {…}}` — and for a thinking block how
	// long the model thought (`{"duration_ms": …}`), both as the JSON text the runtime
	// derived (src/llm_message.go). Empty when the message has no structured side.
	NormalizedContent string `json:"normalized_content,omitempty"`
	// Model / Provider are the run's own backend, repeated on each of its messages so
	// a timeline can label a run without a second read.
	Model     string    `json:"model,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	RunID     string    `json:"run_id,omitempty"`
	Status    string    `json:"status,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AgentRunSummary is one run of the stream, as its header has it: which turn it is,
// which model answered, how long it took and how it ended. A timeline groups the events
// below by TurnID and closes each group with this — the run's terminus is the run's own
// result (reason_turns.status / duration_ms / error_message), not something a reader has
// to infer from the last message it happens to see.
type AgentRunSummary struct {
	TurnID       int64  `json:"turn_id"`
	Cycle        int    `json:"cycle"`
	Mode         string `json:"mode,omitempty"`
	Model        string `json:"model,omitempty"`
	Provider     string `json:"provider,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	Status       string `json:"status,omitempty"`
	DurationMS   int64  `json:"duration_ms,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	EndedAt      string `json:"ended_at,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// AgentStreamResponse is an incremental poll of an agent's conversation stream.
type AgentStreamResponse struct {
	TaskID  string        `json:"task_id"`
	AgentID int64         `json:"agent_id"`
	Events  []StreamEvent `json:"events"`
	// Turns is the header of every run this page touches, deduplicated, in turn order:
	// a poll that brings a run's last message also brings the run's result, so the
	// consumer can close the group it just rendered. A turn whose header cannot be read
	// is left out — the conversation is what was asked for.
	Turns            []AgentRunSummary `json:"turns"`
	LastMessageSeq   int64             `json:"last_message_seq"`
	NextPollAfterSeq int64             `json:"next_poll_after_seq"`
}

var (
	errTaskNotFound   = errors.New("task not found")
	errTaskNotRunning = errors.New("task is not running")
	// errAgentNotFound / errAgentOffTask are the two reasons an agent does not answer
	// for a task: no such row, or a row that belongs to another task. The handlers read
	// them apart — "no such agent" is a 404, "wrong task" is a 400 — because they are
	// different mistakes.
	errAgentNotFound = errors.New("agent not found")
	errAgentOffTask  = errors.New("agent is bound to another task")
)

// inFlightTasks maps a task that is being processed right now to the cancel func of
// the message its agent is on. It is what POST /api/tasks/{id}/stop cancels, and it
// is written by that message's own processing (Autonomy.processInstruction).
var inFlightTasks sync.Map

// AcceptTask accepts one instruction: the task it is about is written as pending,
// its agent is resolved (resumed, after a restart — see resumeAgentForTask), and
// the instruction becomes a message in that agent's inbox. The agent's own consumer
// processes it, one message at a time, in the order the messages arrived — so an
// instruction that arrives while the agent is busy is accepted and queued behind
// what it is doing, instead of being refused.
//
// It is HTTP's door (POST /api/tasks) into accept, and the only thing this door adds
// to it is the decision not to wait: the request is answered as soon as the message
// is in the queue. Run is the other door on the same accept path, and it waits.
func (r *Autonomy) AcceptTask(req AcceptTaskRequest) (*AcceptTaskResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("autonomy not initialized")
	}
	task, agent, msg, err := r.accept(req)
	if err != nil {
		return nil, err
	}
	id, err := r.agentInbox().Enqueue(agent, msg)
	if err != nil {
		return nil, err
	}
	return r.accepted(task, agent, id), nil
}

// Run runs one instruction for a task to completion, in-process. It is the
// programmatic door onto the very thing POST /api/tasks accepts: the same request
// (AcceptTaskRequest — what to do, and optionally which task it is), through the
// same accept path (accept), becoming the same message in the same agent's inbox.
// The difference is only what the caller wants back: HTTP answers with the
// acceptance, Run accepts with a receipt and waits for the run that message became,
// returning how it ended — the error Run has always returned (nil when nothing
// failed, with the task's outcome on its row), alongside the same acceptance HTTP
// answers with.
//
// Nothing about the run is special because it came from here: it is the same
// message, on the same session, going through the same loop as one over HTTP.
func (r *Autonomy) Run(req AcceptTaskRequest) (*AcceptTaskResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("autonomy not initialized")
	}
	task, agent, msg, err := r.accept(req)
	if err != nil {
		return nil, err
	}
	receipt, err := r.agentInbox().Accept(context.Background(), agent, msg)
	if err != nil {
		return nil, err
	}
	// The wait is the only difference from HTTP's door: Run asks for the run, not
	// for a receipt, so it comes back when this message has been processed.
	_, runErr := receipt.Wait()
	return r.accepted(task, agent, receipt.ID()), runErr
}

// accepted describes one acceptance: the task, the agent that will do it, and where
// this instruction stands in that agent's queue. Both doors answer with it.
func (r *Autonomy) accepted(task *Task, agent *Agent, messageID int64) *AcceptTaskResponse {
	return &AcceptTaskResponse{
		TaskID:    task.ID,
		AgentID:   agent.ID,
		Status:    task.Status,
		MessageID: messageID,
		Queued:    r.agentInbox().Ahead(agent, messageID),
	}
}

// accept is the one accept path: the request becomes the task (its own id or a new
// one, description, domain, goal, context references — an existing task continued
// rather than recreated) and the instruction message its agent will process, with
// the agent resolved and resumed (see resumeAgentForTask). A door then decides what
// to do with the message: AcceptTask queues it and answers, Run queues it and waits.
func (r *Autonomy) accept(req AcceptTaskRequest) (*Task, *Agent, AgentMessage, error) {
	desc := strings.TrimSpace(req.Description)
	taskID := strings.TrimSpace(req.ID)
	if taskID == "" {
		taskID = newTaskID()
	}

	domain := TaskDomainSoftwareDevelopment
	if d := strings.TrimSpace(req.Domain); d != "" {
		if converted, err := ConvertToTaskDomain(d); err == nil {
			domain = converted
		} else {
			domain = TaskDomain(d)
		}
	}
	goalType := GoalType(strings.TrimSpace(req.GoalType))
	if goalType == "" {
		goalType = GoalType_FEATURE
	}
	contextRef := map[ContextContainerType]string{}
	for k, v := range req.ContextRef {
		contextRef[ContextContainerType(k)] = v
	}

	// A second instruction for a task the store already knows continues that task
	// rather than starting a new one: it keeps the row's own identity (when the task
	// was created, and the description/domain/goal/context it was accepted with when
	// this request does not restate them) and, above all, the agent it was paired
	// with — because that is the agent this instruction resumes (see
	// resumeAgentForTask).
	//
	// What the task *is* is on its row, so a request that names no world is not a
	// request to leave the world behind: goal_type and context_ref are read back
	// with the row (src/sqlite_query.go) and carried here when the request has none
	// of its own. A request that carries no words of its own changes nothing either:
	// what is being asked *now* is the message, and a message that says nothing new
	// is the row's own description (see instruction).
	//
	// The read is taken from the writer (writerReads): this request is about to write
	// the row it just read, so it must see the version this runtime can already have
	// written (docs/store.md「读写分离」).
	task := &Task{
		ID:          taskID,
		Description: desc,
		Domain:      domain,
		GoalType:    goalType,
		Status:      TaskStatusPending,
		ContextRef:  contextRef,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	var storedTask *Task
	if stored, err := writerReads(r.taskStore()).GetTask(taskID); err == nil && stored != nil {
		storedTask = stored
		task.CreatedAt = stored.CreatedAt
		task.AgentID = stored.AgentID
		if desc == "" {
			task.Description = stored.Description
		}
		if strings.TrimSpace(req.Domain) == "" {
			task.Domain = stored.Domain
		}
		if strings.TrimSpace(req.GoalType) == "" && strings.TrimSpace(string(stored.GoalType)) != "" {
			task.GoalType = stored.GoalType
		}
		if len(contextRef) == 0 && len(stored.ContextRef) > 0 {
			task.ContextRef = stored.ContextRef
		}
	}
	kind, err := parseUserMessageKind(req.Mode)
	if err != nil {
		return nil, nil, AgentMessage{}, err
	}
	// Approve (either way of saying it) makes this message a confirmation, not an
	// instruction that replans (see AcceptTaskRequest.Approve).
	if req.Approve {
		kind = MessageKindApproval
	}
	// Chat is a conversation with the planner: the task row (description,
	// status, the plan it already has) is not the thing being asked about.
	if kind == MessageKindChat && storedTask != nil {
		task.Description = storedTask.Description
		task.Status = storedTask.Status
		task.Error = storedTask.Error
		task.Domain = storedTask.Domain
		task.GoalType = storedTask.GoalType
		if len(storedTask.ContextRef) > 0 {
			task.ContextRef = storedTask.ContextRef
		}
	}
	agent, msg, err := r.instruction(task, desc, kind)
	if err != nil {
		return nil, nil, AgentMessage{}, err
	}
	// A request may name the account this task runs on. It is validated *here* so a typo is a
	// refused instruction rather than a task that dies at its first cycle, and recorded on the
	// agent row so the run resolves that account (src/agent_account.go).
	if err := r.assignAccount(agent, req.AccountID); err != nil {
		return nil, nil, AgentMessage{}, err
	}
	return task, agent, msg, nil
}

// assignAccount points an agent — and with it every run of that task — at one pool account.
// An empty id changes nothing; a named one must exist and be enabled, because a requested
// account that cannot be honoured is an error, not a reason to quietly run elsewhere and bill
// a different key.
func (r *Autonomy) assignAccount(agent *Agent, accountID string) error {
	id := strings.TrimSpace(accountID)
	if id == "" || agent == nil {
		return nil
	}
	store, err := r.accountStoreOrErr()
	if err != nil {
		return err
	}
	account, err := store.GetAccount(id)
	if err != nil {
		return err
	}
	if account == nil {
		return fmt.Errorf("account %s is not in the pool: add it at /accounts", id)
	}
	if !account.Enabled {
		return fmt.Errorf("account %s (%s) is disabled", account.ID, account.Label)
	}
	if agent.AccountID != account.ID {
		agent.adoptAccount(account)
		agent.Persist()
	}
	return nil
}

func newTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	return "task-" + hex.EncodeToString(b[:])
}

// TaskProgress builds a progress snapshot for a task.
func (r *Autonomy) TaskProgress(taskID string) (*TaskProgress, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	task, err := r.taskStore().GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, nil
	}
	progress := &TaskProgress{
		TaskID:      task.ID,
		Description: task.Description,
		Domain:      string(task.Domain),
		Status:      task.Status,
		Error:       task.Error,
		AgentID:     task.AgentID,
		CreatedAt:   task.CreatedAt,
		UpdatedAt:   task.UpdatedAt,
		GoalType:    string(task.GoalType),
		ContextRef:  formatContextRefMap(task.ContextRef),
		Project:     r.TaskProjectOf(task),
		Plans:       []TaskPlanProgress{},
	}
	plans, err := r.executionStore().ListExecutionPlans(taskID)
	if err != nil {
		return nil, err
	}
	for _, plan := range plans {
		item := TaskPlanProgress{
			PlanID:       plan.ID,
			Cycle:        plan.Cycle,
			DecisionType: plan.DecisionType,
			Reason:       plan.Reason,
			Need:         plan.Need,
			StepCount:    plan.StepCount,
		}
		steps, err := r.executionStore().ListExecutionSteps(plan.ID)
		if err != nil {
			return nil, err
		}
		item.Executed = len(steps)
		if outcome, ok, err := r.executionStore().ExecutionPlanOutcome(plan.ID); err != nil {
			return nil, err
		} else if ok {
			item.Outcome = outcome.Status
		}
		planned, err := r.executionStore().ListExecutionStepPlan(plan.ID)
		if err != nil {
			return nil, err
		}
		item.Steps = planStepProgress(planned, steps)
		progress.Plans = append(progress.Plans, item)
	}
	progress.Verification = r.taskVerificationProgress(taskID)
	// The same state a continuing run is handed: one reader for "where does this task
	// stand", so the panel and the next cycle cannot disagree about it.
	if brief := r.taskBriefing(taskID); brief != nil {
		progress.State = brief.State
	}
	return progress, nil
}

// taskVerificationProgress reads what the engine itself judged: the contract pinned on
// cycle 1 and the verdicts appended against it (docs/verification.md).
//
// Nothing to read is not an error and not an empty record: a task no verdict was ever
// written for carries no verification at all, so the field stays absent rather than
// claiming the engine looked and found nothing. A read failure is reported on stderr
// and treated the same way — the detail must not fail because a side log could not be
// read (the same best-effort reading verification itself does).
func (r *Autonomy) taskVerificationProgress(taskID string) *TaskVerificationProgress {
	s := r.verificationStore()
	if s == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	out := &TaskVerificationProgress{}
	contract, err := s.ListCompletionContract(taskID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] read completion contract %s: %v\n", taskID, err)
	}
	for _, row := range contract {
		out.Contract = append(out.Contract, TaskContractCriterion{
			Idx:       row.Idx,
			Name:      row.Name,
			Criterion: rawJSON(row.Criterion),
		})
	}
	verdicts, err := s.ListVerifications(taskID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] read verifications %s: %v\n", taskID, err)
	}
	for _, v := range verdicts {
		out.Verdicts = append(out.Verdicts, TaskVerdictProgress{
			ID:        v.ID,
			PlanID:    v.PlanID,
			Cycle:     v.Cycle,
			Criterion: v.Criterion,
			Result:    v.Result,
			Method:    v.Method,
			Evidence:  v.Evidence,
			Expected:  v.Expected,
			Observed:  v.Observed,
			Reason:    v.Reason,
			CreatedAt: v.CreatedAt,
		})
	}
	// A task that was judged keeps a verdict even when no contract was pinned: that is
	// a real, readable state ("nothing was ever pinned, so the done could not be
	// verified"), not an absent one.
	if out.Contract == nil && out.Verdicts == nil {
		return nil
	}
	if out.Contract == nil {
		out.Contract = []TaskContractCriterion{}
	}
	if out.Verdicts == nil {
		out.Verdicts = []TaskVerdictProgress{}
	}
	return out
}

func planStepProgress(planned []ExecutionStepPlan, executed []ExecutionStep) []TaskPlanStepProgress {
	byPlanStep := map[int64]ExecutionStep{}
	byIdx := map[int]ExecutionStep{}
	for _, step := range executed {
		if step.PlanStepID != 0 {
			byPlanStep[step.PlanStepID] = step
		}
		byIdx[step.Idx] = step
	}
	out := make([]TaskPlanStepProgress, 0, len(planned))
	seen := map[int64]bool{}
	for _, step := range planned {
		item := TaskPlanStepProgress{
			Idx:            step.Idx,
			Name:           step.Name,
			Capability:     step.Capability,
			ExpectedEffect: step.ExpectedEffect,
			PlannedInput:   rawJSON(step.Input),
			Status:         "pending",
		}
		ran, ok := byPlanStep[step.ID]
		if !ok {
			ran, ok = byIdx[step.Idx]
		}
		if ok {
			seen[ran.ID] = true
			fillExecuted(&item, ran)
		}
		out = append(out, item)
	}
	for _, ran := range executed {
		if seen[ran.ID] {
			continue
		}
		item := TaskPlanStepProgress{
			Idx:        ran.Idx,
			Name:       ran.Name,
			Capability: ran.Capability,
			Status:     "pending",
		}
		fillExecuted(&item, ran)
		out = append(out, item)
	}
	return out
}

func fillExecuted(item *TaskPlanStepProgress, ran ExecutionStep) {
	if ran.Status != "" {
		item.Status = ran.Status
	}
	item.Input = rawJSON(ran.Input)
	item.Output = rawJSON(ran.Output)
	item.Error = ran.Error
	item.DurationMS = ran.DurationMS
	if item.Capability == "" {
		item.Capability = ran.Capability
	}
	if item.Name == "" {
		item.Name = ran.Name
	}
}

func rawJSON(text string) json.RawMessage {
	text = strings.TrimSpace(text)
	if text == "" || text == "{}" || text == "null" {
		return nil
	}
	if !json.Valid([]byte(text)) {
		quoted, err := json.Marshal(text)
		if err != nil {
			return nil
		}
		return quoted
	}
	return json.RawMessage(text)
}

// StopTask stops what a task's agent is doing: the message it is processing right
// now is cancelled, and the stop is recorded as a message from the system, in its
// place in the queue — what was accepted before it is not lost, it is still the
// agent's to process next (src/inbox.go).
//
// This is the user's door (POST /api/tasks/{id}/stop). A task whose agent is not
// processing a message is not running (a queued instruction has not started):
// errTaskNotRunning. A missing task returns errTaskNotFound.
func (r *Autonomy) StopTask(taskID string) (*Task, error) {
	return r.stopTask(taskID, StopReason{By: stoppedByUser})
}

// stopTask is the one stop: the door above, and the runtime's own shutdown, differ only
// in the reason they carry. The reason is recorded *before* the run is cancelled, because
// the run loop is what marks the task stopped (markStopped) and writes with it what the
// task row says it was stopped for.
func (r *Autonomy) stopTask(taskID string, reason StopReason) (*Task, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	taskID = strings.TrimSpace(taskID)
	// Read-your-writes: the task is about to be marked stopped, so it is read from
	// the writer (docs/store.md「读写分离」).
	stored, err := writerReads(r.taskStore()).GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, errTaskNotFound
	}
	v, ok := inFlightTasks.Load(taskID)
	if !ok {
		return nil, errTaskNotRunning
	}
	setStopReason(taskID, reason)
	if cancel, ok := v.(context.CancelFunc); ok {
		cancel()
	}
	// The record of it, and where it belongs: after the instruction it stopped.
	if r.AgentFactory != nil {
		if agent := r.AgentFactory.ForTask(taskID); agent != nil {
			if _, err := r.agentInbox().Enqueue(agent, AgentMessage{
				TaskID:   taskID,
				Sender:   MessageSenderSystem,
				SenderID: "runtime",
				Kind:     MessageKindStop,
				Content:  reason.message(),
			}); err != nil {
				fmt.Fprintf(os.Stderr, "[autonomy] stop message for %s: %v\n", taskID, err)
			}
		}
	}
	markStopped(stored)
	return stored, nil
}

// AgentStatus returns whether the agent is working and, when it is, the live
// provider run id (reason_turns.run_id) as agent_run_id.
func (r *Autonomy) AgentStatus(taskID string, agentID int64) (*AgentWorkStatus, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	agent, err := r.agentStore().GetAgent(agentID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, nil
	}
	if agent.CurrentTask != nil && agent.CurrentTask.ID != "" && agent.CurrentTask.ID != taskID {
		return nil, fmt.Errorf("agent %d is bound to task %s, not %s", agentID, agent.CurrentTask.ID, taskID)
	}
	status := &AgentWorkStatus{
		TaskID:      taskID,
		AgentID:     agent.ID,
		Name:        agent.Name,
		State:       agent.State,
		LLMProvider: string(agent.LLMProvider),
		Account:     agent.accountSummary(),
		Model:       agent.Model,
	}
	turn, err := r.conversationStore().ActiveReasonTurn(taskID, agentID)
	if err != nil {
		return nil, err
	}
	if turn != nil {
		status.Working = true
		status.AgentRunID = turn.RunID
		status.TurnID = turn.ID
		status.Cycle = turn.Cycle
		if status.State == "" || status.State == "idle" {
			status.State = "running"
		}
	} else if agent.State == "running" {
		status.Working = true
	}
	return status, nil
}

// AgentStream returns conversation rows with id > lastSyncedMessageSeq.
//
// The stream is one task's *one agent's* conversation, so the ids have to be real:
// a task nobody knows, or an agent that is not that task's, has no conversation to
// poll. Answering with an empty list instead would dress a mistyped id up as "nothing
// has happened yet" — the very thing /agents/{agentID} answers with a 404.
func (r *Autonomy) AgentStream(taskID string, agentID, lastSyncedMessageSeq int64) (*AgentStreamResponse, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	task, err := r.taskStore().GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, errTaskNotFound
	}
	agent, err := r.agentStore().GetAgent(agentID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, errAgentNotFound
	}
	if agent.CurrentTask != nil && agent.CurrentTask.ID != "" && agent.CurrentTask.ID != taskID {
		return nil, fmt.Errorf("%w: agent %d is bound to task %s, not %s",
			errAgentOffTask, agentID, agent.CurrentTask.ID, taskID)
	}
	messages, err := r.conversationStore().ListLLMMessagesAfter(taskID, agentID, lastSyncedMessageSeq, 200)
	if err != nil {
		return nil, err
	}
	resp := &AgentStreamResponse{
		TaskID:           taskID,
		AgentID:          agentID,
		Events:           make([]StreamEvent, 0, len(messages)),
		Turns:            r.streamTurns(taskID, agentID, messages),
		NextPollAfterSeq: lastSyncedMessageSeq,
	}
	for _, m := range messages {
		resp.Events = append(resp.Events, StreamEvent{
			MessageSeq:        m.ID,
			TurnID:            m.TurnID,
			TurnSeq:           m.Seq,
			Cycle:             m.Cycle,
			Role:              m.Role,
			Content:           m.Content,
			NormalizedContent: m.NormalizedContent,
			Model:             m.Model,
			Provider:          string(m.LLMProvider),
			RunID:             m.RunID,
			Status:            m.Status,
			CreatedAt:         m.CreatedAt,
		})
		resp.LastMessageSeq = m.ID
		resp.NextPollAfterSeq = m.ID
	}
	return resp, nil
}

// streamTurns is the run header of every turn this page of the stream touches, one row
// per run and in turn order. A run whose header cannot be read (or that turns out to
// belong to somebody else) is left out rather than failing the poll: the conversation is
// what the caller asked for, and a missing summary costs it a closing line, not a page.
func (r *Autonomy) streamTurns(taskID string, agentID int64, messages []LLMMessage) []AgentRunSummary {
	summaries := []AgentRunSummary{}
	if len(messages) == 0 {
		return summaries
	}
	store, err := r.turnQueryStore()
	if err != nil {
		return summaries
	}
	seen := map[int64]bool{}
	for _, m := range messages {
		if m.TurnID == 0 || seen[m.TurnID] {
			continue
		}
		seen[m.TurnID] = true
		turn, err := store.GetTurn(m.TurnID)
		if err != nil || turn == nil || turn.TaskID != taskID || turn.AgentID != agentID {
			continue
		}
		summaries = append(summaries, AgentRunSummary{
			TurnID:       turn.ID,
			Cycle:        turn.Cycle,
			Mode:         string(turn.Mode),
			Model:        turn.Model,
			Provider:     string(turn.Provider),
			RunID:        turn.RunID,
			Status:       turn.Status,
			DurationMS:   turn.DurationMS,
			ErrorCode:    turn.ErrorCode,
			ErrorMessage: turn.ErrorMessage,
			StartedAt:    turn.StartedAt,
			EndedAt:      turn.EndedAt,
			CreatedAt:    turn.CreatedAt,
		})
	}
	return summaries
}
