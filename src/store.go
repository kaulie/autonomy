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
	// FinishReasonTurn finalizes the header with status, usage, and timing,
	// records the assistant message (linked to the user input via the handle),
	// and backfills the run id onto events written before it was known.
	FinishReasonTurn(h ReasonTurnHandle, res LLMRunResult) error
	// ListLLMEvents reads a run's stream events in Seq order.
	ListLLMEvents(turnID int64) ([]LLMEvent, error)
	// ListLLMMessages reads a run's messages in Seq order: the user input, the
	// aggregated thinking/tool intermediates, then the assistant output.
	ListLLMMessages(turnID int64) ([]LLMMessage, error)
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
	TaskID           string
	AgentID          int64
	Step             int
	Mode             ReasonMode
	LLMProvider      LLMProvider
	Model            string
	LLMAgentID       string
	Input            string
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
	Step    int
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
func recordReasonTurn(agent *Agent, taskID string, step int, mode ReasonMode, input, rawOutput string) {
	turn := ReasonTurn{
		TaskID:    taskID,
		Step:      step,
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
