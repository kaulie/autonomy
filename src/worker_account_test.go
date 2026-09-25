package autonomy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
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

// A broker call with nothing being delegated from (no delegating agent) has no account to
// inherit: the plan falls back to the pool's own order for the provider the capability asked
// for, exactly as every acquisition did before.
func TestAWorkerOutsideACycleInheritsNothing(t *testing.T) {
	store := resumeTestStore(t)
	cline, err := store.CreateAccount(Account{
		Harness: "cline", Vendor: "deepseek", Label: "cline default", Model: "deepseek-v4-pro",
		Enabled: true, IsDefault: true, WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	worker := &Agent{ID: 9110, Name: "agent-9110", Role: AgentRoleWorker, Lifecycle: AgentLifecyclePersistent}
	plan, err := assignAgentRuntime(worker, agentRuntimePolicy{RequestedBackend: "cursor"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Account == nil || plan.Account.ID != cline.ID {
		t.Fatalf("account=%v want the pool's own default for the requested harness (%s)", plan.Account, cline.ID)
	}
	if plan.Backend != llmbackend.Cline {
		t.Fatalf("backend=%q want cline (the pool's default for the requested harness)", plan.Backend)
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

// End to end through the loop's own door: the plan step that delegates runs its worker on the
// account the task is on. The process default here is deliberately cline while the task's
// account is codex — without the inheritance the step would run a cline worker.
func TestADelegatedStepRunsItsWorkerOnTheTasksAccount(t *testing.T) {
	installFakeCodexClient(t)
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_CLINE_PROVIDER", "deepseek")
	t.Setenv("AUTONOMY_CLINE_MODEL", "deepseek-v4-pro")
	t.Setenv("PROJECT_ROOT", filepath.Join(".."))
	store := executionTestStore(t)
	created, err := store.CreateAccount(Account{
		Harness: "codex", Vendor: "openai", Label: "codex main", Enabled: true, IsDefault: true,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	agents := NewAgentFactory()
	runtime := NewRuntime(agents)
	factory := capability.NewFactory()
	capability.RegisterDefaults(factory, capability.Deps{Agents: runtime})
	runtime.SetCapabilities(factory.GetAll()...)
	_autonomy = &Autonomy{CapabilityFactory: factory, Store: store}

	ctx := executionContext()
	ctx.Agent.adoptAccount(&created) // the task's own agent, on the task's account

	if _, err := runtime.Execute(Decision{
		Type:   "plan",
		Reason: "hand the work to a coding agent",
		Actions: []Action{CapabilityAction{
			Name:           "code_edit",
			Inputs:         literalInputs(map[string]string{"instruction": "do the thing"}),
			ExpectedEffect: "the change is in the workspace and its pull request is open",
		}},
		Ctx: ctx,
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	plans, err := store.ListExecutionPlans("task-exec")
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%v err=%v", plans, err)
	}
	steps, err := store.ListExecutionSteps(plans[0].ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps=%v err=%v", steps, err)
	}
	interactions, err := store.ListExecutionStepInteractions(steps[0].ID)
	if err != nil || len(interactions) != 1 {
		t.Fatalf("interactions=%v err=%v", interactions, err)
	}
	// The run that answered the step is the codex one (the task's account), not a cline one.
	if got := interactions[0].Provider; got != string(llmbackend.ProviderCodex) {
		t.Fatalf("delegated run provider=%q want codex (the task's account), not the process default", got)
	}

	var agentID int64
	if err := store.RawDB().QueryRow(`SELECT agent_id FROM reason_turns WHERE id = ?`, interactions[0].ReasonTurnID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	worker := agents.Get(fmt.Sprintf("agent-%d", agentID))
	if worker == nil {
		t.Fatalf("the worker agent-%d is not registered", agentID)
	}
	if worker.AccountID != created.ID {
		t.Fatalf("worker account=%q want the task's %s", worker.AccountID, created.ID)
	}
	if worker.Backend != llmbackend.Codex {
		t.Fatalf("worker backend=%q want codex", worker.Backend)
	}
	if !strings.HasPrefix(worker.Workspace, created.WorkspaceRoot) {
		t.Fatalf("worker workspace=%q want it under the account's root %q", worker.Workspace, created.WorkspaceRoot)
	}
}

// The choice is a variable, not a hardcoded rule: the capability may say it for one
// acquisition, the deployment sets the default, and the capability's word wins.
func TestExtendsPlannerAgentIsAChoiceNotAHardcodedRule(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name   string
		env    string
		choice *bool
		want   bool
	}{
		{name: "nothing said, no switch: on (the pool governs the task)", want: true},
		{name: "deployment switches it off", env: "0", want: false},
		{name: "the other spellings of off", env: "off", want: false},
		{name: "false", env: "false", want: false},
		{name: "no", env: "no", want: false},
		{name: "an explicit on stays on", env: "1", want: true},
		{name: "a capability that asks for it, against the deployment", env: "0", choice: &yes, want: true},
		{name: "a capability that refuses it, with no switch", choice: &no, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvWorkerExtendsPlannerAgent, tc.env)
			if got := workerExtendsPlannerAgent(tc.choice); got != tc.want {
				t.Fatalf("env=%q choice=%v → %v, want %v", tc.env, tc.choice, got, tc.want)
			}
		})
	}
}

// Switched off, a worker is exactly what it was before: it takes the provider its capability
// asked for (the process default) and the pool resolves that one for it.
func TestAWorkerThatDoesNotExtendItsPlannerTakesItsOwnProvider(t *testing.T) {
	installFakeCodexClient(t)
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_CLINE_PROVIDER", "deepseek")
	t.Setenv("AUTONOMY_CLINE_MODEL", "deepseek-v4-pro")
	store := resumeTestStore(t)
	created, err := store.CreateAccount(Account{
		Harness: "codex", Vendor: "openai", Label: "codex main", Enabled: true, IsDefault: true,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	planner := &Agent{ID: 9105, Name: "agent-9105", Lifecycle: AgentLifecyclePersistent}
	planner.adoptAccount(&created)

	factory := NewAgentFactory()
	runtime := NewRuntime(factory)
	runtime.cycle = &DecisionContext{Agent: planner}
	no := false
	sess, err := runtime.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose: "code_edit", TaskID: "task-9105", Backend: "cursor", ExtendsPlannerAgent: &no,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	worker := factory.Get(sess.ID())
	if worker == nil {
		t.Fatalf("the acquired worker is not registered: %s", sess.ID())
	}
	if worker.AccountID == created.ID {
		t.Fatalf("worker account=%q, want it *not* to be the task's account when it does not extend its planner", worker.AccountID)
	}
	if worker.Backend != llmbackend.Cline {
		t.Fatalf("worker backend=%q want the provider the capability asked for (the process default: cline)", worker.Backend)
	}
	// It is the pool's default for *that* harness, and that account's root is where it works:
	// the account still owns the root, it is simply not the task's account.
	defaults, err := store.ListAccounts(AccountFilter{Harness: "cline"})
	if err != nil || len(defaults) == 0 {
		t.Fatalf("list cline accounts: %v (%d)", err, len(defaults))
	}
	poolDefault := defaults[0]
	if worker.AccountID != poolDefault.ID {
		t.Fatalf("worker account=%q want the pool's cline default %s", worker.AccountID, poolDefault.ID)
	}
	if want := AgentWorkspacePathIn(poolDefault.WorkspaceRoot, worker.Name); worker.Workspace != want {
		t.Fatalf("worker workspace=%q want %q (that account's root + its own name)", worker.Workspace, want)
	}
}

// The same switch from the outside: nothing in the code changes, the deployment says so.
func TestTheDeploymentCanSwitchWorkerInheritanceOff(t *testing.T) {
	t.Setenv(EnvWorkerExtendsPlannerAgent, "0")
	store := resumeTestStore(t)
	created, err := store.CreateAccount(Account{
		Harness: "codex", Vendor: "openai", Label: "codex main", Enabled: true, IsDefault: true,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	planner := &Agent{ID: 9106, Name: "agent-9106", Lifecycle: AgentLifecyclePersistent}
	planner.adoptAccount(&created)
	factory := NewAgentFactory()
	runtime := NewRuntime(factory)
	runtime.cycle = &DecisionContext{Agent: planner}

	plan, err := assignAgentRuntime(planner, agentRuntimePolicy{DelegatingAgent: nil})
	if err != nil || plan.Account == nil || plan.Account.ID != created.ID {
		t.Fatalf("account=%v err=%v want the task's account for the decision to be about", plan.Account, err)
	}
	// With inheritance off, the acquisition must not consult it at all — the worker keeps the
	// local backend here, exactly as it did before this feature existed.
	sess, err := runtime.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose: "code_edit", TaskID: "task-9106", Backend: string(llmbackend.Local),
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if worker := factory.Get(sess.ID()); worker == nil || worker.AccountID != "" {
		t.Fatalf("worker=%+v want no account on it", worker)
	}
}
