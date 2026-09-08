package autonomy

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore is a file-backed Store using modernc.org/sqlite (pure Go).
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLiteStore creates/opens the DB at path and migrates schema.
func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir store dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// Single-writer friendly defaults for local agent use.
	if _, err := db.Exec(`PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma: %w", err)
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  domain TEXT NOT NULL DEFAULT '',
  context TEXT NOT NULL DEFAULT '',
  target TEXT NOT NULL DEFAULT '',
  goal TEXT NOT NULL DEFAULT '',
  expected_state TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  agent_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  state TEXT NOT NULL DEFAULT '',
  lifecycle TEXT NOT NULL DEFAULT 'ephemeral',
  current_task_id TEXT NOT NULL DEFAULT '',
  context TEXT NOT NULL DEFAULT '',
  llm_agent_id TEXT NOT NULL DEFAULT '',
  llm_provider TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);

CREATE TABLE IF NOT EXISTS reason_turns (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL DEFAULT '',
  agent_id TEXT NOT NULL DEFAULT '',
  step INTEGER NOT NULL DEFAULT 0,
  input TEXT NOT NULL DEFAULT '',
  output TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_reason_turns_agent ON reason_turns(agent_id, step);
CREATE INDEX IF NOT EXISTS idx_reason_turns_task ON reason_turns(task_id, step);
CREATE INDEX IF NOT EXISTS idx_agents_task ON agents(current_task_id);
`
	_, err := s.db.Exec(ddl)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := s.ensureColumn("tasks", "agent_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate tasks.agent_id: %w", err)
	}
	if err := s.ensureColumn("agents", "llm_provider", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate agents.llm_provider: %w", err)
	}
	if err := s.ensureColumn("agents", "model", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate agents.model: %w", err)
	}
	if err := s.renameColumnIfMissing("agents", "cursor_agent_id", "llm_agent_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate agents.llm_agent_id: %w", err)
	}
	return nil
}

func (s *SQLiteStore) columnExists(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// ensureColumn adds a column to an existing table when it is missing. CREATE
// TABLE IF NOT EXISTS only applies to fresh databases, so this keeps older
// databases usable after schema additions.
func (s *SQLiteStore) ensureColumn(table, column, decl string) error {
	exists, err := s.columnExists(table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
	return err
}

// renameColumnIfMissing renames oldCol to newCol for databases that predate the
// rename. It adds the new column, copies any non-empty values, and drops the
// old column. Fresh databases already create newCol and skip this entirely.
func (s *SQLiteStore) renameColumnIfMissing(table, oldCol, newCol, decl string) error {
	hasNew, err := s.columnExists(table, newCol)
	if err != nil {
		return err
	}
	if hasNew {
		return nil
	}
	hasOld, err := s.columnExists(table, oldCol)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, newCol, decl)); err != nil {
		return err
	}
	if hasOld {
		if _, err := s.db.Exec(fmt.Sprintf(
			"UPDATE %s SET %s = %s WHERE %s <> ''", table, newCol, oldCol, oldCol,
		)); err != nil {
			return err
		}
		if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, oldCol)); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) UpsertTask(task *Task) error {
	if task == nil {
		return fmt.Errorf("nil task")
	}
	now := time.Now()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	_, err := s.db.Exec(`
INSERT INTO tasks (id, description, domain, context, target, goal, expected_state, status, agent_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  description=excluded.description,
  domain=excluded.domain,
  context=excluded.context,
  target=excluded.target,
  goal=excluded.goal,
  expected_state=excluded.expected_state,
  status=excluded.status,
  agent_id=excluded.agent_id,
  updated_at=excluded.updated_at
`, task.ID, task.Description, string(task.Domain), task.Context, task.Target, task.Goal,
		task.Contract.ExpectedState, task.Status, task.AgentID, formatTime(task.CreatedAt), formatTime(task.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert task: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpsertAgent(agent *Agent) error {
	if agent == nil {
		return fmt.Errorf("nil agent")
	}
	now := time.Now()
	taskID := ""
	if agent.CurrentTask != nil {
		taskID = agent.CurrentTask.ID
	}
	lifecycle := string(agent.Lifecycle)
	if lifecycle == "" {
		lifecycle = string(AgentLifecycleEphemeral)
	}
	_, err := s.db.Exec(`
INSERT INTO agents (id, state, lifecycle, current_task_id, context, llm_agent_id, llm_provider, model, created_at, updated_at, deleted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(id) DO UPDATE SET
  state=excluded.state,
  lifecycle=excluded.lifecycle,
  current_task_id=excluded.current_task_id,
  context=excluded.context,
  llm_agent_id=excluded.llm_agent_id,
  llm_provider=excluded.llm_provider,
  model=excluded.model,
  updated_at=excluded.updated_at,
  deleted_at=NULL
`, agent.ID, agent.State, lifecycle, taskID, agent.Context, agent.LLMAgentID, string(agent.LLMProvider), agent.Model,
		formatTime(now), formatTime(now))
	if err != nil {
		return fmt.Errorf("upsert agent: %w", err)
	}
	return nil
}

func (s *SQLiteStore) SoftDeleteAgent(id string) error {
	_, err := s.db.Exec(`
UPDATE agents SET deleted_at = ?, updated_at = ?, state = 'deleted'
WHERE id = ? AND deleted_at IS NULL
`, formatTime(time.Now()), formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("soft-delete agent: %w", err)
	}
	return nil
}

func (s *SQLiteStore) InsertReasonTurn(turn ReasonTurn) error {
	if turn.CreatedAt.IsZero() {
		turn.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(`
INSERT INTO reason_turns (task_id, agent_id, step, input, output, created_at)
VALUES (?, ?, ?, ?, ?, ?)
`, turn.TaskID, turn.AgentID, turn.Step, turn.Input, turn.Output, formatTime(turn.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert reason turn: %w", err)
	}
	return nil
}
