package autonomy

import (
	"database/sql"
	"fmt"
	"os"
	"time"
)

// Store is the single, database-agnostic persistence contract the rest of
// Autonomy codes against: tasks, agents, reasoner turns, and conversation
// messages. A reason_turns row is the header (run metadata) of one LLM
// interaction; its llm_messages rows are the user input and assistant output as
// independent, linked records; its llm_events rows are the provider's raw run
// stream (see BeginReasonTurn / AppendLLMEvents / FinishReasonTurn).
//
// Concrete databases sit behind the StoreEngine SPI (store_engine.go): SQLite is
// the built-in engine, and other databases plug in by registering another
// engine. Neither this interface nor its callers change when the database does.
type Store interface {
	UpsertTask(task *Task) error
	UpsertAgent(agent *Agent) error
	SoftDeleteAgent(id int64) error
	// InsertReasonTurn writes a complete interaction in one shot (used by the
	// local reasoner and other non-streaming callers). The header, the user
	// input, and the assistant output are recorded together.
	InsertReasonTurn(turn ReasonTurn) error
	// BeginReasonTurn opens a run header, records the user-input message, and
	// returns the handle that stream events and the final assistant message
	// attach to. The handle's InputMessageID links the output back to its input.
	BeginReasonTurn(turn ReasonTurn) (ReasonTurnHandle, error)
	// AppendLLMEvents appends a batch of neutral stream events to a run.
	AppendLLMEvents(turnID int64, runID string, events []LLMEvent) error
	// AppendLLMMessages upserts aggregated conversation messages (the thinking/
	// tool rows derived from the stream) for a run that is still streaming, so a
	// consumer can follow the conversation before the run ends. Rows are keyed by
	// (turn_id, seq) and a re-write updates the existing row, which is how a
	// message that grows after being written (a tool call that returns later) is
	// kept correct.
	AppendLLMMessages(turnID int64, messages []LLMMessage) error
	// FinishReasonTurn finalizes the header with status, usage, and timing,
	// records the assistant message (linked to the user input via the handle),
	// and backfills the run id onto events written before it was known.
	FinishReasonTurn(h ReasonTurnHandle, res LLMRunResult) error
	// ListLLMEvents reads a run's stream events in Seq order.
	ListLLMEvents(turnID int64) ([]LLMEvent, error)
	// ListLLMMessages reads a run's messages in Seq order: the user input, the
	// aggregated thinking/tool intermediates, then the assistant output.
	ListLLMMessages(turnID int64) ([]LLMMessage, error)
	// AssistantMessageID is the row a run's reply was recorded in, so a decision can
	// be traced to the exact message it came from (llm_messages.id).
	AssistantMessageID(turnID int64) (int64, bool, error)
	// CreateExecutionPlan writes one plan (the steps are written next, before any
	// of them runs). A plan is immutable: there is no update, and a re-plan is a
	// new row.
	CreateExecutionPlan(plan ExecutionPlan) (int64, error)
	// AppendExecutionStepPlans writes a plan's steps in one call. They are written
	// in full before the first step runs, which is what makes "planned but never
	// executed" answerable afterwards.
	AppendExecutionStepPlans(steps []ExecutionStepPlan) error
	// AppendExecutionStep records one step that ran, linked to its planned step.
	AppendExecutionStep(step ExecutionStep) (int64, error)
	// AppendExecutionStepInteraction records one provider interaction of one step.
	AppendExecutionStepInteraction(interaction ExecutionStepInteraction) (int64, error)
	// TaskInputMessageID is the task's own first user input (llm_messages), which
	// every plan of that task points at so a plan is traceable to what asked for it.
	TaskInputMessageID(taskID string) (int64, bool, error)
	// ListExecutionPlans reads a task's plans in creation order, oldest first.
	ListExecutionPlans(taskID string) ([]ExecutionPlan, error)
	// ListExecutionStepPlan reads a plan's planned steps in plan order.
	ListExecutionStepPlan(planID int64) ([]ExecutionStepPlan, error)
	// ListExecutionSteps reads a plan's executed steps in execution order.
	ListExecutionSteps(planID int64) ([]ExecutionStep, error)
	// ListExecutionStepInteractions reads one step's interactions in Seq order.
	ListExecutionStepInteractions(stepID int64) ([]ExecutionStepInteraction, error)
	// ExecutionPlanOutcome derives a plan's result from its steps: the last one's
	// status. Nothing stores it, so none of it can drift.
	ExecutionPlanOutcome(planID int64) (ExecutionPlanOutcome, bool, error)
	// AppendCompletionContract pins one criterion of a task's Completion Contract.
	// The first row for a (task, idx) is the one kept: a task's contract is written
	// once, and a later cycle restating it changes nothing (src/completion_contract.go).
	AppendCompletionContract(criterion ContractCriterion) error
	// ListCompletionContract reads a task's pinned contract in criterion order.
	ListCompletionContract(taskID string) ([]ContractCriterion, error)
	// AppendVerification records one verdict of one criterion (src/verification.go).
	AppendVerification(v Verification) (int64, error)
	// ListVerifications reads a task's verdicts in creation order.
	ListVerifications(taskID string) ([]Verification, error)
	// GetTask reads one task row by id.
	GetTask(taskID string) (*Task, error)
	// GetAgent reads one agent row by id (including soft-deleted).
	GetAgent(agentID int64) (*Agent, error)
	// ActiveReasonTurn is the in-flight LLM run for a task's agent, if any
	// (reason_turns.status = running). Used by the HTTP status API to surface
	// the live provider run id as agent_run_id.
	ActiveReasonTurn(taskID string, agentID int64) (*ReasonTurn, error)
	// ListLLMMessagesAfter reads an agent's conversation stream across turns,
	// ordered by llm_messages.id ascending, for rows with id > afterID. The id
	// is the monotonic sync cursor the HTTP poll API exposes as message_seq
	// (per-turn seq resets every run and cannot drive cross-turn polling).
	ListLLMMessagesAfter(taskID string, agentID, afterID int64, limit int) ([]LLMMessage, error)
	Close() error
}

