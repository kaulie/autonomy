package autonomy

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteStoreTaskAgentReasonTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomy.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	task := &Task{
		ID: "t1", Description: "d", Domain: TaskDomainServer,
		Status:  "running",
		AgentID: 1, CreatedAt: time.Now(),
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	var taskAgentID int64
	err = store.db.QueryRow(`SELECT agent_id FROM tasks WHERE id = ?`, "t1").Scan(&taskAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if taskAgentID != 1 {
		t.Fatalf("task agent_id=%d, want %d", taskAgentID, 1)
	}
	agent := &Agent{
		ID: 1, Name: "agent-1", State: "running", Lifecycle: AgentLifecycleEphemeral,
		CurrentTask: task, LLMAgentID: "cursor-1", LLMProvider: LLMProviderCursor,
		Model: "composer-2",
	}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	var llmProvider string
	err = store.db.QueryRow(`SELECT llm_provider FROM agents WHERE id = ?`, 1).Scan(&llmProvider)
	if err != nil {
		t.Fatal(err)
	}
	if llmProvider != string(LLMProviderCursor) {
		t.Fatalf("llm_provider=%q, want %q", llmProvider, LLMProviderCursor)
	}
	var llmAgentID string
	err = store.db.QueryRow(`SELECT llm_agent_id FROM agents WHERE id = ?`, 1).Scan(&llmAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if llmAgentID != "cursor-1" {
		t.Fatalf("llm_agent_id=%q, want %q", llmAgentID, "cursor-1")
	}
	var model string
	err = store.db.QueryRow(`SELECT model FROM agents WHERE id = ?`, 1).Scan(&model)
	if err != nil {
		t.Fatal(err)
	}
	if model != "composer-2" {
		t.Fatalf("model=%q, want %q", model, "composer-2")
	}
	if err := store.InsertReasonTurn(ReasonTurn{
		TaskID: "t1", AgentID: 1, Step: 1, Mode: ReasonModeAgent,
		LLMProvider: LLMProviderCursor, Model: "composer-2", Input: "in", RawOutput: "out",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SoftDeleteAgent(1); err != nil {
		t.Fatal(err)
	}

	var deletedAt sql.NullString
	var state string
	err = store.db.QueryRow(`SELECT deleted_at, state FROM agents WHERE id = ?`, 1).Scan(&deletedAt, &state)
	if err != nil {
		t.Fatal(err)
	}
	if !deletedAt.Valid || deletedAt.String == "" {
		t.Fatal("expected soft-delete deleted_at")
	}
	if state != "deleted" {
		t.Fatalf("state=%q", state)
	}

	var n int
	err = store.db.QueryRow(`SELECT COUNT(*) FROM reason_turns WHERE agent_id = ?`, 1).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reason_turns=%d", n)
	}
	var mode string
	err = store.db.QueryRow(`SELECT mode FROM reason_turns WHERE agent_id = ?`, 1).Scan(&mode)
	if err != nil {
		t.Fatal(err)
	}
	if mode != string(ReasonModeAgent) {
		t.Fatalf("mode=%q, want %q", mode, ReasonModeAgent)
	}
	var reasonLLMProvider, reasonModel string
	err = store.db.QueryRow(`SELECT llm_provider, model FROM reason_turns WHERE agent_id = ?`, 1).Scan(&reasonLLMProvider, &reasonModel)
	if err != nil {
		t.Fatal(err)
	}
	if reasonLLMProvider != string(LLMProviderCursor) {
		t.Fatalf("reason llm_provider=%q, want %q", reasonLLMProvider, LLMProviderCursor)
	}
	if reasonModel != "composer-2" {
		t.Fatalf("reason model=%q, want %q", reasonModel, "composer-2")
	}

	// Re-upsert clears soft delete.
	agent.State = "idle"
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	err = store.db.QueryRow(`SELECT deleted_at FROM agents WHERE id = ?`, 1).Scan(&deletedAt)
	if err != nil {
		t.Fatal(err)
	}
	if deletedAt.Valid {
		t.Fatalf("expected deleted_at cleared, got %q", deletedAt.String)
	}
}

func TestInsertReasonTurnSplitsRawAndNormalizedOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomy.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	fenced := "```json\n{\"type\":\"plan\",\"reason\":\"go\",\"plan\":[],\"need\":{}}\n```"
	if err := store.InsertReasonTurn(ReasonTurn{TaskID: "t-fence", AgentID: 1, RawOutput: fenced}); err != nil {
		t.Fatal(err)
	}
	var rawOut, normalizedOut string
	err = store.db.QueryRow(`SELECT raw_output, normalized_output FROM reason_turns WHERE agent_id = ?`, 1).
		Scan(&rawOut, &normalizedOut)
	if err != nil {
		t.Fatal(err)
	}
	if rawOut != fenced {
		t.Fatalf("raw_output=%q, want original fenced output %q", rawOut, fenced)
	}
	want := `{"type":"plan","reason":"go","plan":[],"need":{}}`
	if normalizedOut != want {
		t.Fatalf("normalized_output=%q, want %q", normalizedOut, want)
	}
}

func TestSQLiteStoreMigratesSplitsReasonTurnOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE reason_turns (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL DEFAULT '',
		agent_id TEXT NOT NULL DEFAULT '',
		step INTEGER NOT NULL DEFAULT 0,
		input TEXT NOT NULL DEFAULT '',
		output TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	fenced := "```json\n{\"type\":\"plan\"}\n```"
	_, err = db.Exec(`INSERT INTO reason_turns
		(task_id, agent_id, step, input, output, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		"t-old", "a-old", 1, "in", fenced, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var rawOut, normalizedOut string
	err = store.db.QueryRow(`SELECT raw_output, normalized_output FROM reason_turns WHERE agent_id = ?`, "a-old").
		Scan(&rawOut, &normalizedOut)
	if err != nil {
		t.Fatal(err)
	}
	if rawOut != fenced {
		t.Fatalf("raw_output=%q, want legacy output preserved as %q", rawOut, fenced)
	}
	want := `{"type":"plan"}`
	if normalizedOut != want {
		t.Fatalf("normalized_output=%q, want %q", normalizedOut, want)
	}
}

func TestSQLiteStoreRebuildsLegacyAgentsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy schema: TEXT agents.id and old tasks without agent_id.
	_, err = db.Exec(`CREATE TABLE agents (
		id TEXT PRIMARY KEY,
		state TEXT NOT NULL DEFAULT '',
		lifecycle TEXT NOT NULL DEFAULT 'ephemeral',
		current_task_id TEXT NOT NULL DEFAULT '',
		context TEXT NOT NULL DEFAULT '',
		cursor_agent_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		deleted_at TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE tasks (
		id TEXT PRIMARY KEY,
		description TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO agents
		(id, state, lifecycle, current_task_id, context, cursor_agent_id, created_at, updated_at, deleted_at)
		VALUES ('old', 'idle', 'ephemeral', '', '', 'cursor-keep-me', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Legacy data is intentionally discarded; tables are rebuilt with int ids.
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("agents rows=%d, want 0 after rebuild", n)
	}
	legacy, err := store.agentsUseTextID()
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		t.Fatal("agents.id should be INTEGER after rebuild")
	}
	exists, err := store.columnExists("agents", "name")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("agents.name column missing after rebuild")
	}

	// First new agent gets id 10000 and name agent-10000.
	agent := &Agent{State: "idle"}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	if agent.ID != 10000 {
		t.Fatalf("first agent id=%d, want 10000", agent.ID)
	}
	if agent.Name != "agent-10000" {
		t.Fatalf("agent name=%q, want agent-10000", agent.Name)
	}
	var name string
	if err := store.db.QueryRow(`SELECT name FROM agents WHERE id = ?`, agent.ID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "agent-10000" {
		t.Fatalf("stored name=%q, want agent-10000", name)
	}
}

func TestLocalReasonerPersistsTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomy.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	task := &Task{ID: "t-local"}
	agent := &Agent{ID: 42, CurrentTask: task}
	r := NewLocalReasoner("local")
	_, err = r.Reason(DecisionContext{Task: task, Agent: agent, Step: 2}, ReasoningInput{})
	if err != nil {
		t.Fatal(err)
	}
	var step int
	var input, rawOutput, normalizedOutput, mode, llmProvider, model string
	err = store.db.QueryRow(`SELECT step, input, raw_output, normalized_output, mode, llm_provider, model FROM reason_turns WHERE agent_id = ?`, 42).
		Scan(&step, &input, &rawOutput, &normalizedOutput, &mode, &llmProvider, &model)
	if err != nil {
		t.Fatal(err)
	}
	if step != 2 || input == "" || rawOutput == "" || normalizedOutput == "" {
		t.Fatalf("step=%d input=%q raw_output=%q normalized_output=%q", step, input, rawOutput, normalizedOutput)
	}
	if mode != string(ReasonModePlan) {
		t.Fatalf("mode=%q, want %q", mode, ReasonModePlan)
	}
	if llmProvider != "" || model != "" {
		t.Fatalf("llm_provider=%q model=%q, want empty for local reasoner", llmProvider, model)
	}
}

func TestRecordReasonIOCursorBackendUsesPlanMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomy.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	agent := &Agent{ID: 9, Backend: AgentBackendCursor, LLMProvider: LLMProviderCursor, Model: "composer-2"}
	recordReasonIO(DecisionContext{Agent: agent, Task: &Task{ID: "t-cursor"}, Step: 1}, "in", "out")

	var mode string
	err = store.db.QueryRow(`SELECT mode FROM reason_turns WHERE agent_id = ?`, 9).Scan(&mode)
	if err != nil {
		t.Fatal(err)
	}
	if mode != string(ReasonModePlan) {
		t.Fatalf("mode=%q, want %q", mode, ReasonModePlan)
	}
}

func TestRecordAgentPromptPersistsTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomy.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	agent := &Agent{
		ID: 7, LLMProvider: LLMProviderCursor, Model: "composer-2",
	}
	recordAgentPrompt(agent, "task-1", "please edit code", "changed files: a.go")

	var taskID, input, rawOutput, normalizedOutput, mode, llmProvider, model string
	err = store.db.QueryRow(`SELECT task_id, input, raw_output, normalized_output, mode, llm_provider, model FROM reason_turns WHERE agent_id = ?`, 7).
		Scan(&taskID, &input, &rawOutput, &normalizedOutput, &mode, &llmProvider, &model)
	if err != nil {
		t.Fatal(err)
	}
	if taskID != "task-1" {
		t.Fatalf("task_id=%q, want %q", taskID, "task-1")
	}
	if input != "please edit code" || rawOutput != "changed files: a.go" {
		t.Fatalf("input=%q raw_output=%q", input, rawOutput)
	}
	if normalizedOutput != "changed files: a.go" {
		t.Fatalf("normalized_output=%q, want raw text for non-JSON output", normalizedOutput)
	}
	if mode != string(ReasonModeAgent) {
		t.Fatalf("mode=%q, want %q", mode, ReasonModeAgent)
	}
	if llmProvider != string(LLMProviderCursor) || model != "composer-2" {
		t.Fatalf("llm_provider=%q model=%q", llmProvider, model)
	}
}

func TestFinishAgentSoftDeletesInStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomy.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	auto := &Autonomy{AgentFactory: NewAgentFactory(), Store: store}
	task := &Task{ID: "t-soft"}
	agent := auto.AgentFactory.Create(task)
	id := agent.ID
	name := agent.Name
	auto.finishAgent(agent)
	if got := auto.AgentFactory.Get(name); got != nil {
		t.Fatal("still in factory")
	}
	var deletedAt sql.NullString
	err = store.db.QueryRow(`SELECT deleted_at FROM agents WHERE id = ?`, id).Scan(&deletedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !deletedAt.Valid {
		t.Fatal("expected soft delete in sqlite")
	}
}

func preparePolicyRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "src", "agent_policy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join("agent_policy", "AGENT_V2.md")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENT_V2.md"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
