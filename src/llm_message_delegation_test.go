package autonomy

import (
	"path/filepath"
	"testing"
)

// TestDelegatedRunInputIsAttributedToTheAgent pins who authored a run's input
// row. The runtime's own prompts are the user's task (role=user); a run a
// capability delegated to this agent was prompted by another agent, so its input
// row is role=agent — the user only authors the top-level task.
func TestDelegatedRunInputIsAttributedToTheAgent(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	// The runtime's own run for the task the user submitted.
	own := BeginLLMTrace(&Agent{ID: 5001, Name: "agent-5001"}, "task-1", 0, ReasonModePlan, "runtime prompt")
	own.Finish(LLMRunResult{Status: LLMStatusFinished, RawOutput: "plan"})
	ownMsgs := mustListMessages(t, store, own.handle.TurnID)
	if ownMsgs[0].Role != LLMMessageRoleUser {
		t.Fatalf("runtime run input role=%q, want %q", ownMsgs[0].Role, LLMMessageRoleUser)
	}

	// A delegated run: a capability prompted this worker agent. The worker identity on
	// the agent is what makes it a delegated turn.
	sess := &LLMSession{agent: &Agent{ID: 5002, Name: "agent-5002", Role: AgentRoleWorker}, taskID: "task-1"}
	trace := sess.beginTurn("You are an autonomous software engineer working in this workspace:\n\n/ws\n\nGoal:\n\ndo it", RoundAuto)
	trace.Finish(LLMRunResult{Status: LLMStatusFinished, RawOutput: "done"})

	msgs := mustListMessages(t, store, trace.handle.TurnID)
	if msgs[0].Role != LLMMessageRoleAgent {
		t.Fatalf("delegated run input role=%q, want %q", msgs[0].Role, LLMMessageRoleAgent)
	}
	if msgs[0].Seq != 0 || msgs[0].AgentID != 5002 || msgs[0].TaskID != "task-1" {
		t.Fatalf("delegated input row=%+v, want seq 0 for agent 5002 on task-1", msgs[0])
	}
	last := msgs[len(msgs)-1]
	if last.Role != LLMMessageRoleAssistant || last.Seq != 1 {
		t.Fatalf("delegated run assistant row=%+v, want role=assistant at seq 1", last)
	}
	if last.ParentID != msgs[0].ID {
		t.Fatalf("assistant parent_id=%d, want the input row id %d", last.ParentID, msgs[0].ID)
	}

	// The log line names the same author.
	if got, want := formatLLMMessage(msgs[0]), "[autonomy] llm seq=0 agent: You are an autonomous software engineer working in this workspace: /ws Goal: do it"; got != want {
		t.Fatalf("log line=%q, want %q", got, want)
	}
}
