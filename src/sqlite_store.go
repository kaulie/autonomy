package autonomy

import (
	"database/sql"
	"encoding/json"
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
  llm_agent_id TEXT NOT NULL DEFAULT '',
  input TEXT NOT NULL DEFAULT '',
  raw_output TEXT NOT NULL DEFAULT '',
  normalized_output TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  error_code TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  event_count INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  cost_cents REAL,
  started_at TEXT,
  ended_at TEXT,
  created_at TEXT NOT NULL
);

-- llm_events is the raw, provider-neutral stream for one reason_turns run.
-- payload keeps the provider's event verbatim; the extra columns only make the
-- stream queryable without JSON parsing. UNIQUE(turn_id, seq) makes appends
-- idempotent when a dropped stream is replayed via the WaitLiveRun fallback.
CREATE TABLE IF NOT EXISTS llm_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  turn_id INTEGER NOT NULL DEFAULT 0,
  run_id TEXT NOT NULL DEFAULT '',
  seq INTEGER NOT NULL DEFAULT 0,
  offset_token TEXT NOT NULL DEFAULT '',
  channel TEXT NOT NULL DEFAULT '',
  event_type TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  text_delta TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL DEFAULT '{}',
  elapsed_ms INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_reason_turns_agent ON reason_turns(agent_id, step);
