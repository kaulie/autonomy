package autonomy

import (
	"context"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// The pool is the only place an agent's credentials, model and workspace root come from
// (src/agent_account.go). These tests pin the resolution: what wins, what is refused, and what
// an agent adopts from the account it lands on.

func TestAnAgentResolvesItsHarnessDefaultAccount(t *testing.T) {
	store := resumeTestStore(t)
	root := t.TempDir()
	account, err := store.CreateAccount(Account{
		Harness: "codex", Vendor: "openai", Label: "codex prod", Model: "gpt-5-codex",
		APIKey: "sk-codex-secret", BaseURL: "https://api.example.test/", WorkspaceRoot: root,
		Enabled: true, IsDefault: true,
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	t.Setenv("AUTONOMY_LLM_BACKEND", "codex")
	agent := &Agent{ID: 9911, Name: "agent-9911", Lifecycle: AgentLifecycleEphemeral}

	plan, err := assignAgentRuntime(agent, agentRuntimePolicy{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	resolved := plan.Account
	if resolved == nil || resolved.ID != account.ID {
		t.Fatalf("resolved %v, want the default account %s", resolved, account.ID)
	}

	agent.adoptAccount(resolved)
	if agent.AccountID != account.ID || agent.Backend != llmbackend.Codex {
		t.Fatalf("account=%q backend=%q want %s/codex", agent.AccountID, agent.Backend, account.ID)
	}
	if agent.Model != "gpt-5-codex" {
		t.Fatalf("model=%q want the account's model", agent.Model)
	}
	// The account owns its root; the agent works in its own directory under it.
	if want := AgentWorkspacePathIn(account.WorkspaceRoot, agent.Name); agent.Workspace != want {
		t.Fatalf("workspace=%q want %q (the account's root plus this agent's own name)", agent.Workspace, want)
	}
	// The credential the session is built from is the account's, verbatim: no environment
	// variable takes part in this.
	creds := agent.Facts().Creds
	if creds.APIKey != "sk-codex-secret" || creds.BaseURL != "https://api.example.test/" || creds.Vendor != "openai" {
		t.Fatalf("creds=%+v want the account's", creds)
	}
}

func TestARecordedAccountWinsOverTheDefault(t *testing.T) {
	store := resumeTestStore(t)
	first, err := store.CreateAccount(Account{
		Harness: "cursor", Label: "cursor default", WorkspaceRoot: t.TempDir(), Enabled: true, IsDefault: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := store.CreateAccount(Account{
		Harness: "cursor", Label: "cursor other", APIKey: "sk-other", WorkspaceRoot: t.TempDir(), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Setenv("AUTONOMY_LLM_BACKEND", "cursor")

	// Without a recorded account the default wins; with one, the recorded choice does — that
	// is what makes "the task named an account" mean the task bills that account.
	plain := &Agent{ID: 9912, Name: "agent-9912", Lifecycle: AgentLifecycleEphemeral}
	plainPlan, err := assignAgentRuntime(plain, agentRuntimePolicy{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plainPlan.Account == nil || plainPlan.Account.ID != first.ID {
		t.Fatalf("resolved %v, want the default %s", plainPlan.Account, first.ID)
	}

	named := &Agent{ID: 9913, Name: "agent-9913", Lifecycle: AgentLifecycleEphemeral, AccountID: second.ID}
	namedPlan, err := assignAgentRuntime(named, agentRuntimePolicy{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if namedPlan.Account == nil || namedPlan.Account.ID != second.ID {
		t.Fatalf("resolved %v, want the recorded %s", namedPlan.Account, second.ID)
	}
}

func TestPoolWithoutTheHarnessRefusesAndSaysWhereToAddOne(t *testing.T) {
	store := resumeTestStore(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "codex")
	// The fixture seeds cursor and cline; the codex pool is emptied on purpose here.
	codexAccounts, err := store.ListAccounts(AccountFilter{Harness: "codex"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, account := range codexAccounts {
		if err := store.DeleteAccount(account.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}

	agent := &Agent{ID: 9914, Name: "agent-9914", Lifecycle: AgentLifecycleEphemeral}
	_, err = assignAgentRuntime(agent, agentRuntimePolicy{})
	if err == nil {
		t.Fatal("an agent with no account to run on must be refused")
	}
	if !strings.Contains(err.Error(), "/accounts") {
		t.Fatalf("error %q must point at /accounts", err)
	}
}

func TestADisabledAccountIsRefusedNotSkipped(t *testing.T) {
	store := resumeTestStore(t)
	account, err := store.CreateAccount(Account{
		Harness: "cline", Vendor: "deepseek", Label: "off", WorkspaceRoot: t.TempDir(), Enabled: false,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	agent := &Agent{ID: 9915, Name: "agent-9915", Lifecycle: AgentLifecycleEphemeral, AccountID: account.ID}
	_, err = assignAgentRuntime(agent, agentRuntimePolicy{})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err=%v want a refusal naming the disabled account", err)
	}
}

// A task may name the account it runs on; the choice is validated when the instruction is
// accepted, so a bad id is a refused instruction rather than a task that dies at its first cycle.
func TestATaskCanNameTheAccountItRunsOn(t *testing.T) {
	store := resumeTestStore(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	account, err := store.CreateAccount(Account{
		Harness: "cline", Vendor: "minimax", Label: "minimax prod", Model: "minimax-m2",
		WorkspaceRoot: t.TempDir(), Enabled: true, IsDefault: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	f := NewAgentFactory()
	runtime := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	task, err := runtime.AcceptTask(AcceptTaskRequest{
		ID: "t-account", Description: "do the thing", AccountID: account.ID,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if task.AgentID == 0 {
		t.Fatal("no agent was paired with the task")
	}
	agent, err := store.GetAgent(task.AgentID)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if agent.AccountID != account.ID {
		t.Fatalf("agents.account_id=%q want %s", agent.AccountID, account.ID)
	}

	// And an account that does not exist is refused at accept.
	_, err = runtime.AcceptTask(AcceptTaskRequest{ID: "t-bad-account", Description: "x", AccountID: "acct-nope"})
	if err == nil || !strings.Contains(err.Error(), "/accounts") {
		t.Fatalf("err=%v want a refusal naming /accounts", err)
	}
}

// A refused account must leave nothing behind: no task row, and no agent paired with it. The
// caller was told the instruction was refused, so finding a task that claims to exist is a lie
// — and a later run of it would be the "quietly run on another key" this refuses to do.
func TestARefusedAccountLeavesNoTaskBehind(t *testing.T) {
	store := resumeTestStore(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	f := NewAgentFactory()
	runtime := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}

	if _, err := runtime.AcceptTask(AcceptTaskRequest{
		ID: "t-ghost", Description: "x", AccountID: "acct-nope",
	}); err == nil {
		t.Fatal("an account that is not in the pool must refuse the instruction")
	}
	task, err := store.GetTask("t-ghost")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task != nil {
		t.Fatalf("the refused instruction left %+v behind", task)
	}
	if agent, err := store.GetAgent(10000); err != nil {
		t.Fatalf("get agent: %v", err)
	} else if agent != nil && agent.CurrentTask != nil && agent.CurrentTask.ID == "t-ghost" {
		t.Fatalf("an agent was paired with the refused task: %+v", agent)
	}
}

var _ = context.Background

// An account must own a real directory: the root is required and absolute, the same rule
// web-cursor's pool enforces (docs/accounts.md).
func TestAnAccountNeedsAnAbsoluteRoot(t *testing.T) {
	store := resumeTestStore(t)
	_, err := store.CreateAccount(Account{Harness: "cline", Vendor: "deepseek", Label: "no root"})
	if err == nil || !strings.Contains(err.Error(), "agent_root_workspace is required") {
		t.Fatalf("err=%v want a required-root refusal", err)
	}
	_, err = store.CreateAccount(Account{Harness: "cline", Vendor: "deepseek", Label: "relative", WorkspaceRoot: "relative/root"})
	if err == nil || !strings.Contains(err.Error(), "must be an absolute path") {
		t.Fatalf("err=%v want an absolute-path refusal", err)
	}
}
