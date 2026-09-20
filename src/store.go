package autonomy

import (
	"fmt"
	"os"
	"time"
)

// Persistence is described by five cohesive ports instead of one flat surface,
// so each component takes the slice it needs and a database engine has a scoped
// method set to implement (and can even be built port by port):
//
//	TaskStore          tasks: what was asked for, and how it ended
//	AgentStore         agents
//	InboxStore         each agent's messages, in the order they arrived
//	ConversationStore  a run's header, its messages, and its raw event stream
//	ExecutionStore     what the runtime planned, and what it actually did
//	VerificationStore  the Completion Contract and the verdicts against it
//
// Store is their union: the whole contract one database engine implements behind
// the StoreEngine SPI (store_engine.go). Upper-layer components depend on the
// port they use rather than on Store — llm_trace writes ConversationStore, the
// verifier reads ExecutionStore and writes VerificationStore, the HTTP read API
// reads TaskStore/AgentStore/ConversationStore/ExecutionStore — so switching the
// database does not touch any of them.
//
// That boundary is enforced, not just intended: store_ports_test.go fails when a
// file outside the engine imports a database driver or names the concrete engine.

// TaskStore is the task rows: what the user asked for and how the run ended.
type TaskStore interface {
	// UpsertTask writes the task, including the agent the run gave it.
	UpsertTask(task *Task) error
	// GetTask reads one task row by id. A missing row returns (nil, nil).
	GetTask(taskID string) (*Task, error)
	// ListTasks reads every task row, oldest first. It is how a broadcast
	// resolves its scope: a project's agents are the agents of that project's
	// tasks, and "every project" is every task (src/broadcast.go). One task per
	// row, with the context references it carries — the project a task names is
	// on its own row (tasks.context_ref), so the scope is decided from what the
	// tasks are, not from what a request happened to say.
	ListTasks() ([]*Task, error)
}

// AgentStore is the agent rows.
type AgentStore interface {
	UpsertAgent(agent *Agent) error
	SoftDeleteAgent(id int64) error
	// GetAgent reads one agent row by id (including soft-deleted). A missing row
	// returns (nil, nil).
	GetAgent(agentID int64) (*Agent, error)
}

// InboxStore is every agent's inbox: the messages addressed to it, in arrival
// order (src/message.go, src/inbox.go). A message is who said something and what
// they said; the agent processes them one at a time, oldest first.
type InboxStore interface {
	// EnqueueMessage appends one message to an agent's inbox and returns its id,
	// which is its place in the queue.
	EnqueueMessage(msg AgentMessage) (int64, error)
	// ClaimNextMessage takes the oldest queued message of one agent and marks it
	// running, so one consumer at a time owns it. found is false when the inbox is
	// empty (or when another consumer won that message).
	ClaimNextMessage(agentID int64) (AgentMessage, bool, error)
	// FinishAgentMessage records how one message ended (done / failed / stopped).
	FinishAgentMessage(id int64, status AgentMessageStatus, errText string) error
	// RequeueRunningMessages puts an agent's running messages back in the queue: a
	// message a process claimed and then died on is still the agent's to process.
	RequeueRunningMessages(agentID int64) error
	// ListAgentMessages reads an agent's inbox in arrival order. limit <= 0 means
	// all of it.
	ListAgentMessages(agentID int64, limit int) ([]AgentMessage, error)
	// CountQueuedMessages is how many messages are waiting for an agent (queued,
	// not being processed): what "is there anything behind this one" asks.
	CountQueuedMessages(agentID int64) (int, error)
	// CountMessagesAhead is how many messages are still in front of one message: the
	// agent's messages that arrived before it and have not finished — queued, or
	// being processed right now. 0 means the agent is on that message, or is about to
	// take it next. What "how long until the agent gets to this one" asks (the
	// acceptance both task entry points answer with).
	CountMessagesAhead(agentID, messageID int64) (int, error)
}

