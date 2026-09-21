package db

import . "github.com/kaulie/autonomy/src"

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresStore is a Store backed by PostgreSQL (pgx over database/sql). It is the
// postgres engine's implementation of every Store port: tasks and agents and the
// conversation (this file, with postgres_query.go), the execution record
// (postgres_execution.go), the inbox (postgres_inbox.go), verification
// (postgres_verification.go), and the read-only log the data API walks
// (postgres_turns.go).
//
// It is a second implementation of the same contract, not a wrapper around the
// sqlite one: the schema is PostgreSQL's (BIGINT identity ids, timestamptz columns,
// numbered parameters, RETURNING instead of LastInsertId), and it exists so the
// database behind the runtime can change without touching a single caller
// (docs/store.md).
//
// Two consequences worth knowing when reading the SQL below:
//
//   - Every inserted id is read back with RETURNING, because PostgreSQL has no
//     LastInsertId.
//   - Where sqlite says INSERT OR IGNORE, PostgreSQL says ON CONFLICT DO NOTHING
//     against the same unique key, so a replayed write stays a no-op.
type PostgresStore struct {
	db *sql.DB
}

// RawDB exposes the engine's underlying connection. See SQLiteStore.RawDB: it is
// the seam tests use, not something the runtime calls.
func (s *PostgresStore) RawDB() *sql.DB { return s.db }

