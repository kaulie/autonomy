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

-- llm_messages is the conversation log for one reason_turns run: the user input
-- and the assistant output are separate rows. The assistant row's parent_id
-- points back at the user row it answers, so a return is traceable to its
-- specific input. UNIQUE(turn_id, seq) keeps the pair stable on replay.
CREATE TABLE IF NOT EXISTS llm_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  turn_id INTEGER NOT NULL DEFAULT 0,
  task_id TEXT NOT NULL DEFAULT '',
  agent_id INTEGER NOT NULL DEFAULT 0,
  step INTEGER NOT NULL DEFAULT 0,
  seq INTEGER NOT NULL DEFAULT 0,
  role TEXT NOT NULL DEFAULT '',
  parent_id INTEGER,
  content TEXT NOT NULL DEFAULT '',
  normalized_content TEXT NOT NULL DEFAULT '',
  llm_provider TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_reason_turns_agent ON reason_turns(agent_id, step);
CREATE INDEX IF NOT EXISTS idx_reason_turns_task ON reason_turns(task_id, step);
CREATE INDEX IF NOT EXISTS idx_agents_task ON agents(current_task_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_events_turn_seq ON llm_events(turn_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_events_run ON llm_events(run_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_events_kind ON llm_events(turn_id, channel, event_type);
CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_messages_turn_seq ON llm_messages(turn_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_messages_parent ON llm_messages(parent_id);
CREATE INDEX IF NOT EXISTS idx_llm_messages_task ON llm_messages(task_id, step);
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
	if err := s.backfillLLMMessages(); err != nil {
		return fmt.Errorf("backfill llm_messages: %w", err)
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

// llmMessageSeqUser is the seq of a run's user-input message. The assistant
// (final return) row is placed one past the aggregated thinking/tool rows, so
// with no aggregated rows it keeps the original seq 1 layout.
const (
	llmMessageSeqUser      = 0
	llmMessageSeqAssistant = 1
)

// insertReasonTurnTx writes the run header inside tx and returns its id.
func insertReasonTurnTx(tx *sql.Tx, turn ReasonTurn) (int64, error) {
	args := reasonTurnInsertArgs(turn)
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ")
	res, err := tx.Exec(
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

// insertMessageTx writes one llm_messages row inside tx and returns its id.
// Re-inserting the same (turn_id, seq) is a no-op that returns the existing id,
// so a replayed finish cannot duplicate the message pair.
func insertMessageTx(tx *sql.Tx, msg LLMMessage) (int64, error) {
	var parent any
	if msg.ParentID != 0 {
		parent = msg.ParentID
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO llm_messages
(turn_id, task_id, agent_id, step, seq, role, parent_id, content, normalized_content, llm_provider, model, run_id, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.TurnID, msg.TaskID, msg.AgentID, msg.Step, msg.Seq, string(msg.Role), parent,
		msg.Content, msg.NormalizedContent, string(msg.LLMProvider), msg.Model, msg.RunID,
		msg.Status, formatTime(msg.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert llm message: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("insert llm message rows: %w", err)
	}
	if affected == 0 {
		var id int64
		if err := tx.QueryRow(`SELECT id FROM llm_messages WHERE turn_id = ? AND seq = ?`,
			msg.TurnID, msg.Seq).Scan(&id); err != nil {
			return 0, fmt.Errorf("lookup existing llm message: %w", err)
		}
		return id, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert llm message id: %w", err)
	}
	return id, nil
}

// userMessage builds the user-input llm_messages row for a run header.
func userMessage(turnID int64, turn ReasonTurn) LLMMessage {
	return LLMMessage{
		TurnID:      turnID,
		TaskID:      turn.TaskID,
		AgentID:     turn.AgentID,
		Step:        turn.Step,
		Seq:         llmMessageSeqUser,
		Role:        LLMMessageRoleUser,
		Content:     turn.Input,
		LLMProvider: turn.LLMProvider,
		Model:       turn.Model,
		RunID:       turn.RunID,
		Status:      turn.Status,
		CreatedAt:   turn.CreatedAt,
	}
}

// InsertReasonTurn writes a complete interaction in one shot: the run header plus
// the user input and assistant output as two linked llm_messages rows.
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
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin reason turn tx: %w", err)
	}
	turnID, err := insertReasonTurnTx(tx, turn)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	inputID, err := insertMessageTx(tx, userMessage(turnID, turn))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	assistant := LLMMessage{
		TurnID:            turnID,
		TaskID:            turn.TaskID,
		AgentID:           turn.AgentID,
		Step:              turn.Step,
		Seq:               llmMessageSeqAssistant,
		Role:              LLMMessageRoleAssistant,
		ParentID:          inputID,
		Content:           turn.RawOutput,
		NormalizedContent: turn.NormalizedOutput,
		LLMProvider:       turn.LLMProvider,
		Model:             turn.Model,
		RunID:             turn.RunID,
		Status:            turn.Status,
		CreatedAt:         turn.EndedAt,
	}
	if _, err := insertMessageTx(tx, assistant); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reason turn: %w", err)
	}
	return nil
}

// BeginReasonTurn opens a run header and records the user-input message so
// stream events can be appended while the run is live. The returned handle links
// the eventual assistant message back to this input.
func (s *SQLiteStore) BeginReasonTurn(turn ReasonTurn) (ReasonTurnHandle, error) {
	if turn.CreatedAt.IsZero() {
		turn.CreatedAt = time.Now()
	}
	if turn.Status == "" {
		turn.Status = string(LLMStatusRunning)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ReasonTurnHandle{}, fmt.Errorf("begin reason turn tx: %w", err)
	}
	turnID, err := insertReasonTurnTx(tx, turn)
	if err != nil {
		_ = tx.Rollback()
		return ReasonTurnHandle{}, err
	}
	inputID, err := insertMessageTx(tx, userMessage(turnID, turn))
	if err != nil {
		_ = tx.Rollback()
		return ReasonTurnHandle{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReasonTurnHandle{}, fmt.Errorf("commit reason turn: %w", err)
	}
	return ReasonTurnHandle{TurnID: turnID, InputMessageID: inputID}, nil
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

// FinishReasonTurn finalizes the run header (status, usage, timing, outputs),
// records the assistant message linked to the user input, and backfills the run
// id onto events written before it was known.
func (s *SQLiteStore) FinishReasonTurn(h ReasonTurnHandle, res LLMRunResult) error {
	if h.TurnID == 0 {
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
		h.TurnID)
	if err != nil {
		return fmt.Errorf("finish reason turn: %w", err)
	}
	events, err := s.ListLLMEvents(h.TurnID)
	if err != nil {
		return err
	}
	assistantSeq, err := s.recordAggregatedMessages(h.TurnID, h.InputMessageID, events)
	if err != nil {
		return err
	}
	if err := s.recordAssistantMessage(h, res, assistantSeq); err != nil {
		return err
	}
	if res.ProviderRunID != "" {
		if _, err := s.db.Exec(`UPDATE llm_events SET run_id = ? WHERE turn_id = ? AND run_id = ''`,
			res.ProviderRunID, h.TurnID); err != nil {
			return fmt.Errorf("backfill llm event run id: %w", err)
		}
	}
	return nil
}

// recordAssistantMessage writes the assistant llm_messages row for a finished
// run, pulling task/agent/step/provider/model from the header (INSERT .. SELECT)
// and linking it back to the user input via parent_id. seq is one past the
// aggregated thinking/tool rows, so the return stays the run's final message. It
// is idempotent: the UNIQUE(turn_id, seq) key plus INSERT OR IGNORE keeps one
// assistant row per run.
func (s *SQLiteStore) recordAssistantMessage(h ReasonTurnHandle, res LLMRunResult, seq int) error {
	ended := res.EndedAt
	if ended.IsZero() {
		ended = time.Now()
	}
	var parent any
	if h.InputMessageID != 0 {
		parent = h.InputMessageID
	}
	_, err := s.db.Exec(`
INSERT OR IGNORE INTO llm_messages
(turn_id, task_id, agent_id, step, seq, role, parent_id, content, normalized_content, llm_provider, model, run_id, status, created_at)
SELECT id, task_id, agent_id, step, ?, ?, ?, ?, ?, llm_provider, model, ?, ?, ?
FROM reason_turns WHERE id = ?
`, seq, string(LLMMessageRoleAssistant), parent,
		res.RawOutput, normalizeReasonOutput(res.RawOutput), res.ProviderRunID, string(res.Status),
		formatTime(ended), h.TurnID)
	if err != nil {
		return fmt.Errorf("insert assistant message: %w", err)
	}
	return nil
}

// recordAggregatedMessages derives the thinking/tool messages from a run's raw
// stream and writes them (seq 1..N, parent_id = the user input). It returns the
// seq the assistant row must use, which is one past the last aggregated row so
// the return stays final. With no aggregated rows that is 1, the original layout.
// It is idempotent: INSERT OR IGNORE on UNIQUE(turn_id, seq) makes a replayed
// finish a no-op.
func (s *SQLiteStore) recordAggregatedMessages(turnID, parentID int64, events []LLMEvent) (int, error) {
	rows := aggregateChatMessages(events)
	for _, m := range rows {
		m.TurnID = turnID
		m.ParentID = parentID
		if err := s.insertDerivedMessage(m); err != nil {
			return 0, err
		}
	}
	return len(rows) + 1, nil
}

// insertDerivedMessage writes one aggregated llm_messages row, copying
// task/agent/step/provider/model/run_id/status from the run header.
func (s *SQLiteStore) insertDerivedMessage(m LLMMessage) error {
	var parent any
	if m.ParentID != 0 {
		parent = m.ParentID
	}
	_, err := s.db.Exec(`
INSERT OR IGNORE INTO llm_messages
(turn_id, task_id, agent_id, step, seq, role, parent_id, content, normalized_content, llm_provider, model, run_id, status, created_at)
SELECT id, task_id, agent_id, step, ?, ?, ?, ?, ?, llm_provider, model, run_id, status, ?
FROM reason_turns WHERE id = ?
`, m.Seq, string(m.Role), parent, m.Content, m.NormalizedContent, formatTime(m.CreatedAt), m.TurnID)
	if err != nil {
		return fmt.Errorf("insert %s message: %w", m.Role, err)
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

// ListLLMMessages returns a run's messages (user input + assistant output) in
// Seq order. An assistant row's ParentID is the id of the user row it answers,
// so a return can be traced back to its specific input.
func (s *SQLiteStore) ListLLMMessages(turnID int64) ([]LLMMessage, error) {
	rows, err := s.db.Query(`SELECT id, turn_id, task_id, agent_id, step, seq, role, parent_id,
content, normalized_content, llm_provider, model, run_id, status, created_at
FROM llm_messages WHERE turn_id = ? ORDER BY seq, id`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query llm messages: %w", err)
	}
	defer rows.Close()
	var out []LLMMessage
	for rows.Next() {
		var (
			m         LLMMessage
			role      string
			provider  string
			parentID  sql.NullInt64
			createdAt string
		)
		if err := rows.Scan(&m.ID, &m.TurnID, &m.TaskID, &m.AgentID, &m.Step, &m.Seq, &role,
			&parentID, &m.Content, &m.NormalizedContent, &provider, &m.Model, &m.RunID, &m.Status,
			&createdAt); err != nil {
			return nil, fmt.Errorf("scan llm message: %w", err)
		}
		m.Role = LLMMessageRole(role)
		m.LLMProvider = LLMProvider(provider)
		if parentID.Valid {
			m.ParentID = parentID.Int64
		}
		m.CreatedAt = parseTime(createdAt)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm messages: %w", err)
	}
	return out, nil
}

// backfillLLMMessages seeds llm_messages from reason_turns rows that predate the
// message table: each header's input becomes a user message and its raw_output
// becomes the assistant message linked to it. It is idempotent (only turns with
// no messages yet are touched) and skips fully empty turns.
func (s *SQLiteStore) backfillLLMMessages() error {
	rows, err := s.db.Query(`SELECT t.id, t.task_id, CAST(t.agent_id AS INTEGER), t.step, t.llm_provider, t.model,
t.input, t.raw_output, t.normalized_output, t.run_id, t.status, t.created_at
FROM reason_turns t
WHERE (t.input <> '' OR t.raw_output <> '')
  AND NOT EXISTS (SELECT 1 FROM llm_messages m WHERE m.turn_id = t.id)`)
	if err != nil {
		return err
	}
	type pendingTurn struct {
		id   int64
		turn ReasonTurn
	}
	var pending []pendingTurn
	for rows.Next() {
		var (
			id, agentID            int64
			step                   int
			taskID, provider       string
			model                  string
			input, raw, normalized string
			runID, status          string
			createdAt              string
		)
		if err := rows.Scan(&id, &taskID, &agentID, &step, &provider, &model, &input, &raw,
			&normalized, &runID, &status, &createdAt); err != nil {
			rows.Close()
			return err
		}
		if normalized == "" {
			normalized = normalizeReasonOutput(raw)
		}
		pending = append(pending, pendingTurn{id: id, turn: ReasonTurn{
			TaskID: taskID, AgentID: agentID, Step: step, LLMProvider: LLMProvider(provider), Model: model,
			Input: input, RawOutput: raw, NormalizedOutput: normalized, RunID: runID, Status: status,
			CreatedAt: parseTime(createdAt),
		}})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, p := range pending {
		if err := s.backfillReasonTurnMessages(p.id, p.turn); err != nil {
			return err
		}
	}
	return nil
}

// backfillReasonTurnMessages writes the message rows for one backfilled turn in
// a single transaction: the user input, the aggregated thinking/tool rows
// derived from any recorded stream, then the assistant output (kept final).
func (s *SQLiteStore) backfillReasonTurnMessages(turnID int64, turn ReasonTurn) error {
	events, err := s.ListLLMEvents(turnID)
	if err != nil {
		return err
	}
	derived := aggregateChatMessages(events)
	for i := range derived {
		derived[i].TurnID = turnID
		derived[i].TaskID = turn.TaskID
		derived[i].AgentID = turn.AgentID
		derived[i].Step = turn.Step
		derived[i].LLMProvider = turn.LLMProvider
		derived[i].Model = turn.Model
		derived[i].RunID = turn.RunID
		derived[i].Status = turn.Status
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	inputID, err := insertMessageTx(tx, userMessage(turnID, turn))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, m := range derived {
		m.ParentID = inputID
		if _, err := insertMessageTx(tx, m); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	assistant := LLMMessage{
		TurnID:            turnID,
		TaskID:            turn.TaskID,
		AgentID:           turn.AgentID,
		Step:              turn.Step,
		Seq:               len(derived) + 1,
		Role:              LLMMessageRoleAssistant,
		ParentID:          inputID,
		Content:           turn.RawOutput,
		NormalizedContent: turn.NormalizedOutput,
		LLMProvider:       turn.LLMProvider,
		Model:             turn.Model,
		RunID:             turn.RunID,
		Status:            turn.Status,
		CreatedAt:         turn.CreatedAt,
	}
	if _, err := insertMessageTx(tx, assistant); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
