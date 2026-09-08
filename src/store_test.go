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
		ID: "t1", Description: "d", Domain: TaskDomainServer, Target: "1",
		Goal: "g", Status: "running", Contract: Contract{ExpectedState: "changed"},
		CreatedAt: time.Now(),
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{
		ID: "a1", State: "running", Lifecycle: AgentLifecycleEphemeral,
		CurrentTask: task, LLMAgentID: "cursor-1", LLMProvider: LLMProviderCursor,
	}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	var llmProvider string
	err = store.db.QueryRow(`SELECT llm_provider FROM agents WHERE id = ?`, "a1").Scan(&llmProvider)
	if err != nil {
		t.Fatal(err)
	}
	if llmProvider != string(LLMProviderCursor) {
		t.Fatalf("llm_provider=%q, want %q", llmProvider, LLMProviderCursor)
	}
	var llmAgentID string
	err = store.db.QueryRow(`SELECT llm_agent_id FROM agents WHERE id = ?`, "a1").Scan(&llmAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if llmAgentID != "cursor-1" {
		t.Fatalf("llm_agent_id=%q, want %q", llmAgentID, "cursor-1")
	}
	if err := store.InsertReasonTurn(ReasonTurn{
		TaskID: "t1", AgentID: "a1", Step: 1, Input: "in", Output: "out",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SoftDeleteAgent("a1"); err != nil {
		t.Fatal(err)
	}

	var deletedAt sql.NullString
	var state string
	err = store.db.QueryRow(`SELECT deleted_at, state FROM agents WHERE id = ?`, "a1").Scan(&deletedAt, &state)
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
	err = store.db.QueryRow(`SELECT COUNT(*) FROM reason_turns WHERE agent_id = ?`, "a1").Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reason_turns=%d", n)
	}

	// Re-upsert clears soft delete.
	agent.State = "idle"
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	err = store.db.QueryRow(`SELECT deleted_at FROM agents WHERE id = ?`, "a1").Scan(&deletedAt)
	if err != nil {
		t.Fatal(err)
	}
	if deletedAt.Valid {
		t.Fatalf("expected deleted_at cleared, got %q", deletedAt.String)
	}
}

func TestSQLiteStoreMigratesExistingAgentsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Old schema predates llm_provider and llm_agent_id.
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

	var got string
	err = store.db.QueryRow(`SELECT llm_agent_id FROM agents WHERE id = ?`, "old").Scan(&got)
	if err != nil {
		t.Fatal(err)
	}
	if got != "cursor-keep-me" {
		t.Fatalf("llm_agent_id=%q, want %q", got, "cursor-keep-me")
	}
	exists, err := store.columnExists("agents", "cursor_agent_id")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("cursor_agent_id column still exists after migration")
	}

	agent := &Agent{ID: "migrated", LLMAgentID: "cursor-new", LLMProvider: LLMProviderCline}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	var llmAgentID string
	err = store.db.QueryRow(`SELECT llm_provider, llm_agent_id FROM agents WHERE id = ?`, "migrated").Scan(&got, &llmAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(LLMProviderCline) {
		t.Fatalf("llm_provider=%q, want %q", got, LLMProviderCline)
	}
	if llmAgentID != "cursor-new" {
		t.Fatalf("llm_agent_id=%q, want %q", llmAgentID, "cursor-new")
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

	task := &Task{ID: "t-local", Goal: "g", Target: "1"}
	agent := &Agent{ID: "a-local", CurrentTask: task}
	r := NewLocalReasoner("local")
	_, err = r.Reason(DecisionContext{Task: task, Agent: agent, Step: 2}, ReasoningInput{})
	if err != nil {
		t.Fatal(err)
	}
	var step int
	var input, output string
	err = store.db.QueryRow(`SELECT step, input, output FROM reason_turns WHERE agent_id = ?`, "a-local").
		Scan(&step, &input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if step != 2 || input == "" || output == "" {
		t.Fatalf("step=%d input=%q output=%q", step, input, output)
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
	auto.finishAgent(agent)
	if got := auto.AgentFactory.Get(id); got != nil {
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
	src := filepath.Join("agent_policy", "AGENT_V1.md")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENT_V1.md"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
