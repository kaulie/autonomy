package autonomy

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// This schema change replaces the legacy uuid-based TEXT agents.id with an
	// INTEGER AUTOINCREMENT id and a derived name. Existing data is discarded.
	legacy, err := s.agentsUseTextID()
	if err != nil {
		return err
	}
	if legacy {
		for _, table := range []string{"reason_turns", "tasks", "agents"} {
			if _, err := s.db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
				return fmt.Errorf("drop legacy %s: %w", table, err)
			}
		}
	}

	const ddl = `
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  domain TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  agent_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL DEFAULT '',
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
  agent_id INTEGER NOT NULL DEFAULT 0,
  step INTEGER NOT NULL DEFAULT 0,
  mode TEXT NOT NULL DEFAULT '',
  llm_provider TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  input TEXT NOT NULL DEFAULT '',
  raw_output TEXT NOT NULL DEFAULT '',
  normalized_output TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_reason_turns_agent ON reason_turns(agent_id, step);
CREATE INDEX IF NOT EXISTS idx_reason_turns_task ON reason_turns(task_id, step);
CREATE INDEX IF NOT EXISTS idx_agents_task ON agents(current_task_id);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := s.ensureAgentsIDSequence(); err != nil {
		return fmt.Errorf("migrate agents.id sequence: %w", err)
	}

	// reason_turns additions for databases that predate these columns. Fresh
	// databases already create them above; this keeps older DBs usable.
	if err := s.ensureColumn("reason_turns", "mode", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate reason_turns.mode: %w", err)
	}
	if err := s.ensureColumn("reason_turns", "llm_provider", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate reason_turns.llm_provider: %w", err)
	}
	if err := s.ensureColumn("reason_turns", "model", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate reason_turns.model: %w", err)
	}
	// Split the legacy reason_turns.output into raw_output (verbatim) and
	// normalized_output (derived, typically JSON). Existing output values move
	// into raw_output as best effort.
	if err := s.renameColumnIfMissing("reason_turns", "output", "raw_output", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate reason_turns.raw_output: %w", err)
	}
	if err := s.ensureColumn("reason_turns", "normalized_output", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate reason_turns.normalized_output: %w", err)
	}
	if err := s.backfillReasonTurnNormalizedOutputs(); err != nil {
		return fmt.Errorf("normalize reason_turns.normalized_output: %w", err)
	}
	return nil
}

// agentsUseTextID reports whether the agents table still uses the legacy TEXT
// primary key. A missing agents table reports false (fresh database).
func (s *SQLiteStore) agentsUseTextID() (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(agents)`)
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
		if name == "id" {
			return strings.Contains(strings.ToUpper(ctype), "TEXT"), nil
		}
	}
	return false, rows.Err()
}

// ensureAgentsIDSequence makes agents.id start at 10000 (and never reset it
// below the current max). It is idempotent across opens.
func (s *SQLiteStore) ensureAgentsIDSequence() error {
	var maxID int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM agents`).Scan(&maxID); err != nil {
		return err
	}
	seq := int64(9999)
	if maxID > seq {
		seq = maxID
	}
	if _, err := s.db.Exec(`DELETE FROM sqlite_sequence WHERE name = 'agents'`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO sqlite_sequence(name, seq) VALUES('agents', ?)`, seq); err != nil {
		return err
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

// backfillReasonTurnNormalizedOutputs derives normalized_output for rows that
// predate the raw_output/normalized_output split. It is idempotent: rows that
// already have a normalized_output are left untouched.
func (s *SQLiteStore) backfillReasonTurnNormalizedOutputs() error {
	rows, err := s.db.Query(`SELECT id, raw_output FROM reason_turns WHERE normalized_output = ''`)
	if err != nil {
		return err
	}
	type normalizedUpdate struct {
		id         int64
		normalized string
	}
	var updates []normalizedUpdate
	for rows.Next() {
		var (
			id  int64
			raw string
		)
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		normalized := normalizeReasonOutput(raw)
		if normalized != "" {
			updates = append(updates, normalizedUpdate{id: id, normalized: normalized})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := s.db.Exec(`UPDATE reason_turns SET normalized_output = ? WHERE id = ?`, u.normalized, u.id); err != nil {
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
INSERT INTO tasks (id, description, domain, status, agent_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  description=excluded.description,
  domain=excluded.domain,
  status=excluded.status,
  agent_id=excluded.agent_id,
  updated_at=excluded.updated_at
`, task.ID, task.Description, string(task.Domain),
		task.Status, task.AgentID, formatTime(task.CreatedAt), formatTime(task.UpdatedAt))
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

	// New agent: let SQLite allocate the AUTOINCREMENT id (starting at 10000)
	// and derive the name from it.
	if agent.ID == 0 {
		res, err := s.db.Exec(`
INSERT INTO agents (name, state, lifecycle, current_task_id, context, llm_agent_id, llm_provider, model, created_at, updated_at, deleted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
`, "", agent.State, lifecycle, taskID, agent.Context, agent.LLMAgentID, string(agent.LLMProvider), agent.Model,
			formatTime(now), formatTime(now))
		if err != nil {
			return fmt.Errorf("insert agent: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("agent last insert id: %w", err)
		}
		agent.ID = id
		agent.Name = fmt.Sprintf("agent-%d", id)
		if _, err := s.db.Exec(`UPDATE agents SET name = ? WHERE id = ?`, agent.Name, agent.ID); err != nil {
			return fmt.Errorf("set agent name: %w", err)
		}
		return nil
	}

	_, err := s.db.Exec(`
INSERT INTO agents (id, name, state, lifecycle, current_task_id, context, llm_agent_id, llm_provider, model, created_at, updated_at, deleted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name,
  state=excluded.state,
  lifecycle=excluded.lifecycle,
  current_task_id=excluded.current_task_id,
  context=excluded.context,
  llm_agent_id=excluded.llm_agent_id,
  llm_provider=excluded.llm_provider,
  model=excluded.model,
  updated_at=excluded.updated_at,
  deleted_at=NULL
`, agent.ID, agent.Name, agent.State, lifecycle, taskID, agent.Context, agent.LLMAgentID, string(agent.LLMProvider), agent.Model,
		formatTime(now), formatTime(now))
	if err != nil {
		return fmt.Errorf("upsert agent: %w", err)
	}
	return nil
}

func (s *SQLiteStore) SoftDeleteAgent(id int64) error {
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
	if turn.NormalizedOutput == "" {
		turn.NormalizedOutput = normalizeReasonOutput(turn.RawOutput)
	}
	_, err := s.db.Exec(`
INSERT INTO reason_turns (task_id, agent_id, step, mode, llm_provider, model, input, raw_output, normalized_output, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, turn.TaskID, turn.AgentID, turn.Step, string(turn.Mode), string(turn.LLMProvider), turn.Model, turn.Input, turn.RawOutput, turn.NormalizedOutput, formatTime(turn.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert reason turn: %w", err)
	}
	return nil
}
