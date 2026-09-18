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
  error TEXT NOT NULL DEFAULT '',
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
  cycle INTEGER NOT NULL DEFAULT 0,
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
  kind TEXT NOT NULL DEFAULT '',
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
  cycle INTEGER NOT NULL DEFAULT 0,
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

CREATE INDEX IF NOT EXISTS idx_agents_task ON agents(current_task_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_events_turn_seq ON llm_events(turn_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_events_run ON llm_events(run_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_events_kind ON llm_events(turn_id, channel, event_type);
CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_messages_turn_seq ON llm_messages(turn_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_messages_parent ON llm_messages(parent_id);

-- execution_plan is one decision's plan: the steps are planned here before they
-- run, and nothing about it is ever rewritten. A re-plan is a new row (the plan is
-- one-shot), which is why the identity is this row's id and not the step names: a
-- reply_message_id can produce one plan, and cycle is only an ordering label.
--
-- The plan's outcome is *derived*: the status of its last execution_step (nothing
-- here maintains one), and a planned step with no execution row simply never ran.
CREATE TABLE IF NOT EXISTS execution_plan (
  id                     INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id                TEXT NOT NULL DEFAULT '',
  agent_id               INTEGER NOT NULL DEFAULT 0,
  cycle                  INTEGER NOT NULL DEFAULT 0, -- this agent's own round (ordering only)
  decision_type          TEXT NOT NULL DEFAULT '',   -- plan | done | blocked | need_input
  reason                 TEXT NOT NULL DEFAULT '',
  evidence               TEXT NOT NULL DEFAULT '[]',
  need                   TEXT NOT NULL DEFAULT '',   -- what blocked / need_input asked for
  step_count             INTEGER NOT NULL DEFAULT 0,
  plan_hash              TEXT NOT NULL DEFAULT '',   -- the planned steps' fingerprint
  reply_message_id       INTEGER,                    -- llm_messages: the planner reply this plan is
  input_message_id       INTEGER,                    -- llm_messages: the input that reply answered
  task_input_message_id  INTEGER,                    -- llm_messages: the task's own first user input
  reason_turn_id         INTEGER,                    -- reason_turns: the run that produced the reply
  created_at             TEXT NOT NULL,
  UNIQUE(reply_message_id)
);
CREATE INDEX IF NOT EXISTS idx_execution_plan_task ON execution_plan(task_id, id);

-- execution_step_plan is the plan's steps, written in full before the first one
-- runs and never updated afterwards: whether a step ran is a question for
-- execution_step (a left join), not a column here.
CREATE TABLE IF NOT EXISTS execution_step_plan (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  plan_id         INTEGER NOT NULL, -- execution_plan.id
  idx             INTEGER NOT NULL, -- position in the plan (1-based)
  name            TEXT NOT NULL DEFAULT '', -- what the plan calls this step; a binding addresses it as step:<name>.output.<key>
  capability      TEXT NOT NULL DEFAULT '',
  input           TEXT NOT NULL DEFAULT '{}', -- the input the plan asked for, verbatim
  expected_effect TEXT NOT NULL DEFAULT '',
  evidence_refs   TEXT NOT NULL DEFAULT '[]',
  created_at      TEXT NOT NULL,
  UNIQUE(plan_id, idx)
);
CREATE INDEX IF NOT EXISTS idx_execution_step_plan_plan ON execution_step_plan(plan_id);

-- execution_step is what actually happened: one row per executed step, linked to
-- the plan step it carries out. The first failing step ends the cycle, so the
-- steps after it have no row at all.
CREATE TABLE IF NOT EXISTS execution_step (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  plan_id      INTEGER NOT NULL, -- execution_plan.id
  plan_step_id INTEGER NOT NULL, -- execution_step_plan.id
  task_id      TEXT NOT NULL DEFAULT '',
  agent_id     INTEGER NOT NULL DEFAULT 0, -- the agent that ran the step (its planner)
  cycle        INTEGER NOT NULL DEFAULT 0,
  idx          INTEGER NOT NULL DEFAULT 0, -- execution order within the plan
  name         TEXT NOT NULL DEFAULT '', -- what the plan called this step (execution_step_plan.name)
  capability   TEXT NOT NULL DEFAULT '',
  provider     TEXT NOT NULL DEFAULT '', -- github / agent-control-plane / cursor / cline / autonomy
  status       TEXT NOT NULL DEFAULT '', -- ok | failed
  input        TEXT NOT NULL DEFAULT '{}', -- what the capability was actually called with
  output       TEXT NOT NULL DEFAULT '{}',
  error        TEXT NOT NULL DEFAULT '',
  started_at   TEXT NOT NULL,
  ended_at     TEXT,
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_execution_step_plan ON execution_step(plan_step_id);
CREATE INDEX IF NOT EXISTS idx_execution_step_plan_id ON execution_step(plan_id, idx);
CREATE INDEX IF NOT EXISTS idx_execution_step_task ON execution_step(task_id, cycle);

-- execution_step_interaction is what one step talked to while it ran: a step may
-- have several interactions, and each is with one provider. An agent-backed
-- capability points at its LLM run here (reason_turn_id); a capability that talks
-- to an API over HTTP records the interaction kind and provider, with the run
-- detail left to the non-LLM event stream (see docs/execution-step.md).
CREATE TABLE IF NOT EXISTS execution_step_interaction (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  step_id        INTEGER NOT NULL, -- execution_step.id
  seq            INTEGER NOT NULL, -- which interaction of that step (1-based)
  kind           TEXT NOT NULL DEFAULT '', -- llm | http | local
  provider       TEXT NOT NULL DEFAULT '',
  reason_turn_id INTEGER, -- reason_turns.id for an LLM interaction
  created_at     TEXT NOT NULL,
  UNIQUE(step_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_execution_step_interaction_step ON execution_step_interaction(step_id);
CREATE INDEX IF NOT EXISTS idx_execution_step_interaction_turn ON execution_step_interaction(reason_turn_id);

-- completion_contract is the contract a task is judged by: the facts that must hold
-- for it to be done (docs/verification.md). It is written once, by the run's first
-- answer, and never rewritten -- a (task_id, idx) that is already there keeps its
-- criterion, which is what makes the standard a done cannot weaken on the way out.
CREATE TABLE IF NOT EXISTS completion_contract (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id    TEXT NOT NULL DEFAULT '',
  idx        INTEGER NOT NULL DEFAULT 0, -- position in the contract (1-based)
  plan_id    INTEGER NOT NULL DEFAULT 0, -- the plan it arrived with (execution_plan.id)
  name       TEXT NOT NULL DEFAULT '',   -- what the criterion is called
  criterion  TEXT NOT NULL DEFAULT '{}', -- the criterion's JSON, verbatim
  created_at TEXT NOT NULL,
  UNIQUE(task_id, idx)
);
CREATE INDEX IF NOT EXISTS idx_completion_contract_task ON completion_contract(task_id, idx);

-- verification is one verdict on one criterion: what the contract said must hold, what
-- the authoritative source answered, and where that answer came from. Appended, never
-- updated (docs/verification.md).
CREATE TABLE IF NOT EXISTS verification (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id      TEXT NOT NULL DEFAULT '',
  plan_id      INTEGER NOT NULL DEFAULT 0,
  cycle        INTEGER NOT NULL DEFAULT 0,
  criterion    TEXT NOT NULL DEFAULT '',
  requirement  TEXT NOT NULL DEFAULT '',
  method       TEXT NOT NULL DEFAULT '', -- world_model | registry:<cap> | declared:<cap> | -
  evidence     TEXT NOT NULL DEFAULT '{}', -- {"slot": "…", "reference": "…"}
  expected     TEXT NOT NULL DEFAULT '',
  observed     TEXT NOT NULL DEFAULT '',
  result       TEXT NOT NULL DEFAULT '', -- pass | fail | inconclusive
  reason       TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_verification_task ON verification(task_id, id);
CREATE INDEX IF NOT EXISTS idx_verification_plan ON verification(plan_id);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Indexes on the columns that older databases still call step: they are built
	// after the rename below, or a legacy table would fail here ("no such column:
	// cycle") before it ever got the chance to be migrated.
	const ddlIndexedAfterRename = `
CREATE INDEX IF NOT EXISTS idx_reason_turns_agent ON reason_turns(agent_id, cycle);
CREATE INDEX IF NOT EXISTS idx_reason_turns_task ON reason_turns(task_id, cycle);
CREATE INDEX IF NOT EXISTS idx_llm_messages_task ON llm_messages(task_id, cycle);
`
	if err := s.ensureAgentsIDSequence(); err != nil {
		return fmt.Errorf("migrate agents.id sequence: %w", err)
	}
	// tasks.error records why a task ended in error. Databases that predate the
	// column (every task row until now) get it empty, which is what they know.
	if err := s.ensureColumn("tasks", "error", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate tasks.error: %w", err)
	}
	// reason_turns.step / llm_messages.step counted decision cycles and are called
	// cycle now: a step is one execution step of a plan (see docs/execution-step.md),
	// and one word should not mean two things. Older databases are renamed in place
	// — A SQLite RENAME COLUMN carries the data and rewrites the indexes that
	// reference it, so nothing has to be copied.
	for _, table := range []string{"reason_turns", "llm_messages"} {
		if err := s.renameColumnIfPresent(table, "step", "cycle"); err != nil {
			return fmt.Errorf("migrate %s.step→cycle: %w", table, err)
		}
	}
	// Now that every database — migrated or fresh — has cycle, index it.
	if _, err := s.db.Exec(ddlIndexedAfterRename); err != nil {
		return fmt.Errorf("migrate cycle indexes: %w", err)
	}

	// reason_turns additions for databases that predate these columns. Fresh
	// databases already create them above; this keeps older DBs usable.
	// kind is the provider-neutral fine-grained classification (assistant_delta,
	// tool_call_completed, ...). Older databases predate the column.
	if err := s.ensureColumn("llm_events", "kind", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate llm_events.kind: %w", err)
	}
	// Index it only after the column is guaranteed to exist, so legacy
	// databases without kind migrate cleanly.
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_llm_events_turn_kind ON llm_events(turn_id, kind)`); err != nil {
		return fmt.Errorf("migrate llm_events kind index: %w", err)
	}
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
	// HTTP poll / agent-status indexes: only after status (and llm_messages) exist.
	if _, err := s.db.Exec(`
CREATE INDEX IF NOT EXISTS idx_llm_messages_agent_id ON llm_messages(task_id, agent_id, id);
CREATE INDEX IF NOT EXISTS idx_reason_turns_active ON reason_turns(task_id, agent_id, status);
`); err != nil {
		return fmt.Errorf("migrate http query indexes: %w", err)
	}
	// The execution tables gained the step's name: a plan's input bindings address a
	// step by name, so without it a stored plan's lineage points at nothing
	// (docs/execution-step.md, "Plan Data Lineage").
	for _, table := range []string{"execution_step_plan", "execution_step"} {
		if err := s.ensureColumn(table, "name", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("migrate %s.name: %w", table, err)
		}
	}
	if err := s.backfillReasonTurnNormalizedOutputs(); err != nil {
		return fmt.Errorf("normalize reason_turns.normalized_output: %w", err)
	}
	if err := s.backfillLLMMessages(); err != nil {
		return fmt.Errorf("backfill llm_messages: %w", err)
	}
	// Last, so it sees every column and every message row: cycle is relative to the
	// agent it belongs to, and the turns written before that was true are numbered
	// here (see backfillAgentCycles).
	if err := s.backfillAgentCycles(); err != nil {
		return fmt.Errorf("backfill agent cycles: %w", err)
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

// backfillAgentCycles numbers the turns that were recorded before cycle meant
// "this agent's own round, from 1".
//
// Rows written then say 0 (a worker was recorded as having no cycle) or the
// delegating agent's cycle (the era before that). Both are relative to somebody
// else, so each agent's turns are renumbered by their own order — oldest first —
// which is the closest reconstruction of what the new writer would have recorded:
// this agent's first prompt is cycle 1, its second is 2. Planner turns already
// carry their own cycle and are left as they are, and a turn whose agent is
// unknown (agent_id 0) stays untouched: nothing can say what its first round was.
//
// Messages follow their run header, so a message never disagrees with the turn it
// belongs to. Both steps are guarded by "is anything actually different", so a
// database that has already been numbered is a read-only check on every open.
func (s *SQLiteStore) backfillAgentCycles() error {
	// The round a turn is, counted within its own agent, oldest first.
	const ownRound = `(SELECT COUNT(*) FROM reason_turns e
	                   WHERE e.agent_id = reason_turns.agent_id AND e.mode = 'agent'
	                     AND (e.created_at < reason_turns.created_at
	                          OR (e.created_at = reason_turns.created_at AND e.id <= reason_turns.id)))`

	misnumbered, err := s.exists(`
SELECT EXISTS (SELECT 1 FROM reason_turns
                WHERE mode = 'agent' AND agent_id <> 0 AND cycle <> ` + ownRound + `)`)
	if err != nil {
		return err
	}
	if misnumbered {
		if _, err := s.db.Exec(`
UPDATE reason_turns SET cycle = ` + ownRound + `
 WHERE mode = 'agent' AND agent_id <> 0`); err != nil {
			return fmt.Errorf("number agent cycles: %w", err)
		}
	}

	unmirrored, err := s.exists(`
SELECT EXISTS (SELECT 1 FROM llm_messages m JOIN reason_turns r ON r.id = m.turn_id
                WHERE m.cycle <> r.cycle)`)
	if err != nil {
		return err
	}
	if unmirrored {
		if _, err := s.db.Exec(`
UPDATE llm_messages SET cycle = (SELECT r.cycle FROM reason_turns r WHERE r.id = llm_messages.turn_id)
 WHERE turn_id IN (SELECT id FROM reason_turns)`); err != nil {
			return fmt.Errorf("mirror cycles onto messages: %w", err)
		}
	}
	return nil
}

// exists reports whether a `SELECT EXISTS (...)` query finds anything.
func (s *SQLiteStore) exists(query string) (bool, error) {
	var found bool
	if err := s.db.QueryRow(query).Scan(&found); err != nil {
		return false, err
	}
	return found, nil
}

// renameColumnIfPresent renames oldCol to newCol when the table still has the old
// name and not the new one. SQLite rewrites the column's references (indexes,
// triggers, views) as part of the rename, so the data and the indexes survive
// without a copy. A fresh database already has newCol and skips this.
func (s *SQLiteStore) renameColumnIfPresent(table, oldCol, newCol string) error {
	hasOld, err := s.columnExists(table, oldCol)
	if err != nil {
		return err
	}
	if !hasOld {
		return nil
	}
	hasNew, err := s.columnExists(table, newCol)
	if err != nil {
		return err
	}
	if hasNew {
		return nil
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", table, oldCol, newCol))
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
INSERT INTO tasks (id, description, domain, status, error, agent_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  description=excluded.description,
  domain=excluded.domain,
  status=excluded.status,
  error=excluded.error,
  agent_id=excluded.agent_id,
  updated_at=excluded.updated_at
`, task.ID, task.Description, string(task.Domain),
		task.Status, task.Error, task.AgentID, formatTime(task.CreatedAt), formatTime(task.UpdatedAt))
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
const reasonTurnColumns = "task_id, agent_id, cycle, mode, llm_provider, model, llm_agent_id, " +
	"input, raw_output, normalized_output, run_id, status, error_code, error_message, " +
	"duration_ms, event_count, input_tokens, output_tokens, cache_read_tokens, " +
	"cache_write_tokens, reasoning_tokens, total_tokens, cost_cents, started_at, ended_at, created_at"

func reasonTurnInsertArgs(turn ReasonTurn) []any {
	return []any{
		turn.TaskID, turn.AgentID, turn.Cycle, string(turn.Mode), string(turn.LLMProvider), turn.Model,
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
(turn_id, task_id, agent_id, cycle, seq, role, parent_id, content, normalized_content, llm_provider, model, run_id, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.TurnID, msg.TaskID, msg.AgentID, msg.Cycle, msg.Seq, string(msg.Role), parent,
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

// inputMessage builds a run's input llm_messages row. Its role names who authored
// the prompt: the user (the default) for the runtime's own prompts, agent when
// another agent delegated this run to the agent that is running it.
func inputMessage(turnID int64, turn ReasonTurn) LLMMessage {
	role := turn.InputRole
	if role == "" {
		role = LLMMessageRoleUser
	}
	return LLMMessage{
		TurnID:      turnID,
		TaskID:      turn.TaskID,
		AgentID:     turn.AgentID,
		Cycle:       turn.Cycle,
		Seq:         llmMessageSeqUser,
		Role:        role,
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
	inputID, err := insertMessageTx(tx, inputMessage(turnID, turn))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	assistant := LLMMessage{
		TurnID:            turnID,
		TaskID:            turn.TaskID,
		AgentID:           turn.AgentID,
		Cycle:             turn.Cycle,
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
	inputID, err := insertMessageTx(tx, inputMessage(turnID, turn))
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
(turn_id, run_id, seq, offset_token, channel, kind, event_type, role, name, text_delta, payload, elapsed_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare llm event insert: %w", err)
	}
	defer stmt.Close()
	for _, ev := range events {
		if _, err := stmt.Exec(
			turnID, runID, ev.Seq, ev.OffsetToken, string(ev.Channel), string(ev.Kind), ev.EventType,
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
	// The stream may already have been aggregated while it ran (LLMTrace writes
	// each message as it completes). Re-deriving here is the safety net for
	// callers that only appended events; the upsert makes the second pass a
	// no-op for rows that are already correct.
	events, err := s.ListLLMEvents(h.TurnID)
	if err != nil {
		return err
	}
	if err := s.recordAggregatedMessages(h.TurnID, h.InputMessageID, events); err != nil {
		return err
	}
	// The assistant row is written once, at the seq one past everything stored so
	// far (aggregated rows may have been written while the run streamed). A
	// replayed finish finds it already there and leaves it alone.
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
(turn_id, task_id, agent_id, cycle, seq, role, parent_id, content, normalized_content, llm_provider, model, run_id, status, created_at)
SELECT id, task_id, agent_id, cycle, ?, ?, ?, ?, ?, llm_provider, model, ?, ?, ?
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
// stream and writes them (seq 1..N, parent_id = the user input). The seq the
// assistant row takes is read back from the table (nextMessageSeq) rather than
// assumed here, because the same rows may already have been written while the run
// was streaming. Re-writing a row is an upsert, so a replayed finish is a no-op.
func (s *SQLiteStore) recordAggregatedMessages(turnID, parentID int64, events []LLMEvent) error {
	for _, m := range aggregateChatMessages(events) {
		m.TurnID = turnID
		m.ParentID = parentID
		if err := s.insertDerivedMessage(m); err != nil {
			return err
		}
	}
	return nil
}

// upsertDerivedMessageSQL writes one aggregated (thinking/tool) message row,
// copying task/agent/step/provider/model/run_id/status from the run header. It is
// keyed by (turn_id, seq): a re-write updates the row in place, so a message that
// is persisted while the run streams and then grows (or is re-derived at finish)
// stays one row with its original id.
const upsertDerivedMessageSQL = `
INSERT INTO llm_messages
(turn_id, task_id, agent_id, cycle, seq, role, parent_id, content, normalized_content, llm_provider, model, run_id, status, created_at)
SELECT id, task_id, agent_id, cycle, ?, ?, ?, ?, ?, llm_provider, model, run_id, status, ?
FROM reason_turns WHERE id = ?
ON CONFLICT(turn_id, seq) DO UPDATE SET
  role = excluded.role,
  parent_id = excluded.parent_id,
  content = excluded.content,
  normalized_content = excluded.normalized_content,
  created_at = excluded.created_at
`

// AppendLLMMessages upserts aggregated messages for a run that is still
// streaming (see the Store contract). Idempotent on (turn_id, seq).
func (s *SQLiteStore) AppendLLMMessages(turnID int64, messages []LLMMessage) error {
	if turnID == 0 || len(messages) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin llm messages tx: %w", err)
	}
	stmt, err := tx.Prepare(upsertDerivedMessageSQL)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare llm message insert: %w", err)
	}
	defer stmt.Close()
	for _, m := range messages {
		var parent any
		if m.ParentID != 0 {
			parent = m.ParentID
		}
		if _, err := stmt.Exec(m.Seq, string(m.Role), parent, m.Content, m.NormalizedContent,
			formatTime(m.CreatedAt), turnID); err != nil {
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
// upsertDerivedMessageSQL).
func (s *SQLiteStore) insertDerivedMessage(m LLMMessage) error {
	var parent any
	if m.ParentID != 0 {
		parent = m.ParentID
	}
	if _, err := s.db.Exec(upsertDerivedMessageSQL,
		m.Seq, string(m.Role), parent, m.Content, m.NormalizedContent, formatTime(m.CreatedAt), m.TurnID); err != nil {
		return fmt.Errorf("insert %s message: %w", m.Role, err)
	}
	return nil
}

// assistantMessageSeq reports whether the run already has its assistant row, and
// at which seq. A replayed finish must not append a second one.
func (s *SQLiteStore) assistantMessageSeq(turnID int64) (int, bool, error) {
	var seq int
	err := s.db.QueryRow(`SELECT seq FROM llm_messages WHERE turn_id = ? AND role = ? ORDER BY seq LIMIT 1`,
		turnID, string(LLMMessageRoleAssistant)).Scan(&seq)
	switch {
	case err == sql.ErrNoRows:
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("lookup assistant message: %w", err)
	}
	return seq, true, nil
}

// nextMessageSeq is the seq the next llm_messages row of a run takes: one past
// the highest seq already stored (the user input is 0). It is read from the table
// rather than derived from the event stream because aggregated rows may already
// have been written while the run was streaming.
func (s *SQLiteStore) nextMessageSeq(turnID int64) (int, error) {
	var seq int
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM llm_messages WHERE turn_id = ?`, turnID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("next llm message seq: %w", err)
	}
	return seq, nil
}

// ListLLMEvents returns a run's stream events in Seq order for replay/analysis.
func (s *SQLiteStore) ListLLMEvents(turnID int64) ([]LLMEvent, error) {
	rows, err := s.db.Query(`SELECT seq, offset_token, channel, kind, event_type, role, name, text_delta, payload, elapsed_ms, created_at
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
			kind      string
			payload   string
			createdAt string
		)
		if err := rows.Scan(&ev.Seq, &ev.OffsetToken, &channel, &kind, &ev.EventType, &ev.Role, &ev.Name,
			&ev.TextDelta, &payload, &ev.ElapsedMS, &createdAt); err != nil {
			return nil, fmt.Errorf("scan llm event: %w", err)
		}
		ev.Channel = LLMEventChannel(channel)
		ev.Kind = LLMEventKind(kind)
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
	rows, err := s.db.Query(`SELECT id, turn_id, task_id, agent_id, cycle, seq, role, parent_id,
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
	rows, err := s.db.Query(`SELECT t.id, t.task_id, CAST(t.agent_id AS INTEGER), t.cycle, t.llm_provider, t.model,
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
			cycle                  int
			taskID, provider       string
			model                  string
			input, raw, normalized string
			runID, status          string
			createdAt              string
		)
		if err := rows.Scan(&id, &taskID, &agentID, &cycle, &provider, &model, &input, &raw,
			&normalized, &runID, &status, &createdAt); err != nil {
			rows.Close()
			return err
		}
		if normalized == "" {
			normalized = normalizeReasonOutput(raw)
		}
		pending = append(pending, pendingTurn{id: id, turn: ReasonTurn{
			TaskID: taskID, AgentID: agentID, Cycle: cycle, LLMProvider: LLMProvider(provider), Model: model,
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
		derived[i].Cycle = turn.Cycle
		derived[i].LLMProvider = turn.LLMProvider
		derived[i].Model = turn.Model
		derived[i].RunID = turn.RunID
		derived[i].Status = turn.Status
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	inputID, err := insertMessageTx(tx, inputMessage(turnID, turn))
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
		Cycle:             turn.Cycle,
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
