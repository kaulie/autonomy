package autonomy

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/codexsdk"
	"github.com/kaulie/autonomy/src/codexsdk/fakebridge"
	"github.com/kaulie/autonomy/src/llmbackend"
	"github.com/kaulie/autonomy/src/llmbackend/codex"
)

// This file covers where a *capability-acquired* worker runs (src/runtime.go,
// Runtime.AcquireAgent) and where every agent's workspace comes from (src/agent_backend.go).
//
// The rule they pin: the account a task runs on is the whole answer — harness, credential,
// workspace root — for that task's own agent *and* for the workers it delegates to. A
// capability names a purpose, not a provider, and it must never spend a second account's quota
// on the same task's work (2026-09-24: a task on a codex account had its code written by a
// cline worker, because a worker took the process default backend and that harness's default
// account).

// TestFakeCodexBridgeProcess re-executes this test binary as the fake Codex bridge (the same
// helper the codex package's own tests use, so a worker can be attached without Node).
func TestFakeCodexBridgeProcess(t *testing.T) {
	if !fakebridge.Enabled() {
		t.Skip("helper process only")
	}
	fakebridge.Main()
}

// installFakeCodexClient points the process-wide codex client at that fake bridge.
func installFakeCodexClient(t *testing.T) {
	t.Helper()
	argv, env := fakebridge.Command(os.Args[0])
	manager := codexsdk.NewBridgeManager()
	manager.Command, manager.Env = argv, env
	previous := codex.SwapCodexClient(codexsdk.NewClient(codexsdk.WithManager(manager)))
	t.Cleanup(func() {
		if client := codex.SwapCodexClient(previous); client != nil {
			_ = client.Close()
		}
	})
}

func TestADelegatedWorkerInheritsTheTasksAccount(t *testing.T) {
	installFakeCodexClient(t)
	// The process default is deliberately *not* the task's harness: before this, a worker
	// took AUTONOMY_LLM_BACKEND (cline) no matter what the task ran on.
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	store := resumeTestStore(t)
	created, err := store.CreateAccount(Account{
		Harness: "codex", Vendor: "openai", Label: "codex main",
		Enabled: true, IsDefault: true, WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	account := &created

	// The task's own agent, on the account the task named (what a panel's dropdown does).
	planner := &Agent{ID: 9101, Name: "agent-9101", Lifecycle: AgentLifecyclePersistent}
	planner.adoptAccount(account)

	factory := NewAgentFactory()
	runtime := NewRuntime(factory)
	// The delegation happens inside that task's cycle, which is where the account comes from.
	runtime.cycle = &DecisionContext{Agent: planner}

	sess, err := runtime.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose: "code_edit", TaskID: "task-9101", Backend: "cursor",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	worker := factory.Get(sess.ID())
	if worker == nil {
		t.Fatalf("the acquired worker is not registered: %s", sess.ID())
	}
	if worker.AccountID != account.ID {
		t.Fatalf("workers.account_id=%q want the task's account %s", worker.AccountID, account.ID)
	}
	if worker.Backend != llmbackend.Codex {
		t.Fatalf("worker backend=%q want the account's harness (codex), not the process default", worker.Backend)
	}
	if !strings.HasPrefix(worker.Workspace, account.WorkspaceRoot) {
		t.Fatalf("worker workspace=%q want it under the account's root %q", worker.Workspace, account.WorkspaceRoot)
	}
	if worker.Session == nil || worker.llm == nil || worker.llm.ProviderSessionID() == "" {
		t.Fatal("the worker has no provider session attached")
	}
}

// A worker is not allowed to quietly run elsewhere when the task's own account is gone: the
// delegation fails, in the same words every other path uses.
func TestADelegationOntoAMissingAccountIsRefused(t *testing.T) {
	resumeTestStore(t)
	planner := &Agent{ID: 9102, Name: "agent-9102", Lifecycle: AgentLifecyclePersistent, AccountID: "acct-gone"}
	factory := NewAgentFactory()
	runtime := NewRuntime(factory)
	runtime.cycle = &DecisionContext{Agent: planner}

	_, err := runtime.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose: "code_edit", TaskID: "task-9102", Backend: "cursor",
	})
	if err == nil || !strings.Contains(err.Error(), "acct-gone") {
		t.Fatalf("err=%v want a refusal naming the account the task is on", err)
	}
	if !strings.Contains(err.Error(), "/accounts") {
		t.Fatalf("err=%v want it to point at /accounts", err)
	}
}

