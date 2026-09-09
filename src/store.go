package autonomy

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store persists tasks, agents, and reasoner turns.
type Store interface {
	UpsertTask(task *Task) error
	UpsertAgent(agent *Agent) error
	SoftDeleteAgent(id int64) error
	InsertReasonTurn(turn ReasonTurn) error
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
// The output is stored twice: RawOutput keeps the model's response verbatim,
// while NormalizedOutput is the structured/plain form (typically JSON) derived
// from it, which is what downstream consumers should rely on.
type ReasonTurn struct {
	TaskID           string
	AgentID          int64
	Step             int
	Mode             ReasonMode
	LLMProvider      LLMProvider
	Model            string
	Input            string
	RawOutput        string
	NormalizedOutput string
	CreatedAt        time.Time
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

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
