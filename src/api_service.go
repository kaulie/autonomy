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
	TaskID      string             `json:"task_id"`
	Description string             `json:"description"`
	Domain      string             `json:"domain"`
	Status      string             `json:"status"`
	Error       string             `json:"error,omitempty"`
	AgentID     int64              `json:"agent_id"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	Plans       []TaskPlanProgress `json:"plans"`
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
type TaskPlanStepProgress struct {
	Idx            int             `json:"idx"`
	Name           string          `json:"name,omitempty"`
	Capability     string          `json:"capability,omitempty"`
	ExpectedEffect string          `json:"expected_effect,omitempty"`
	PlannedInput   json.RawMessage `json:"planned_input,omitempty"`
	Status         string          `json:"status"`
	Input          json.RawMessage `json:"input,omitempty"`
	Output         json.RawMessage `json:"output,omitempty"`
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
}

// StreamEvent is one incremental conversation item for poll clients.
// MessageSeq is llm_messages.id — the monotonic cursor across turns (per-turn
// seq resets every run and is exposed separately as TurnSeq).
type StreamEvent struct {
	MessageSeq int64          `json:"message_seq"`
	TurnID     int64          `json:"turn_id"`
	TurnSeq    int            `json:"turn_seq"`
	Cycle      int            `json:"cycle"`
	Role       LLMMessageRole `json:"role"`
	Content    string         `json:"content"`
	RunID      string         `json:"run_id,omitempty"`
	Status     string         `json:"status,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// AgentStreamResponse is an incremental poll of an agent's conversation stream.
type AgentStreamResponse struct {
	TaskID           string        `json:"task_id"`
	AgentID          int64         `json:"agent_id"`
	Events           []StreamEvent `json:"events"`
	LastMessageSeq   int64         `json:"last_message_seq"`
	NextPollAfterSeq int64         `json:"next_poll_after_seq"`
}

var (
	errTaskNotFound   = errors.New("task not found")
	errTaskNotRunning = errors.New("task is not running")
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
	// was created, and the description/domain/goal it was accepted with when this
	// request does not restate them) and, above all, the agent it was paired with —
	// because that is the agent this instruction resumes (see resumeAgentForTask).
	// A request that carries no words of its own changes nothing about what the task
	// is: what is being asked *now* is the message, and a message that says nothing
	// new is the row's own description (see instruction).
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
	if stored, err := r.taskStore().GetTask(taskID); err == nil && stored != nil {
		task.CreatedAt = stored.CreatedAt
		task.AgentID = stored.AgentID
		if desc == "" {
			task.Description = stored.Description
		}
		if strings.TrimSpace(req.Domain) == "" {
			task.Domain = stored.Domain
		}
		if strings.TrimSpace(req.GoalType) == "" {
			task.GoalType = stored.GoalType
		}
	}
	agent, msg, err := r.instruction(context.Background(), task, desc)
	if err != nil {
		return nil, nil, AgentMessage{}, err
	}
	return task, agent, msg, nil
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
	return progress, nil
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
// A task whose agent is not processing a message is not running (a queued
// instruction has not started): errTaskNotRunning. A missing task returns
// errTaskNotFound.
func (r *Autonomy) StopTask(taskID string) (*Task, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	taskID = strings.TrimSpace(taskID)
	stored, err := r.taskStore().GetTask(taskID)
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
				Content:  "the user stopped the task",
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
func (r *Autonomy) AgentStream(taskID string, agentID, lastSyncedMessageSeq int64) (*AgentStreamResponse, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	messages, err := r.conversationStore().ListLLMMessagesAfter(taskID, agentID, lastSyncedMessageSeq, 200)
	if err != nil {
		return nil, err
	}
	resp := &AgentStreamResponse{
		TaskID:           taskID,
		AgentID:          agentID,
		Events:           make([]StreamEvent, 0, len(messages)),
		NextPollAfterSeq: lastSyncedMessageSeq,
	}
	for _, m := range messages {
		resp.Events = append(resp.Events, StreamEvent{
			MessageSeq: m.ID,
			TurnID:     m.TurnID,
			TurnSeq:    m.Seq,
			Cycle:      m.Cycle,
			Role:       m.Role,
			Content:    m.Content,
			RunID:      m.RunID,
			Status:     m.Status,
			CreatedAt:  m.CreatedAt,
		})
		resp.LastMessageSeq = m.ID
		resp.NextPollAfterSeq = m.ID
	}
	return resp, nil
}