// A broker call with nothing being delegated from (no cycle) has no account to inherit: the
// worker keeps the provider it asked for, exactly as every acquisition did before.
func TestAWorkerOutsideACycleInheritsNothing(t *testing.T) {
	runtime := NewRuntime(NewAgentFactory())
	if account, err := runtime.workerAccount(); err != nil || account != nil {
		t.Fatalf("account=%v err=%v want nothing to inherit", account, err)
	}
}

// Where an agent works is its account's root + its own directory — not the workspace a caller
// read before the account was resolved. Reading it first and re-applying it put every agent
// back on the runtime's default root, so the pool's roots were ignored unless the task had
// named the account at accept time.
func TestAnAgentsWorkspaceComesFromItsAccountNotFromAStaleCaller(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	store := resumeTestStore(t)
	created, err := store.CreateAccount(Account{
		Harness: "cline", Vendor: "deepseek", Label: "cline acct", Model: "deepseek-v4-pro",
		Enabled: true, IsDefault: true, WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	account := &created

	// What a freshly registered agent starts with: the runtime's default root.
	stale := AgentWorkspacePath("agent-9103")
	agent := &Agent{ID: 9103, Name: "agent-9103", Lifecycle: AgentLifecycleEphemeral, Backend: llmbackend.Local, Workspace: stale}
	if _, err := agent.ensureLLMSession(context.Background(), "", stale, ReasonModePlan); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	if agent.AccountID != account.ID {
		t.Fatalf("account=%q want %s", agent.AccountID, account.ID)
	}
	if !strings.HasPrefix(agent.Workspace, account.WorkspaceRoot) || agent.Workspace == stale {
		t.Fatalf("workspace=%q want the account's root %q, not the stale %q", agent.Workspace, account.WorkspaceRoot, stale)
	}
}

// A capability that asks for a directory of its own keeps it — the account owns its root, but
// an explicit choice outranks it (and keeps outranking it on every later turn).
func TestAWorkerAcquiredWithItsOwnWorkspaceKeepsIt(t *testing.T) {
	installFakeCodexClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	store := resumeTestStore(t)
	created, err := store.CreateAccount(Account{
		Harness: "codex", Vendor: "openai", Label: "codex main", Enabled: true, IsDefault: true,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	planner := &Agent{ID: 9104, Name: "agent-9104", Lifecycle: AgentLifecyclePersistent}
	planner.adoptAccount(&created)

	factory := NewAgentFactory()
	runtime := NewRuntime(factory)
	runtime.cycle = &DecisionContext{Agent: planner}
	requested := t.TempDir()
	sess, err := runtime.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose: "code_edit", TaskID: "task-9104", Backend: "cursor", Workspace: requested,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	worker := factory.Get(sess.ID())
	if worker == nil {
		t.Fatalf("the acquired worker is not registered: %s", sess.ID())
	}
	if worker.Workspace != requested {
		t.Fatalf("workspace=%q want the requested %q", worker.Workspace, requested)
	}
	// The account is still what it runs on: the directory is the only thing overridden.
	if worker.AccountID != created.ID || worker.Backend != llmbackend.Codex {
		t.Fatalf("account=%q backend=%q want %s / codex", worker.AccountID, worker.Backend, created.ID)
	}
	// A later turn re-resolves the account; it must not move the worker either.
	if _, err := worker.ensureLLMSession(context.Background(), "", worker.Workspace, ReasonModeAgent); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	if worker.Workspace != requested {
		t.Fatalf("after a turn the workspace=%q want the requested %q", worker.Workspace, requested)
	}
}
