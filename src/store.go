package autonomy

import (
	"fmt"
	"os"
	"time"
)

// Persistence is described by seven cohesive ports instead of one flat surface,
// so each component takes the slice it needs and a database engine has a scoped
// method set to implement (and can even be built port by port):
//
//	TaskStore          tasks: what was asked for, and how it ended
//	AgentStore         agents
//	InboxStore         each agent's messages, in the order they arrived
//	ConversationStore  a run's header, its messages, and its raw event stream
//	ExecutionStore     what the runtime planned, and what it actually did
//	VerificationStore  the Completion Contract and the verdicts against it
//	TurnQueryStore     the read-only walk over reason turns the evaluation side reads through
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

// TurnQueryStore is the read-only log surface the evaluation side reads through:
// the benchmark tool, over HTTP (docs/http-api.md「数据 API」), instead of opening
// this database's file.
//
// It is a port of its own rather than more ConversationStore methods because it
// answers a different question. ConversationStore follows *one* run: its header,
// its messages, its stream. This one walks the whole log — filter, order, page,
// facet and count reason turns, and list the tasks those turns belong to — which is
// what a list page, a comparison page and a filter bar ask. Every method is a read:
// none of them writes, and none of them migrates, so no query here can have a side
// effect on the database it is only supposed to describe.
//
// The rows it returns are the *contract's* shape, not the schema's (TurnRecord
// below): that normalization is the point of moving the reader here — column names,
// the agent-name join and the "0 vs unknown cost" distinction are this side's
// business now, and a schema change no longer has to be chased by every consumer.
type TurnQueryStore interface {
	// QueryTurns reads one page of reason turns: the rows matching q, and the
	// number of rows that match the same filter (the pager's count, not the
	// table's). A filter matching nothing is an empty slice and a 0 count, not an
	// error.
	QueryTurns(q TurnQuery) ([]TurnRecord, int, error)
	// GetTurn reads one turn by id, untruncated. A missing row returns (nil, nil).
	GetTurn(id int64) (*TurnRecord, error)
	// TurnFacets reads every filter's distinct values with their counts, in one
	// call: the filter bar renders from one round trip.
	TurnFacets() (TurnFacets, error)
	// ListTaskOptions reads the task selector's candidates: the task rows unioned
	// with the task ids that only ever appear in the log (a task whose row is gone,
	// or one that was never written, is still a task someone ran).
	ListTaskOptions() ([]TaskOption, error)
	// ListTurnsByTask reads one task's turns in execution order (created_at, then
	// id — not id, which is only the order they were written in), and how many the
	// task has in total, so a caller can tell a capped page from a complete one.
	ListTurnsByTask(taskID string, limit int) ([]TurnRecord, int, error)
	// CountTurns is how many reason turns the store holds at all (the data API's
	// self-description, where the old reader asked the file).
	CountTurns() (int, error)
}

// TurnQuery is one page request of the turn list: every field is optional, and the
// zero value is "the newest first page of everything".
type TurnQuery struct {
	// TaskID / Mode / Model / Status match exactly.
	TaskID string
	Mode   ReasonMode
	Model  string
	Status string
	// Agent matches agents.name — the name, not the id, because a name is what the
	// filter bar offers (TurnFacets.Agents) and what a person recognizes.
	Agent string
	// Search is a literal substring of a turn's input or raw output. It is a
	// substring, not a pattern: % and _ are characters the caller searched for.
	Search string
	// Order is the column to sort by — id, created_at, duration_ms or total_tokens
	// (see the engine's whitelist). Anything else reads as id, so no caller text
	// ever reaches the SQL text. Dir is the direction: "asc" is oldest (or smallest)
	// first, and anything else — including nothing at all — is "desc", the reading
	// order, newest first. Ties break on id descending, which is what keeps a page
	// boundary from skipping or repeating a row.
	Order string
	Dir   string
	// Limit / Offset are the page: a limit <= 0 means DefaultTurnPageLimit and one
	// above MaxTurnPageLimit is clamped to it; a negative offset means 0. The store
	// clamps them itself, so a caller that already clamped (the HTTP layer, which
	// echoes the effective limit back) and one that did not agree.
	Limit  int
	Offset int
}

