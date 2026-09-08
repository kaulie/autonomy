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
	SoftDeleteAgent(id string) error
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
type ReasonTurn struct {
	TaskID    string
	AgentID   string
	Step      int
	Mode      ReasonMode
	Input     string
	Output    string
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
			fmt.Fprintf(os.Stderr, "[autonomy] persist agent %s: %v\n", agent.ID, err)
		}
	}
}

func softDeleteAgent(id string) {
	if s := activeStore(); s != nil {
		if err := s.SoftDeleteAgent(id); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] soft-delete agent %s: %v\n", id, err)
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