// ReasonMode identifies whether a reason turn was produced by the local
// planning reasoner or a Cursor-backed agent run.
type ReasonMode string

const (
	ReasonModePlan  ReasonMode = "plan"
	ReasonModeAgent ReasonMode = "agent"
)

// ReasonTurn is one reasoner conversation (input prompt + model/local output).
// It is the run header for an LLM interaction: the output is stored twice
// (RawOutput verbatim, NormalizedOutput the structured/plain form downstream
// consumers rely on), and the streamed events live in llm_events.
//
// The Run* / Status / usage fields are populated for streamed provider runs;
// they stay zero for local or one-shot turns.
type ReasonTurn struct {
	// ID is the reason_turns row id (0 until persisted).
	ID          int64
	TaskID      string
	AgentID     int64
	Cycle       int
	Mode        ReasonMode
	LLMProvider LLMProvider
	Model       string
	LLMAgentID  string
	Input       string
	// InputRole is who authored Input: the user (empty defaults to it) for the
	// runtime's own prompts, or agent when another agent delegated this run.
	InputRole        LLMMessageRole
	RawOutput        string
	NormalizedOutput string
	// RunID is the provider's run id (e.g. Cursor RunResult.RunID).
	RunID string
	// Status is the run lifecycle status (LLMStatus*).
	Status           string
	ErrorCode        string
	ErrorMessage     string
	DurationMS       int64
	EventCount       int
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	TotalTokens      int64
	// CostCents is nil when the provider did not report a cost.
	CostCents *float64
	StartedAt time.Time
	EndedAt   time.Time
	CreatedAt time.Time
}

// LLMMessageRole identifies the author/kind of one llm_messages row. user is the
// input, assistant the run's final return, and thinking/tool are the aggregated
// intermediates derived from the run's raw stream (see aggregateChatMessages).
type LLMMessageRole string

const (
	LLMMessageRoleUser      LLMMessageRole = "user"
	LLMMessageRoleAssistant LLMMessageRole = "assistant"
	LLMMessageRoleThinking  LLMMessageRole = "thinking"
	LLMMessageRoleTool      LLMMessageRole = "tool"
	// LLMMessageRoleAgent marks a run's input when another agent authored the
	// prompt (a capability delegating a sub-task to this agent), as opposed to
	// user, which is the human's own task. The user only authors the top-level
	// task; everything delegated below it is agent to agent.
	LLMMessageRoleAgent LLMMessageRole = "agent"
)

// ReasonTurnHandle is what BeginReasonTurn returns: the run header id plus the
// id of the user-input message. FinishReasonTurn consumes it so the assistant
// message it records can point back (ParentID) at the exact input it answers.
type ReasonTurnHandle struct {
	TurnID         int64
	InputMessageID int64
}

// LLMMessage is one stored conversation message: a user input or an assistant
// output as an independent record. Assistant rows carry ParentID pointing at the
// user row they answer, so a return is traceable to its specific input.
//
// It is deliberately database-agnostic; the engine decides how to persist it.
type LLMMessage struct {
	ID      int64
	TurnID  int64
	TaskID  string
	AgentID int64
	Cycle   int
	// Seq orders messages within one turn (user input = 0, assistant = 1).
	Seq  int
	Role LLMMessageRole
	// ParentID is the id of the message this one answers (0 when none).
	ParentID          int64
	Content           string
	NormalizedContent string
	LLMProvider       LLMProvider
	Model             string
	RunID             string
	Status            string
	CreatedAt         time.Time
}