// TurnRecord is one reason turn as the data API reads it: a reason_turns row plus
// the name of the agent that produced it, with the contract's field names
// (llm_provider → provider, raw_output → output) and its timestamps verbatim.
//
// The timestamps are strings, exactly as the database holds them (RFC3339 text, in
// UTC): the log is evidence, and re-formatting evidence is how a reader ends up
// disagreeing with the file it read. CostCents is a pointer because the schema
// allows NULL — "the provider did not say" and "it cost 0" are different facts.
type TurnRecord struct {
	ID               int64       `json:"id"`
	TaskID           string      `json:"task_id"`
	Cycle            int         `json:"cycle"`
	Mode             ReasonMode  `json:"mode"`
	AgentID          int64       `json:"agent_id"`
	Agent            string      `json:"agent"`
	Provider         LLMProvider `json:"provider"`
	Model            string      `json:"model"`
	LLMAgentID       string      `json:"llm_agent_id"`
	Status           string      `json:"status"`
	ErrorCode        string      `json:"error_code"`
	ErrorMessage     string      `json:"error_message"`
	Input            string      `json:"input"`
	Output           string      `json:"output"`
	NormalizedOutput string      `json:"normalized_output"`
	RunID            string      `json:"run_id"`
	DurationMS       int64       `json:"duration_ms"`
	EventCount       int         `json:"event_count"`
	InputTokens      int64       `json:"input_tokens"`
	OutputTokens     int64       `json:"output_tokens"`
	CacheReadTokens  int64       `json:"cache_read_tokens"`
	CacheWriteTokens int64       `json:"cache_write_tokens"`
	ReasoningTokens  int64       `json:"reasoning_tokens"`
	TotalTokens      int64       `json:"total_tokens"`
	CostCents        *float64    `json:"cost_cents"`
	StartedAt        string      `json:"started_at"`
	EndedAt          string      `json:"ended_at"`
	CreatedAt        string      `json:"created_at"`
}

// TurnFacets is the filter bar's data: each facet column's distinct values, with
// how many turns carry them, ordered most-used first.
type TurnFacets struct {
	Tasks     []TurnFacetValue `json:"tasks"`
	Agents    []TurnFacetValue `json:"agents"`
	Modes     []TurnFacetValue `json:"modes"`
	Models    []TurnFacetValue `json:"models"`
	Providers []TurnFacetValue `json:"providers"`
	Statuses  []TurnFacetValue `json:"statuses"`
}

// TurnFacetValue is one value a filter accepts, and how many turns have it (empty
// values are left out: "" is not a filter anyone can choose).
type TurnFacetValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// TaskOption is one candidate of the task selector: what the task row says it is,
// plus what the log says happened to it.
type TaskOption struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Turns       int    `json:"turns"`
	// LastAt is the newest turn's created_at, verbatim, and "" when the task has
	// no turns at all (which is a real state: accepted, never run).
	LastAt string `json:"last_at"`
}

const (
	// DefaultTurnPageLimit / MaxTurnPageLimit bound one page of the turn list: the
	// list page's default, and the most a caller may ask for in one request (the
	// rows carry whole prompts, so the page size is also a payload size).
	DefaultTurnPageLimit = 50
	MaxTurnPageLimit     = 500
	// DefaultTaskTurnLimit / MaxTaskTurnLimit bound one task's execution series.
	// The series is read whole — a comparison page orders the whole task — so the
	// cap is high, and a series longer than it reports itself as capped.
	DefaultTaskTurnLimit = 1000
	MaxTaskTurnLimit     = 1000
	// DefaultTurnPreviewChars is how much of input / output a preview page keeps
	// when the caller does not say (truncate).
	DefaultTurnPreviewChars = 400
)

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
	TurnQueryStore
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
