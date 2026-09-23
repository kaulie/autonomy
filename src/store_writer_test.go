package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"path/filepath"
	"testing"
)

// These tests drive the runtime's own record-keeping (the persist*/record* writers
// in src/store.go, which know only the ports) against a real engine and read the
// result back through the ports. They live in the root package because they exercise
// unexported writers; the engine itself now lives in src/db, reached through the
// engine registry (openStore).

func TestLocalReasonerPersistsTurn(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
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
	if _, err := r.Reason(DecisionContext{Task: task, Agent: agent, Cycle: 2}, ReasoningInput{}); err != nil {
		t.Fatal(err)
	}
	turns, _, err := store.ListTurnsByTask("t-local", 0)
	if err != nil || len(turns) != 1 {
		t.Fatalf("ListTurnsByTask = %+v, %v", turns, err)
	}
	turn := turns[0]
	if turn.Cycle != 2 || turn.Input == "" || turn.Output == "" || turn.NormalizedOutput == "" {
		t.Fatalf("cycle=%d input=%q output=%q normalized_output=%q", turn.Cycle, turn.Input, turn.Output, turn.NormalizedOutput)
	}
	if turn.Mode != ReasonModePlan {
		t.Fatalf("mode=%q, want %q", turn.Mode, ReasonModePlan)
	}
	if turn.Provider != "" || turn.Model != "" {
		t.Fatalf("provider=%q model=%q, want empty for local reasoner", turn.Provider, turn.Model)
	}
}

func TestRecordReasonIOCursorBackendUsesPlanMode(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	agent := &Agent{ID: 9, Backend: llmbackend.Cursor, LLMProvider: llmbackend.ProviderCursor, Model: "composer-2"}
	recordReasonIO(DecisionContext{Agent: agent, Task: &Task{ID: "t-cursor"}, Cycle: 1}, "in", "out")

	turns, _, err := store.ListTurnsByTask("t-cursor", 0)
	if err != nil || len(turns) != 1 {
		t.Fatalf("ListTurnsByTask = %+v, %v", turns, err)
	}
	if turns[0].Mode != ReasonModePlan {
		t.Fatalf("mode=%q, want %q", turns[0].Mode, ReasonModePlan)
	}
}

func TestRecordAgentPromptPersistsTurn(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	agent := &Agent{
		ID: 7, LLMProvider: llmbackend.ProviderCursor, Model: "composer-2",
	}
	recordAgentPrompt(agent, "task-1", "please edit code", "changed files: a.go")

	turns, _, err := store.ListTurnsByTask("task-1", 0)
	if err != nil || len(turns) != 1 {
		t.Fatalf("ListTurnsByTask = %+v, %v", turns, err)
	}
	turn := turns[0]
	if turn.TaskID != "task-1" {
		t.Fatalf("task_id=%q, want %q", turn.TaskID, "task-1")
	}
	if turn.Input != "please edit code" || turn.Output != "changed files: a.go" {
		t.Fatalf("input=%q output=%q", turn.Input, turn.Output)
	}
	if turn.NormalizedOutput != "changed files: a.go" {
		t.Fatalf("normalized_output=%q, want raw text for non-JSON output", turn.NormalizedOutput)
	}
	if turn.Mode != ReasonModeAgent {
		t.Fatalf("mode=%q, want %q", turn.Mode, ReasonModeAgent)
	}
	if turn.Provider != llmbackend.ProviderCursor || turn.Model != "composer-2" {
		t.Fatalf("provider=%q model=%q", turn.Provider, turn.Model)
	}
}

// TestFinishAgentSoftDeletesAnEphemeralAgentInStore: deleting is what happens to
// an agent somebody asked to throw away (ephemeral), and the row records it. The
// default keeps the agent instead (see TestFinishAgentKeepsTheDefaultAgentInTheStore).
func TestFinishAgentSoftDeletesAnEphemeralAgentInStore(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
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
	agent.Lifecycle = AgentLifecycleEphemeral
	id := agent.ID
	name := agent.Name
	auto.finishAgent(agent)
	if got := auto.AgentFactory.Get(name); got != nil {
		t.Fatal("still in factory")
	}
	stored, err := store.GetAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.DeletedAt.IsZero() {
		t.Fatal("expected a soft delete recorded on the agent row")
	}
}
