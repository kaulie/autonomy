package autonomy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// AcceptTaskResponse is returned as soon as the task is accepted and the planner
// agent exists; Run continues asynchronously.
type AcceptTaskResponse struct {
	TaskID  string `json:"task_id"`
	AgentID int64  `json:"agent_id"`
	Status  string `json:"status"`
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

// inFlightTasks maps a running task id to the cancel func for its Run.
var inFlightTasks sync.Map

// AcceptTask persists the task, creates its planner agent, and starts Run on a
// background goroutine so the HTTP caller is not locked inside the decision loop.
func (r *Autonomy) AcceptTask(req AcceptTaskRequest) (*AcceptTaskResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("autonomy not initialized")
	}
	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		return nil, fmt.Errorf("description is required")
	}
	taskID := strings.TrimSpace(req.ID)
	if taskID == "" {
		taskID = newTaskID()
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, loaded := inFlightTasks.LoadOrStore(taskID, cancel); loaded {
		cancel()
		return nil, fmt.Errorf("task %s is already running", taskID)
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
	persistTask(task)

	go func() {
		defer inFlightTasks.Delete(taskID)
		defer cancel()
		task.Status = TaskStatusRunning
		if err := r.run(ctx, task); err != nil {
			fmt.Printf("[autonomy] task %s finished with error: %v\n", taskID, err)
		}
	}()

	// Agent id is assigned inside Run; wait briefly so the accept response can
	// include it. If Run is slow to start, clients can still poll progress.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, err := r.Store.GetTask(taskID)
		if err == nil && stored != nil && stored.AgentID != 0 {
			return &AcceptTaskResponse{
				TaskID:  taskID,
				AgentID: stored.AgentID,
				Status:  stored.Status,
			}, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	stored, _ := r.Store.GetTask(taskID)
	status := TaskStatusPending
	var agentID int64
	if stored != nil {
		status = stored.Status
		agentID = stored.AgentID
	}
	return &AcceptTaskResponse{TaskID: taskID, AgentID: agentID, Status: status}, nil
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
	task, err := r.Store.GetTask(taskID)
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
	plans, err := r.Store.ListExecutionPlans(taskID)
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
		steps, err := r.Store.ListExecutionSteps(plan.ID)
		if err != nil {
			return nil, err
		}
		item.Executed = len(steps)
		if outcome, ok, err := r.Store.ExecutionPlanOutcome(plan.ID); err != nil {
			return nil, err
		} else if ok {
			item.Outcome = outcome.Status
		}
		planned, err := r.Store.ListExecutionStepPlan(plan.ID)
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

// StopTask cancels a running task. The run records status "stopped" and leaves
// the planner loop. A task that is not in flight returns errTaskNotRunning;
// a missing task returns errTaskNotFound.
func (r *Autonomy) StopTask(taskID string) (*Task, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	taskID = strings.TrimSpace(taskID)
	stored, err := r.Store.GetTask(taskID)
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
	markStopped(stored)
	return stored, nil
}

// AgentStatus returns whether the agent is working and, when it is, the live
// provider run id (reason_turns.run_id) as agent_run_id.
func (r *Autonomy) AgentStatus(taskID string, agentID int64) (*AgentWorkStatus, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	agent, err := r.Store.GetAgent(agentID)
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
	turn, err := r.Store.ActiveReasonTurn(taskID, agentID)
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
	messages, err := r.Store.ListLLMMessagesAfter(taskID, agentID, lastSyncedMessageSeq, 200)
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