// OpenPostgresStore connects to the database at dsn, checks the connection, and
// makes the schema it needs.
//
// It migrates nothing from anywhere: a SQLite path is a whole database whose data
// stays where it is (docs/store.md「两个引擎，两份数据」). This engine's own schema
// is created idempotently on every open, so an existing postgres database is left as
// it is.
func OpenPostgresStore(dsn string) (*PostgresStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("open postgres: empty dsn")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	// Several goroutines of one runtime write here (a run's own records, and the inbox
	// consumer running the same agent's next message), so the pool is a pool, and a
	// connection is recycled rather than held for the life of the process.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)
	// Ping with a deadline: an unreachable server must fail the open in seconds, not
	// hang a restart until the socket gives up on its own.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	s := &PostgresStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the engine's connection pool.
func (s *PostgresStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// migrate makes the schema. Every statement is CREATE ... IF NOT EXISTS, so opening
// an existing database is a no-op, and there is no rename/copy step here on purpose:
// this engine has no older schema of its own to carry (the sqlite engine keeps its
// schema — and its data).
func (s *PostgresStore) migrate() error {
	for _, ddl := range []string{postgresSchema, pgExecutionDDL, pgVerificationDDL, pgInboxDDL} {
		if _, err := s.db.Exec(ddl); err != nil {
			return fmt.Errorf("migrate postgres schema: %w", err)
		}
	}
	if err := s.ensureIDSequences(); err != nil {
		return fmt.Errorf("migrate postgres id sequences: %w", err)
	}
	return nil
}

// ensureIDSequences puts every id space a consumer sees at its contract floor
// (AgentIDBase / MessageIDBase) and never rewinds one below the highest id already stored:
// reopening a database must not hand out an id that is in use. It is idempotent, and it runs
// after the schema exists because it reads each table's sequence through
// pg_get_serial_sequence. The table names are this file's own identifiers, never caller
// input.
func (s *PostgresStore) ensureIDSequences() error {
	for _, space := range []struct {
		table string
		base  int64
	}{
		{"agents", AgentIDBase},
		{"agent_messages", MessageIDBase},
		{"llm_messages", MessageIDBase},
	} {
		query := fmt.Sprintf(`
SELECT setval(pg_get_serial_sequence('%s', 'id'),
              GREATEST(%d, COALESCE((SELECT MAX(id) FROM %s), 0)))`,
			space.table, space.base-1, space.table)
		if _, err := s.db.Exec(query); err != nil {
			return fmt.Errorf("%s.id: %w", space.table, err)
		}
	}
	return nil
}

// postgresSchema is the task/agent/conversation half of the schema: the five core
// tables plus the indexes the runtime and the readers walk them with. The execution,
// verification and inbox tables live next to the ports that own them
// (postgres_execution.go, postgres_verification.go, postgres_inbox.go).
//
// Times are timestamptz columns rather than text: the database can order, compare and
// index them, and a reader that is not Go (a SQL console, a reporting query) gets a
// timestamp instead of a string it has to parse. The Go contract is unchanged — the
// read ports hand out the same RFC3339 text (pgTimeText).
//
// JSON columns stay text and are kept verbatim, because for those the *text* is the
// record — a criterion's own words, a provider's raw event — and a jsonb round trip
// would hand back a reformatted document. Ids are BIGINT identity columns:
// PostgreSQL allocates them and the engine reads them back with RETURNING.
//
// There is not one FOREIGN KEY, exactly as in the sqlite schema: the links between
// these tables are soft (llm_messages.turn_id -> reason_turns.id,
// execution_step_interaction.reason_turn_id -> reason_turns.id, …), spelled out in the
// column names, so no delete cascades and llm_events stays a leaf.
const postgresSchema = `
CREATE TABLE IF NOT EXISTS tasks (
  id          TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  domain      TEXT NOT NULL DEFAULT '',
  goal_type   TEXT NOT NULL DEFAULT '',      -- GoalType: what the task was accepted as
  context_ref TEXT NOT NULL DEFAULT '{}',    -- ContextContainerType -> container id (JSON object)
  status      TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT '',
  agent_id    BIGINT NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  id              BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  name            TEXT NOT NULL DEFAULT '',
  state           TEXT NOT NULL DEFAULT '',
  lifecycle       TEXT NOT NULL DEFAULT 'ephemeral',
  current_task_id TEXT NOT NULL DEFAULT '',
  context         TEXT NOT NULL DEFAULT '',
  llm_agent_id    TEXT NOT NULL DEFAULT '',
  llm_provider    TEXT NOT NULL DEFAULT '',
  model           TEXT NOT NULL DEFAULT '',
  created_at      TIMESTAMPTZ NOT NULL,
  updated_at      TIMESTAMPTZ NOT NULL,
  deleted_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_agents_task ON agents(current_task_id);

CREATE TABLE IF NOT EXISTS reason_turns (
  id                 BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  task_id            TEXT NOT NULL DEFAULT '',
  agent_id           BIGINT NOT NULL DEFAULT 0,
  cycle              INTEGER NOT NULL DEFAULT 0,
  mode               TEXT NOT NULL DEFAULT '',
  llm_provider       TEXT NOT NULL DEFAULT '',
  model              TEXT NOT NULL DEFAULT '',
  llm_agent_id       TEXT NOT NULL DEFAULT '',
  input              TEXT NOT NULL DEFAULT '',
  raw_output         TEXT NOT NULL DEFAULT '',
  normalized_output  TEXT NOT NULL DEFAULT '',
  run_id             TEXT NOT NULL DEFAULT '',
  status             TEXT NOT NULL DEFAULT '',
  error_code         TEXT NOT NULL DEFAULT '',
  error_message      TEXT NOT NULL DEFAULT '',
  duration_ms        BIGINT NOT NULL DEFAULT 0,
  event_count        INTEGER NOT NULL DEFAULT 0,
  input_tokens       BIGINT NOT NULL DEFAULT 0,
  output_tokens      BIGINT NOT NULL DEFAULT 0,
  cache_read_tokens  BIGINT NOT NULL DEFAULT 0,
  cache_write_tokens BIGINT NOT NULL DEFAULT 0,
  reasoning_tokens   BIGINT NOT NULL DEFAULT 0,
  total_tokens       BIGINT NOT NULL DEFAULT 0,
  cost_cents         DOUBLE PRECISION,
  started_at         TIMESTAMPTZ,
  ended_at           TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_reason_turns_agent ON reason_turns(agent_id, cycle);
CREATE INDEX IF NOT EXISTS idx_reason_turns_task ON reason_turns(task_id, cycle);
CREATE INDEX IF NOT EXISTS idx_reason_turns_run ON reason_turns(run_id);
CREATE INDEX IF NOT EXISTS idx_reason_turns_active ON reason_turns(task_id, agent_id, status);

-- llm_events is the raw, provider-neutral stream of one run. UNIQUE(turn_id, seq)
-- makes an append idempotent, so a replayed stream cannot double-write a seq.
CREATE TABLE IF NOT EXISTS llm_events (
  id           BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  turn_id      BIGINT NOT NULL DEFAULT 0,
  run_id       TEXT NOT NULL DEFAULT '',
  seq          INTEGER NOT NULL DEFAULT 0,
  offset_token TEXT NOT NULL DEFAULT '',
  channel      TEXT NOT NULL DEFAULT '',
  kind         TEXT NOT NULL DEFAULT '',
  event_type   TEXT NOT NULL DEFAULT '',
  role         TEXT NOT NULL DEFAULT '',
  name         TEXT NOT NULL DEFAULT '',
  text_delta   TEXT NOT NULL DEFAULT '',
  payload      TEXT NOT NULL DEFAULT '{}',
  elapsed_ms   BIGINT NOT NULL DEFAULT 0,
  created_at   TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_events_turn_seq ON llm_events(turn_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_events_run ON llm_events(run_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_events_kind ON llm_events(turn_id, channel, event_type);
CREATE INDEX IF NOT EXISTS idx_llm_events_turn_kind ON llm_events(turn_id, kind);

-- llm_messages is the conversation log of one run: the user input, the aggregated
-- thinking/tool rows, and the assistant return, each its own row. An assistant row's
-- parent_id points at the input it answers.
CREATE TABLE IF NOT EXISTS llm_messages (
  id                 BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  turn_id            BIGINT NOT NULL DEFAULT 0,
  task_id            TEXT NOT NULL DEFAULT '',
  agent_id           BIGINT NOT NULL DEFAULT 0,
  cycle              INTEGER NOT NULL DEFAULT 0,
  seq                INTEGER NOT NULL DEFAULT 0,
  role               TEXT NOT NULL DEFAULT '',
  parent_id          BIGINT,
  content            TEXT NOT NULL DEFAULT '',
  normalized_content TEXT NOT NULL DEFAULT '',
  llm_provider       TEXT NOT NULL DEFAULT '',
  model              TEXT NOT NULL DEFAULT '',
  run_id             TEXT NOT NULL DEFAULT '',
  status             TEXT NOT NULL DEFAULT '',
  created_at         TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_messages_turn_seq ON llm_messages(turn_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_messages_parent ON llm_messages(parent_id);
CREATE INDEX IF NOT EXISTS idx_llm_messages_task ON llm_messages(task_id, cycle);
CREATE INDEX IF NOT EXISTS idx_llm_messages_agent_id ON llm_messages(task_id, agent_id, id);
`

// UpsertTask writes one task row. goal_type / context_ref / agent_id keep the same
// empty-value semantics as everywhere else in the contract: a write that says nothing
// about what the task is (the Task values the runtime upserts on its way through a
// run) leaves what is already there, and only a write that names a new value takes it
// away (docs/store.md, docs/task.md).
func (s *PostgresStore) UpsertTask(task *Task) error {
	if task == nil {
		return fmt.Errorf("nil task")
	}
	now := time.Now()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	_, err := s.db.Exec(`
INSERT INTO tasks (id, description, domain, goal_type, context_ref, status, error, agent_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT(id) DO UPDATE SET
  description=excluded.description,
  domain=excluded.domain,
  -- A write that says nothing new about what the task is does not take it away:
  -- goal_type and context_ref are what the task was accepted as.
  goal_type=CASE WHEN excluded.goal_type = '' THEN tasks.goal_type ELSE excluded.goal_type END,
  context_ref=CASE WHEN excluded.context_ref = '{}' THEN tasks.context_ref ELSE excluded.context_ref END,
  status=excluded.status,
  error=excluded.error,
  -- A write that names no agent does not un-pair the task: the task's agent is what a
  -- later instruction resumes, and a Task value on its way through a run need not
  -- carry it.
  agent_id=CASE WHEN excluded.agent_id = 0 THEN tasks.agent_id ELSE excluded.agent_id END,
  updated_at=excluded.updated_at
`, task.ID, task.Description, string(task.Domain), string(task.GoalType), taskContextRefJSON(task.ContextRef),
		task.Status, task.Error, task.AgentID, pgTime(task.CreatedAt), pgTime(task.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert task: %w", err)
	}
	return nil
}

// UpsertAgent writes one agent row.
func (s *PostgresStore) UpsertAgent(agent *Agent) error {
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

	// New agent: let PostgreSQL allocate the id (starting at 10000, see
	// ensureAgentsIDSequence) and derive the name from it.
	if agent.ID == 0 {
		var id int64
		err := s.db.QueryRow(`
INSERT INTO agents (name, state, lifecycle, current_task_id, context, llm_agent_id, llm_provider, model, created_at, updated_at, deleted_at)
VALUES ('', $1, $2, $3, $4, $5, $6, $7, $8, $8, NULL)
RETURNING id
`, agent.State, lifecycle, taskID, agent.Context, agent.LLMAgentID, string(agent.LLMProvider), agent.Model,
			pgTime(now)).Scan(&id)
		if err != nil {
			return fmt.Errorf("insert agent: %w", err)
		}
		agent.ID = id
		agent.Name = fmt.Sprintf("agent-%d", id)
		if _, err := s.db.Exec(`UPDATE agents SET name = $1 WHERE id = $2`, agent.Name, agent.ID); err != nil {
			return fmt.Errorf("set agent name: %w", err)
		}
		return nil
	}

	_, err := s.db.Exec(`
INSERT INTO agents (id, name, state, lifecycle, current_task_id, context, llm_agent_id, llm_provider, model, created_at, updated_at, deleted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, NULL)
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
`, agent.ID, agent.Name, agent.State, lifecycle, taskID, agent.Context, agent.LLMAgentID,
		string(agent.LLMProvider), agent.Model, pgTime(now))
	if err != nil {
		return fmt.Errorf("upsert agent: %w", err)
	}
	return nil
}

// SoftDeleteAgent marks an agent deleted without removing its row: the runs it took
// part in still name it.
func (s *PostgresStore) SoftDeleteAgent(id int64) error {
	now := pgTime(time.Now())
	_, err := s.db.Exec(`
UPDATE agents SET deleted_at = $1, updated_at = $1, state = 'deleted'
WHERE id = $2 AND deleted_at IS NULL
`, now, id)
	if err != nil {
		return fmt.Errorf("soft-delete agent: %w", err)
	}
	return nil
}

// pgReasonTurnColumns lists the insertable reason_turns columns in the order
// pgReasonTurnArgs fills them.
const pgReasonTurnColumns = "task_id, agent_id, cycle, mode, llm_provider, model, llm_agent_id, " +
	"input, raw_output, normalized_output, run_id, status, error_code, error_message, " +
	"duration_ms, event_count, input_tokens, output_tokens, cache_read_tokens, " +
	"cache_write_tokens, reasoning_tokens, total_tokens, cost_cents, started_at, ended_at, created_at"

// pgReasonTurnArgs is one reason_turns row as statement arguments. A zero cost is
// NULL (the provider did not say, which is not the same as 0), and a zero time in a
// nullable column is NULL as well.
func pgReasonTurnArgs(turn ReasonTurn) []any {
	return []any{
		turn.TaskID, turn.AgentID, turn.Cycle, string(turn.Mode), string(turn.LLMProvider), turn.Model,
		turn.LLMAgentID, turn.Input, turn.RawOutput, turn.NormalizedOutput,
		turn.RunID, turn.Status, turn.ErrorCode, turn.ErrorMessage,
		turn.DurationMS, turn.EventCount, turn.InputTokens, turn.OutputTokens,
		turn.CacheReadTokens, turn.CacheWriteTokens, turn.ReasoningTokens, turn.TotalTokens,
		pgFloat(turn.CostCents), pgNullTime(turn.StartedAt), pgNullTime(turn.EndedAt),
		pgTime(turn.CreatedAt),
	}
}

// pgInsertReasonTurnTx writes the run header inside tx and returns its id.
func pgInsertReasonTurnTx(tx *sql.Tx, turn ReasonTurn) (int64, error) {
	args := pgReasonTurnArgs(turn)
	var id int64
	err := tx.QueryRow(
		"INSERT INTO reason_turns ("+pgReasonTurnColumns+") VALUES ("+pgPlaceholders(len(args))+") RETURNING id",
		args...).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert reason turn: %w", err)
	}
	return id, nil
}

// pgInsertMessageTx writes one llm_messages row inside tx and returns its id.
// Re-inserting the same (turn_id, seq) is a no-op that returns the existing id, so a
// replayed finish cannot duplicate the message pair.
func pgInsertMessageTx(tx *sql.Tx, msg LLMMessage) (int64, error) {
	var id int64
	err := tx.QueryRow(`
INSERT INTO llm_messages
(turn_id, task_id, agent_id, cycle, seq, role, parent_id, content, normalized_content, llm_provider, model, run_id, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (turn_id, seq) DO NOTHING
RETURNING id
`, msg.TurnID, msg.TaskID, msg.AgentID, msg.Cycle, msg.Seq, string(msg.Role), nullID(msg.ParentID),
		msg.Content, msg.NormalizedContent, string(msg.LLMProvider), msg.Model, msg.RunID,
		msg.Status, pgTime(msg.CreatedAt)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// DO NOTHING wrote nothing: the row is already there, and its id is the one the
		// caller must keep using (the assistant row is linked to it).
		if err := tx.QueryRow(`SELECT id FROM llm_messages WHERE turn_id = $1 AND seq = $2`,
			msg.TurnID, msg.Seq).Scan(&id); err != nil {
			return 0, fmt.Errorf("lookup existing llm message: %w", err)
		}
		return id, nil
	}
	if err != nil {
		return 0, fmt.Errorf("insert llm message: %w", err)
	}
	return id, nil
}

// InsertReasonTurn writes a complete interaction in one shot: the run header plus the
// user input and assistant output as two linked llm_messages rows.
func (s *PostgresStore) InsertReasonTurn(turn ReasonTurn) error {
	if turn.CreatedAt.IsZero() {
		turn.CreatedAt = time.Now()
	}
	if turn.NormalizedOutput == "" {
		turn.NormalizedOutput = NormalizeReasonOutput(turn.RawOutput)
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
	turnID, err := pgInsertReasonTurnTx(tx, turn)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	inputID, err := pgInsertMessageTx(tx, InputMessage(turnID, turn))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	assistant := LLMMessage{
		TurnID:            turnID,
		TaskID:            turn.TaskID,
		AgentID:           turn.AgentID,
		Cycle:             turn.Cycle,
		Seq:               LLMMessageSeqAssistant,
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
	if _, err := pgInsertMessageTx(tx, assistant); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reason turn: %w", err)
	}
	return nil
}

// BeginReasonTurn opens a run header and records the user-input message, so stream
// events can be appended while the run is live. The returned handle links the
// eventual assistant message back to this input.
func (s *PostgresStore) BeginReasonTurn(turn ReasonTurn) (ReasonTurnHandle, error) {
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
	turnID, err := pgInsertReasonTurnTx(tx, turn)
	if err != nil {
		_ = tx.Rollback()
		return ReasonTurnHandle{}, err
	}
	inputID, err := pgInsertMessageTx(tx, InputMessage(turnID, turn))
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
// ON CONFLICT DO NOTHING keeps appends idempotent on the UNIQUE(turn_id, seq) key,
// so a replayed stream (the WaitLiveRun fallback) cannot double-write a seq.
func (s *PostgresStore) AppendLLMEvents(turnID int64, runID string, events []LLMEvent) error {
	if turnID == 0 || len(events) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin llm events tx: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO llm_events
(turn_id, run_id, seq, offset_token, channel, kind, event_type, role, name, text_delta, payload, elapsed_ms, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (turn_id, seq) DO NOTHING`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare llm event insert: %w", err)
	}
	defer stmt.Close()
	for _, ev := range events {
		if _, err := stmt.Exec(
			turnID, runID, ev.Seq, ev.OffsetToken, string(ev.Channel), string(ev.Kind), ev.EventType,
			ev.Role, ev.Name, ev.TextDelta, ev.PayloadJSON(), ev.ElapsedMS, pgTime(ev.CreatedAt),
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

// FinishReasonTurn finalizes the run header (status, usage, timing, outputs), records
// the assistant message linked to the user input, and backfills the run id onto events
// written before it was known.
func (s *PostgresStore) FinishReasonTurn(h ReasonTurnHandle, res LLMRunResult) error {
	if h.TurnID == 0 {
		return nil
	}
	normalized := NormalizeReasonOutput(res.RawOutput)
	_, err := s.db.Exec(`
UPDATE reason_turns SET
  raw_output = $1, normalized_output = $2,
  run_id = COALESCE(NULLIF($3, ''), run_id),
  llm_agent_id = COALESCE(NULLIF($4, ''), llm_agent_id),
  status = $5, error_code = $6, error_message = $7, duration_ms = $8, event_count = $9,
  input_tokens = $10, output_tokens = $11, cache_read_tokens = $12, cache_write_tokens = $13,
  reasoning_tokens = $14, total_tokens = $15, cost_cents = $16, ended_at = $17
WHERE id = $18
`, res.RawOutput, normalized, res.ProviderRunID, res.LLMAgentID, string(res.Status),
		res.ErrorCode, res.ErrorMessage, res.DurationMS, res.EventCount,
		res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.CacheReadTokens, res.Usage.CacheWriteTokens,
		res.Usage.ReasoningTokens, res.Usage.TotalTokens, pgUsageCost(res.Usage), pgNullTime(res.EndedAt),
		h.TurnID)
	if err != nil {
		return fmt.Errorf("finish reason turn: %w", err)
	}
	// The stream may already have been aggregated while it ran (LLMTrace writes each
	// message as it completes). Re-deriving here is the safety net for callers that only
	// appended events; the upsert makes the second pass a no-op for rows already correct.
	events, err := s.ListLLMEvents(h.TurnID)
	if err != nil {
		return err
	}
	if err := s.recordAggregatedMessages(h.TurnID, h.InputMessageID, events); err != nil {
		return err
	}
	// The assistant row is written once, at the seq one past everything stored so far
	// (aggregated rows may have been written while the run streamed). A replayed finish
	// finds it already there and leaves it alone.
	if _, exists, err := s.assistantMessageSeq(h.TurnID); err != nil {
		return err
	} else if !exists {
		assistantSeq, err := s.nextMessageSeq(h.TurnID)
		if err != nil {
			return err
		}
		if err := s.recordAssistantMessage(h, res, assistantSeq); err != nil {
			return err
		}
	}
	if res.ProviderRunID != "" {
		if _, err := s.db.Exec(`UPDATE llm_events SET run_id = $1 WHERE turn_id = $2 AND run_id = ''`,
			res.ProviderRunID, h.TurnID); err != nil {
			return fmt.Errorf("backfill llm event run id: %w", err)
		}
	}
	return nil
}

// recordAssistantMessage writes the assistant llm_messages row for a finished run,
// pulling task/agent/cycle/provider/model from the header (INSERT .. SELECT) and
// linking it back to the user input via parent_id. seq is one past the aggregated
// thinking/tool rows, so the return stays the run's final message; the unique key plus
// DO NOTHING keeps one assistant row per run.
func (s *PostgresStore) recordAssistantMessage(h ReasonTurnHandle, res LLMRunResult, seq int) error {
	ended := res.EndedAt
	if ended.IsZero() {
		ended = time.Now()
	}
	_, err := s.db.Exec(`
INSERT INTO llm_messages
(turn_id, task_id, agent_id, cycle, seq, role, parent_id, content, normalized_content, llm_provider, model, run_id, status, created_at)
SELECT id, task_id, agent_id, cycle, $1, $2, $3, $4, $5, llm_provider, model, $6, $7, $8
FROM reason_turns WHERE id = $9
ON CONFLICT (turn_id, seq) DO NOTHING
`, seq, string(LLMMessageRoleAssistant), nullID(h.InputMessageID),
		res.RawOutput, NormalizeReasonOutput(res.RawOutput), res.ProviderRunID, string(res.Status),
		pgTime(ended), h.TurnID)
	if err != nil {
		return fmt.Errorf("insert assistant message: %w", err)
	}
	return nil
}

// recordAggregatedMessages derives the thinking/tool messages from a run's raw stream
// and writes them (seq 1..N, parent_id = the user input). Re-writing a row is an
// upsert, so a replayed finish is a no-op.
func (s *PostgresStore) recordAggregatedMessages(turnID, parentID int64, events []LLMEvent) error {
	for _, m := range AggregateChatMessages(events) {
		m.TurnID = turnID
		m.ParentID = parentID
		if err := s.insertDerivedMessage(m); err != nil {
			return err
		}
	}
	return nil
}

// pgUpsertDerivedMessageSQL writes one aggregated (thinking/tool) message row,
// copying task/agent/cycle/provider/model/run_id/status from the run header. It is
// keyed by (turn_id, seq): a re-write updates the row in place, so a message that is
// persisted while the run streams and then grows (or is re-derived at finish) stays one
// row with its original id.
const pgUpsertDerivedMessageSQL = `
INSERT INTO llm_messages
(turn_id, task_id, agent_id, cycle, seq, role, parent_id, content, normalized_content, llm_provider, model, run_id, status, created_at)
SELECT id, task_id, agent_id, cycle, $1, $2, $3, $4, $5, llm_provider, model, run_id, status, $6
FROM reason_turns WHERE id = $7
ON CONFLICT (turn_id, seq) DO UPDATE SET
  role = excluded.role,
  parent_id = excluded.parent_id,
  content = excluded.content,
  normalized_content = excluded.normalized_content,
  created_at = excluded.created_at
`

// AppendLLMMessages upserts aggregated messages for a run that is still streaming
// (see the Store contract). Idempotent on (turn_id, seq).
func (s *PostgresStore) AppendLLMMessages(turnID int64, messages []LLMMessage) error {
	if turnID == 0 || len(messages) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin llm messages tx: %w", err)
	}
	stmt, err := tx.Prepare(pgUpsertDerivedMessageSQL)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare llm message insert: %w", err)
	}
	defer stmt.Close()
	for _, m := range messages {
		if _, err := stmt.Exec(m.Seq, string(m.Role), nullID(m.ParentID), m.Content, m.NormalizedContent,
			pgTime(m.CreatedAt), turnID); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("append %s message seq=%d: %w", m.Role, m.Seq, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit llm messages: %w", err)
	}
	return nil
}

// insertDerivedMessage writes one aggregated llm_messages row (see
// pgUpsertDerivedMessageSQL).
func (s *PostgresStore) insertDerivedMessage(m LLMMessage) error {
	if _, err := s.db.Exec(pgUpsertDerivedMessageSQL,
		m.Seq, string(m.Role), nullID(m.ParentID), m.Content, m.NormalizedContent, pgTime(m.CreatedAt), m.TurnID); err != nil {
		return fmt.Errorf("insert %s message: %w", m.Role, err)
	}
	return nil
}

// assistantMessageSeq reports whether the run already has its assistant row, and at
// which seq. A replayed finish must not append a second one.
func (s *PostgresStore) assistantMessageSeq(turnID int64) (int, bool, error) {
	var seq int
	err := s.db.QueryRow(`SELECT seq FROM llm_messages WHERE turn_id = $1 AND role = $2 ORDER BY seq LIMIT 1`,
		turnID, string(LLMMessageRoleAssistant)).Scan(&seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("lookup assistant message: %w", err)
	}
	return seq, true, nil
}

// nextMessageSeq is the seq the next llm_messages row of a run takes: one past the
// highest seq already stored (the user input is 0). It is read from the table rather
// than derived from the event stream because aggregated rows may already have been
// written while the run was streaming.
func (s *PostgresStore) nextMessageSeq(turnID int64) (int, error) {
	var seq int
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM llm_messages WHERE turn_id = $1`, turnID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("next llm message seq: %w", err)
	}
	return seq, nil
}

// ListLLMEvents returns a run's stream events in Seq order for replay/analysis.
func (s *PostgresStore) ListLLMEvents(turnID int64) ([]LLMEvent, error) {
	rows, err := s.db.Query(`SELECT seq, offset_token, channel, kind, event_type, role, name, text_delta, payload, elapsed_ms, created_at
FROM llm_events WHERE turn_id = $1 ORDER BY seq`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query llm events: %w", err)
	}
	defer rows.Close()
	var out []LLMEvent
	for rows.Next() {
		var (
			ev        LLMEvent
			channel   string
			kind      string
			payload   string
			createdAt time.Time
		)
		if err := rows.Scan(&ev.Seq, &ev.OffsetToken, &channel, &kind, &ev.EventType, &ev.Role, &ev.Name,
			&ev.TextDelta, &payload, &ev.ElapsedMS, &createdAt); err != nil {
			return nil, fmt.Errorf("scan llm event: %w", err)
		}
		ev.Channel = LLMEventChannel(channel)
		ev.Kind = LLMEventKind(kind)
		ev.CreatedAt = createdAt.UTC()
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

// ListLLMMessages returns a run's messages (user input, the aggregated
// thinking/tool rows, the assistant return) in Seq order. An assistant row's ParentID
// is the id of the user row it answers.
func (s *PostgresStore) ListLLMMessages(turnID int64) ([]LLMMessage, error) {
	rows, err := s.db.Query(`SELECT id, turn_id, task_id, agent_id, cycle, seq, role, parent_id,
content, normalized_content, llm_provider, model, run_id, status, created_at
FROM llm_messages WHERE turn_id = $1 ORDER BY seq, id`, turnID)
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
			createdAt time.Time
		)
		if err := rows.Scan(&m.ID, &m.TurnID, &m.TaskID, &m.AgentID, &m.Cycle, &m.Seq, &role,
			&parentID, &m.Content, &m.NormalizedContent, &provider, &m.Model, &m.RunID, &m.Status,
			&createdAt); err != nil {
			return nil, fmt.Errorf("scan llm message: %w", err)
		}
		m.Role = LLMMessageRole(role)
		m.LLMProvider = LLMProvider(provider)
		if parentID.Valid {
			m.ParentID = parentID.Int64
		}
		m.CreatedAt = createdAt.UTC()
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm messages: %w", err)
	}
	return out, nil
}
