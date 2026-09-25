package autonomy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/llmbackend"
)

func TestClaudeAccountRunsPlannerAndDelegatedWorker(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"claude-session\",\"result\":\"OK\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTONOMY_CLAUDE_BIN", bin)
	t.Setenv("AUTONOMY_LLM_BACKEND", "claude_code")
	store := resumeTestStore(t)
	account, err := store.CreateAccount(Account{Harness: "claude_code", Label: "Claude test", Enabled: true, IsDefault: true, WorkspaceRoot: dir, Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	if account.Harness != "claude" || account.Vendor != "anthropic" {
		t.Fatalf("%+v", account)
	}
	planner := &Agent{ID: 9901, Name: "agent-9901", Lifecycle: AgentLifecyclePersistent}
	if _, err := planner.ensureLLMSession(context.Background(), "", "", ReasonMode(llmbackend.ModePlan)); err != nil {
		t.Fatal(err)
	}
	if planner.AccountID != account.ID || planner.Backend != llmbackend.Claude {
		t.Fatalf("account=%s backend=%s", planner.AccountID, planner.Backend)
	}
	if out, err := planner.PromptLLMText(context.Background(), "plan", ReasonMode(llmbackend.ModePlan)); err != nil || out != "OK" {
		t.Fatalf("%q %v", out, err)
	}
	if planner.LLMAgentID != "claude-session" {
		t.Fatal(planner.LLMAgentID)
	}
	factory := NewAgentFactory()
	runtime := NewRuntime(factory)
	runtime.cycle = &DecisionContext{Agent: planner}
	workerSession, err := runtime.AcquireAgent(context.Background(), broker.AcquireAgentOpts{Purpose: "code_edit", TaskID: "claude-task", Backend: "cursor"})
	if err != nil {
		t.Fatal(err)
	}
	worker := factory.Get(workerSession.ID())
	if worker == nil || worker.AccountID != account.ID || worker.Backend != llmbackend.Claude {
		t.Fatal("worker did not inherit Claude account")
	}
	defer worker.llmSession().Dispose(context.Background())
	if out, err := worker.PromptLLMText(context.Background(), "work", ReasonMode(llmbackend.ModeAgent)); err != nil || out != "OK" {
		t.Fatalf("%q %v", out, err)
	}
}