// ConversationStore is one LLM interaction: a reason_turns row is the header
// (run metadata) of the interaction, its llm_messages rows are the user input,
// the aggregated thinking/tool intermediates, and the assistant output as
// independent linked records, and its llm_events rows are the provider's raw run
// stream (see BeginReasonTurn / AppendLLMEvents / FinishReasonTurn).
type ConversationStore interface {
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
	// ActiveReasonTurn is the in-flight LLM run for a task's agent, if any
	// (reason_turns.status = running). Used by the HTTP status API to surface
	// the live provider run id as agent_run_id.
	ActiveReasonTurn(taskID string, agentID int64) (*ReasonTurn, error)
	// ListLLMMessagesAfter reads an agent's conversation stream across turns,
	// ordered by llm_messages.id ascending, for rows with id > afterID. The id
	// is the monotonic sync cursor the HTTP poll API exposes as message_seq
	// (per-turn seq resets every run and cannot drive cross-turn polling).
	ListLLMMessagesAfter(taskID string, agentID, afterID int64, limit int) ([]LLMMessage, error)
}

// ExecutionStore is the run's account of itself: the plan written before
// anything ran, the steps that then ran, and the provider interactions inside
// them (src/execution.go, src/runtime.go).
type ExecutionStore interface {
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
}

// VerificationStore is the Completion Contract a task pinned and the verdicts
// checked against it (src/completion_contract.go, src/verification.go).
type VerificationStore interface {
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
}

// Store is every port at once: the contract a database engine implements and the
// handle the runtime's own writers (persistTask, saveExecutionPlan, …) are wired
// to. A caller that needs less should take the port instead of Store.
type Store interface {
	TaskStore
	AgentStore
	InboxStore
	ConversationStore
	ExecutionStore
	VerificationStore
	// Close releases the engine's connection.
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

// activeStore is the process-wide store the runtime's writers use. It is nil
// when no database is wired in (tests, embedding), and every best-effort writer
// below tolerates that.
func activeStore() Store {
	if _store != nil {
		return _store
	}
	if _autonomy != nil {
		return _autonomy.Store
	}
	return nil
}

// The port accessors narrow the active store to the slice a component needs, so
// no part of the upper layer has to know the whole persistence surface: whoever
// reads tasks takes activeTaskStore(), whoever writes a run's conversation takes
// activeConversationStore(). Each is nil when no store is wired in.

func activeTaskStore() TaskStore                 { return activeStore() }
func activeAgentStore() AgentStore               { return activeStore() }
func activeConversationStore() ConversationStore { return activeStore() }
func activeExecutionStore() ExecutionStore       { return activeStore() }
func activeVerificationStore() VerificationStore { return activeStore() }

// The same narrowing for a component that holds an Autonomy rather than the
// process singleton (the HTTP read API). Each method returns the port, nil when
// no store is bound.

func (r *Autonomy) taskStore() TaskStore                 { return r.Store }
func (r *Autonomy) agentStore() AgentStore               { return r.Store }
func (r *Autonomy) conversationStore() ConversationStore { return r.Store }
func (r *Autonomy) executionStore() ExecutionStore       { return r.Store }

// saveExecutionPlan writes one plan and its steps before any of them runs, and
// returns the steps as stored — with their row ids, because plan_step_id is the row
// an execution step belongs to and only the database knows that id. The error is
// returned rather than logged because a plan is authoritative: nothing executes
// without one (see Runtime.Execute). Without a store there is nothing to write and
// nothing to block on.
func saveExecutionPlan(plan ExecutionPlan, steps []ExecutionStepPlan) (int64, []ExecutionStepPlan, error) {
	s := activeExecutionStore()
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
	s := activeExecutionStore()
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
	s := activeExecutionStore()
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
	s := activeVerificationStore()
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
	s := activeExecutionStore()
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
	if s := activeTaskStore(); s != nil {
		if err := s.UpsertTask(task); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] persist task %s: %v\n", task.ID, err)
		}
	}
}

func persistAgent(agent *Agent) {
	if agent == nil {
		return
	}
	if s := activeAgentStore(); s != nil {
		if err := s.UpsertAgent(agent); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] persist agent %s: %v\n", agent.Name, err)
		}
	}
}

func softDeleteAgent(id int64) {
	if s := activeAgentStore(); s != nil {
		if err := s.SoftDeleteAgent(id); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] soft-delete agent %d: %v\n", id, err)
		}
	}
}

func persistReasonTurn(turn ReasonTurn) {
	if s := activeConversationStore(); s != nil {
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
