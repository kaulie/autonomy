package autonomy

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store persists tasks, agents, and reasoner turns. A reason_turns row is the
// header of one LLM interaction; its llm_events rows are the provider's run
// stream (see BeginReasonTurn / AppendLLMEvents / FinishReasonTurn).
type Store interface {
	UpsertTask(task *Task) error
	UpsertAgent(agent *Agent) error
	SoftDeleteAgent(id int64) error
	// InsertReasonTurn writes a complete interaction in one shot (used by the
	// local reasoner and other non-streaming callers).
	InsertReasonTurn(turn ReasonTurn) error
	// BeginReasonTurn opens a run header and returns its id so stream events
	// can be appended while the run is live.
	BeginReasonTurn(turn ReasonTurn) (int64, error)
	// AppendLLMEvents appends a batch of neutral stream events to a run.
	AppendLLMEvents(turnID int64, runID string, events []LLMEvent) error
	// FinishReasonTurn finalizes the header with status, usage, and timing and
	// backfills the run id onto any events written before it was known.
	FinishReasonTurn(turnID int64, res LLMRunResult) error
	// ListLLMEvents reads a run's stream events in Seq order.
	ListLLMEvents(turnID int64) ([]LLMEvent, error)
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

// OpenDefaultStore opens $PROJECT_ROOT/data/autonomy.db.
func OpenDefaultStore() (Store, error) {
	root, err := projectRoot()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "data", "autonomy.db")
	return OpenSQLiteStore(path)
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