var _store Store

func activeStore() Store {
	if _store != nil {
		return _store
	}
	if _autonomy != nil {
		return _autonomy.Store
	}
	return nil
}

// saveExecutionPlan writes one plan and its steps before any of them runs, and
// returns the steps as stored — with their row ids, because plan_step_id is the row
// an execution step belongs to and only the database knows that id. The error is
// returned rather than logged because a plan is authoritative: nothing executes
// without one (see Runtime.Execute). Without a store there is nothing to write and
// nothing to block on.
func saveExecutionPlan(plan ExecutionPlan, steps []ExecutionStepPlan) (int64, []ExecutionStepPlan, error) {
	s := activeStore()
	if s == nil {
		return 0, steps, nil
	}
	planID, err := s.CreateExecutionPlan(plan)
	if err != nil {
		return 0, nil, err
	}
	for i := range steps {
		steps[i].PlanID = planID
	}
	if err := s.AppendExecutionStepPlans(steps); err != nil {
		return planID, nil, err
	}
	saved, err := s.ListExecutionStepPlan(planID)
	if err != nil {
		return planID, nil, err
	}
	return planID, saved, nil
}

// saveExecutionStep records one step that ran, and saveExecutionStepInteraction one
// provider interaction of it. Both are best effort: the step already happened, and
// losing the record must not fail the action it describes.
func saveExecutionStep(step ExecutionStep) int64 {
	s := activeStore()
	if s == nil {
		return 0
	}
	id, err := s.AppendExecutionStep(step)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] append execution step: %v\n", err)
		return 0
	}
	return id
}

func saveExecutionStepInteraction(in ExecutionStepInteraction) {
	s := activeStore()
	if s == nil {
		return
	}
	if _, err := s.AppendExecutionStepInteraction(in); err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] append execution step interaction: %v\n", err)
	}
}

// saveVerification records one verdict. Like the execution records it is best effort:
// the verdict is what the run acts on, and losing the row must not change it.
func saveVerification(v Verification) {
	s := activeStore()
	if s == nil {
		return
	}
	if _, err := s.AppendVerification(v); err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] append verification: %v\n", err)
	}
}

// taskInputMessageID is the task's own first user input as already recorded on its
// plans, so every plan of the task points at the same message.
func taskInputMessageID(taskID string) (int64, bool) {
	s := activeStore()
	if s == nil {
		return 0, false
	}
	id, found, err := s.TaskInputMessageID(taskID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] read task input message: %v\n", err)
		return 0, false
	}
	return id, found
}

func persistTask(task *Task) {
	if task == nil {
		return
	}
	if s := activeStore(); s != nil {
		if err := s.UpsertTask(task); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] persist task %s: %v\n", task.ID, err)
		}
	}
}

func persistAgent(agent *Agent) {
	if agent == nil {
		return
	}
	if s := activeStore(); s != nil {
		if err := s.UpsertAgent(agent); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] persist agent %s: %v\n", agent.Name, err)
		}
	}
}

func softDeleteAgent(id int64) {
	if s := activeStore(); s != nil {
		if err := s.SoftDeleteAgent(id); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] soft-delete agent %d: %v\n", id, err)
		}
	}
}

func persistReasonTurn(turn ReasonTurn) {
	if s := activeStore(); s != nil {
		if turn.CreatedAt.IsZero() {
			turn.CreatedAt = time.Now()
		}
		if err := s.InsertReasonTurn(turn); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] persist reason turn: %v\n", err)
		}
	}
}

// recordReasonTurn is the single entry point for persisting a reasoner or agent
// conversation turn. Both the decision reasoner and capability agent sessions
// funnel through here so field handling stays consistent. rawOutput is the
// model's verbatim response; the normalized form is derived at insert time.
func recordReasonTurn(agent *Agent, taskID string, cycle int, mode ReasonMode, input, rawOutput string) {
	turn := ReasonTurn{
		TaskID:    taskID,
		Cycle:     cycle,
		Mode:      mode,
		Input:     input,
		RawOutput: rawOutput,
	}
	if agent != nil {
		turn.AgentID = agent.ID
		turn.LLMProvider = agent.LLMProvider
		turn.Model = agent.Model
	}
	persistReasonTurn(turn)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// nullTimeArg renders a possibly-zero time as a nullable column value.
func nullTimeArg(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

// nullFloatArg renders a possibly-nil float as a nullable column value.
func nullFloatArg(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

// usageCostArg renders usage cost as a nullable column value, keeping unknown
// cost distinct from a genuine zero.
func usageCostArg(u LLMUsage) any {
	if !u.CostKnown {
		return nil
	}
	return u.CostCents
}

// parseTime parses a stored RFC3339Nano timestamp, returning the zero time for
// empty or malformed input.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