CREATE INDEX IF NOT EXISTS idx_reason_turns_task ON reason_turns(task_id, step);
CREATE INDEX IF NOT EXISTS idx_agents_task ON agents(current_task_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_events_turn_seq ON llm_events(turn_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_events_run ON llm_events(run_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_events_kind ON llm_events(turn_id, channel, event_type);
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
	// Run-header columns for streamed LLM interactions. The stream itself lives
	// in llm_events (created above); these keep the run summary on the header.
	// Fresh databases already create them; this keeps older DBs usable.
	runHeaderColumns := []struct{ name, decl string }{
		{"llm_agent_id", "TEXT NOT NULL DEFAULT ''"},
		{"run_id", "TEXT NOT NULL DEFAULT ''"},
		{"status", "TEXT NOT NULL DEFAULT ''"},
		{"error_code", "TEXT NOT NULL DEFAULT ''"},
		{"error_message", "TEXT NOT NULL DEFAULT ''"},
		{"duration_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"event_count", "INTEGER NOT NULL DEFAULT 0"},
		{"input_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"output_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"cache_read_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"cache_write_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"reasoning_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"total_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"cost_cents", "REAL"},
		{"started_at", "TEXT"},
		{"ended_at", "TEXT"},
	}
	for _, c := range runHeaderColumns {
		if err := s.ensureColumn("reason_turns", c.name, c.decl); err != nil {
			return fmt.Errorf("migrate reason_turns.%s: %w", c.name, err)
		}
	}
	// Index the run id only after the column is guaranteed to exist, so legacy
	// databases without run_id migrate cleanly.
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_reason_turns_run ON reason_turns(run_id)`); err != nil {
		return fmt.Errorf("migrate reason_turns run index: %w", err)
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

// reasonTurnColumns lists the insertable reason_turns columns in the order
// used by reasonTurnInsertArgs.
const reasonTurnColumns = "task_id, agent_id, step, mode, llm_provider, model, llm_agent_id, " +
	"input, raw_output, normalized_output, run_id, status, error_code, error_message, " +
	"duration_ms, event_count, input_tokens, output_tokens, cache_read_tokens, " +
	"cache_write_tokens, reasoning_tokens, total_tokens, cost_cents, started_at, ended_at, created_at"

func reasonTurnInsertArgs(turn ReasonTurn) []any {
	return []any{
		turn.TaskID, turn.AgentID, turn.Step, string(turn.Mode), string(turn.LLMProvider), turn.Model,
		turn.LLMAgentID, turn.Input, turn.RawOutput, turn.NormalizedOutput,
		turn.RunID, turn.Status, turn.ErrorCode, turn.ErrorMessage,
		turn.DurationMS, turn.EventCount, turn.InputTokens, turn.OutputTokens,
		turn.CacheReadTokens, turn.CacheWriteTokens, turn.ReasoningTokens, turn.TotalTokens,
		nullFloatArg(turn.CostCents), nullTimeArg(turn.StartedAt), nullTimeArg(turn.EndedAt),
		formatTime(turn.CreatedAt),
	}
}

func (s *SQLiteStore) insertReasonTurn(turn ReasonTurn) (int64, error) {
	args := reasonTurnInsertArgs(turn)
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ")
	res, err := s.db.Exec(
		"INSERT INTO reason_turns ("+reasonTurnColumns+") VALUES ("+placeholders+")", args...)
	if err != nil {
		return 0, fmt.Errorf("insert reason turn: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert reason turn id: %w", err)
	}
	return id, nil
}

// InsertReasonTurn writes a complete interaction in one shot.
func (s *SQLiteStore) InsertReasonTurn(turn ReasonTurn) error {
	if turn.CreatedAt.IsZero() {
		turn.CreatedAt = time.Now()
	}
	if turn.NormalizedOutput == "" {
		turn.NormalizedOutput = normalizeReasonOutput(turn.RawOutput)
	}
	if turn.Status == "" {
		turn.Status = string(LLMStatusFinished)
	}
	if turn.StartedAt.IsZero() {
		turn.StartedAt = turn.CreatedAt
	}
	if turn.EndedAt.IsZero() {
		turn.EndedAt = turn.CreatedAt
	}
	_, err := s.insertReasonTurn(turn)
	return err
}

// BeginReasonTurn opens a run header so stream events can be appended while the
// run is live. It returns the header id used by AppendLLMEvents/FinishReasonTurn.
func (s *SQLiteStore) BeginReasonTurn(turn ReasonTurn) (int64, error) {
	if turn.CreatedAt.IsZero() {
		turn.CreatedAt = time.Now()
	}
	if turn.Status == "" {
		turn.Status = string(LLMStatusRunning)
	}
	return s.insertReasonTurn(turn)
}

// AppendLLMEvents appends a batch of stream events to a run in one transaction.
// INSERT OR IGNORE keeps appends idempotent on the UNIQUE(turn_id, seq) key, so
// a replayed stream (WaitLiveRun fallback) cannot double-write the same seq.
func (s *SQLiteStore) AppendLLMEvents(turnID int64, runID string, events []LLMEvent) error {
	if turnID == 0 || len(events) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin llm events tx: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO llm_events
(turn_id, run_id, seq, offset_token, channel, event_type, role, name, text_delta, payload, elapsed_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare llm event insert: %w", err)
	}
	defer stmt.Close()
	for _, ev := range events {
		if _, err := stmt.Exec(
			turnID, runID, ev.Seq, ev.OffsetToken, string(ev.Channel), ev.EventType,
			ev.Role, ev.Name, ev.TextDelta, ev.PayloadJSON(), ev.ElapsedMS, formatTime(ev.CreatedAt),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("append llm event seq=%d: %w", ev.Seq, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit llm events: %w", err)
	}
	return nil
}

// FinishReasonTurn finalizes the run header (status, usage, timing, outputs) and
// backfills the run id onto events written before it was known.
func (s *SQLiteStore) FinishReasonTurn(turnID int64, res LLMRunResult) error {
	if turnID == 0 {
		return nil
	}
	normalized := normalizeReasonOutput(res.RawOutput)
	_, err := s.db.Exec(`
UPDATE reason_turns SET
  raw_output = ?, normalized_output = ?,
  run_id = COALESCE(NULLIF(?, ''), run_id),
  llm_agent_id = COALESCE(NULLIF(?, ''), llm_agent_id),
  status = ?, error_code = ?, error_message = ?, duration_ms = ?, event_count = ?,
  input_tokens = ?, output_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?,
  reasoning_tokens = ?, total_tokens = ?, cost_cents = ?, ended_at = ?
WHERE id = ?
`, res.RawOutput, normalized, res.ProviderRunID, res.LLMAgentID, string(res.Status),
		res.ErrorCode, res.ErrorMessage, res.DurationMS, res.EventCount,
		res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.CacheReadTokens, res.Usage.CacheWriteTokens,
		res.Usage.ReasoningTokens, res.Usage.TotalTokens, usageCostArg(res.Usage), nullTimeArg(res.EndedAt),
		turnID)
	if err != nil {
		return fmt.Errorf("finish reason turn: %w", err)
	}
	if res.ProviderRunID != "" {
		if _, err := s.db.Exec(`UPDATE llm_events SET run_id = ? WHERE turn_id = ? AND run_id = ''`,
			res.ProviderRunID, turnID); err != nil {
			return fmt.Errorf("backfill llm event run id: %w", err)
		}
	}
	return nil
}

// ListLLMEvents returns a run's stream events in Seq order for replay/analysis.
func (s *SQLiteStore) ListLLMEvents(turnID int64) ([]LLMEvent, error) {
	rows, err := s.db.Query(`SELECT seq, offset_token, channel, event_type, role, name, text_delta, payload, elapsed_ms, created_at
FROM llm_events WHERE turn_id = ? ORDER BY seq`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query llm events: %w", err)
	}
	defer rows.Close()
	var out []LLMEvent
	for rows.Next() {
		var (
			ev        LLMEvent
			channel   string
			payload   string
			createdAt string
		)
		if err := rows.Scan(&ev.Seq, &ev.OffsetToken, &channel, &ev.EventType, &ev.Role, &ev.Name,
			&ev.TextDelta, &payload, &ev.ElapsedMS, &createdAt); err != nil {
			return nil, fmt.Errorf("scan llm event: %w", err)
		}
		ev.Channel = LLMEventChannel(channel)
		ev.CreatedAt = parseTime(createdAt)
		if payload != "" {
			var m map[string]any
			if err := json.Unmarshal([]byte(payload), &m); err == nil {
				ev.Payload = m
			}
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm events: %w", err)
	}
	return out, nil
}
