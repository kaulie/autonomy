package autonomy

import (
	"path/filepath"
	"testing"
)

// TestLLMMessagesSeparateRecordsLinked proves the user input and the LLM return
// are stored as two independent llm_messages rows, and the return traces back to
// the exact input it answers via parent_id.
func TestLLMMessagesSeparateRecordsLinked(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	handle, err := store.BeginReasonTurn(ReasonTurn{
		TaskID: "t-msg", AgentID: 11, Step: 2, Mode: ReasonModeAgent,
		LLMProvider: LLMProviderCursor, Model: "composer-2", Input: "what is 2+2?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if handle.TurnID == 0 || handle.InputMessageID == 0 {
		t.Fatalf("handle=%+v, want non-zero turn and input message ids", handle)
	}
	if err := store.FinishReasonTurn(handle, LLMRunResult{
		ProviderRunID: "run-9", Status: LLMStatusFinished, RawOutput: "4",
	}); err != nil {
		t.Fatal(err)
	}

	msgs, err := store.ListLLMMessages(handle.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages=%d, want 2 (input + output)", len(msgs))
	}
	in, out := msgs[0], msgs[1]
	if in.Role != LLMMessageRoleUser || in.Content != "what is 2+2?" || in.Seq != llmMessageSeqUser {
		t.Fatalf("input message=%+v", in)
	}
	if in.AgentID != 11 || in.TaskID != "t-msg" || in.Step != 2 || in.LLMProvider != LLMProviderCursor {
		t.Fatalf("input message metadata=%+v", in)
	}
	if in.ParentID != 0 {
		t.Fatalf("input parent_id=%d, want 0", in.ParentID)
	}
	if out.Role != LLMMessageRoleAssistant || out.Content != "4" || out.Seq != llmMessageSeqAssistant {
		t.Fatalf("output message=%+v", out)
	}
	if out.RunID != "run-9" || out.Status != string(LLMStatusFinished) {
		t.Fatalf("output run/status=%q/%q", out.RunID, out.Status)
	}
	// The return traces back to the specific input record.
	if out.ParentID != in.ID || out.ParentID != handle.InputMessageID {
		t.Fatalf("output parent_id=%d, want input id %d (handle %d)", out.ParentID, in.ID, handle.InputMessageID)
	}

	// The run header keeps the content columns (not removed): the pair is also
	// mirrored there for backward compatibility.
	var headerInput, headerOutput string
	if err := store.db.QueryRow(`SELECT input, raw_output FROM reason_turns WHERE id = ?`, handle.TurnID).
		Scan(&headerInput, &headerOutput); err != nil {
		t.Fatal(err)
	}
	if headerInput != "what is 2+2?" || headerOutput != "4" {
		t.Fatalf("header input=%q raw_output=%q", headerInput, headerOutput)
	}

	// Finishing again must not duplicate the assistant message.
	if err := store.FinishReasonTurn(handle, LLMRunResult{
		ProviderRunID: "run-9", Status: LLMStatusFinished, RawOutput: "4",
	}); err != nil {
		t.Fatal(err)
	}
	again, err := store.ListLLMMessages(handle.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 {
		t.Fatalf("messages after re-finish=%d, want 2", len(again))
	}
}

// TestInsertReasonTurnWritesLinkedMessages covers the one-shot (non-streaming)
// path: the header plus the user input and assistant output are written together
// and linked.
func TestInsertReasonTurnWritesLinkedMessages(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	fenced := "```json\n{\"a\":1}\n```"
	if err := store.InsertReasonTurn(ReasonTurn{
		TaskID: "t-one", AgentID: 12, Step: 1, Mode: ReasonModePlan,
		LLMProvider: LLMProviderCursor, Model: "composer-2",
		Input: "plan it", RawOutput: fenced,
	}); err != nil {
		t.Fatal(err)
	}

	var turnID int64
	if err := store.db.QueryRow(`SELECT id FROM reason_turns WHERE agent_id = ?`, 12).Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.ListLLMMessages(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages=%d, want 2", len(msgs))
	}
	if msgs[0].Role != LLMMessageRoleUser || msgs[0].Content != "plan it" {
		t.Fatalf("input=%+v", msgs[0])
	}
	if msgs[1].Role != LLMMessageRoleAssistant || msgs[1].Content != fenced {
		t.Fatalf("output=%+v", msgs[1])
	}
	if msgs[1].NormalizedContent != `{"a":1}` {
		t.Fatalf("normalized_content=%q", msgs[1].NormalizedContent)
	}
	if msgs[1].ParentID != msgs[0].ID {
		t.Fatalf("output parent_id=%d, want input id %d", msgs[1].ParentID, msgs[0].ID)
	}
}

// TestLLMTraceRecordsLinkedMessages proves the streamed provider path also
// records the input/output pair.
func TestLLMTraceRecordsLinkedMessages(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	agent := &Agent{ID: 55, LLMProvider: LLMProviderCursor, Model: "composer-2"}
	trace := BeginLLMTrace(agent, "task-m", 4, ReasonModePlan, "prompt text")
	trace.Finish(LLMRunResult{ProviderRunID: "run-m", Status: LLMStatusFinished, RawOutput: "answer"})

	var turnID int64
	if err := store.db.QueryRow(`SELECT id FROM reason_turns WHERE agent_id = ?`, 55).Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.ListLLMMessages(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages=%d, want 2", len(msgs))
	}
	if msgs[0].Role != LLMMessageRoleUser || msgs[0].Content != "prompt text" {
		t.Fatalf("input=%+v", msgs[0])
	}
	if msgs[1].Role != LLMMessageRoleAssistant || msgs[1].Content != "answer" {
		t.Fatalf("output=%+v", msgs[1])
	}
	if msgs[1].ParentID != msgs[0].ID {
		t.Fatalf("output parent_id=%d, want input id %d", msgs[1].ParentID, msgs[0].ID)
	}
}

// TestBackfillLLMMessagesFromExistingTurns seeds the message table from
// reason_turns rows written before it existed, and is idempotent across reopens.
func TestBackfillLLMMessagesFromExistingTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomy.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a row written by the pre-message-table code path.
	if _, err := store.db.Exec(`INSERT INTO reason_turns
(task_id, agent_id, step, mode, llm_provider, model, input, raw_output, normalized_output, run_id, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"t-old", 3, 2, string(ReasonModeAgent), string(LLMProviderCursor), "composer-2",
		"legacy in", "legacy out", "", "run-old", string(LLMStatusFinished), "2026-01-01T00:00:00Z"); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening migrates and backfills.
	reopened, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var turnID int64
	if err := reopened.db.QueryRow(`SELECT id FROM reason_turns WHERE task_id = ?`, "t-old").Scan(&turnID); err != nil {
		reopened.Close()
		t.Fatal(err)
	}
	msgs, err := reopened.ListLLMMessages(turnID)
	if err != nil {
		reopened.Close()
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("backfilled messages=%d, want 2", len(msgs))
	}
	if msgs[0].Role != LLMMessageRoleUser || msgs[0].Content != "legacy in" {
		t.Fatalf("backfilled input=%+v", msgs[0])
	}
	if msgs[1].Role != LLMMessageRoleAssistant || msgs[1].Content != "legacy out" {
		t.Fatalf("backfilled output=%+v", msgs[1])
	}
	if msgs[1].ParentID != msgs[0].ID {
		t.Fatalf("backfilled output parent_id=%d, want input id %d", msgs[1].ParentID, msgs[0].ID)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	// A second reopen must not duplicate the pair.
	again, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	msgs2, err := again.ListLLMMessages(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs2) != 2 {
		t.Fatalf("messages after second reopen=%d, want 2", len(msgs2))
	}
}
