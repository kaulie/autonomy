package autonomy

import (
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// One decision, one place: assignAgentRuntime answers "which provider, which model, whose
// account" from the agent's role (planner or worker), what the caller asked for, the switches a
// deployment controls, and the pool. These are its cases; the code paths that *use* it
// (Runtime.AcquireAgent, Agent.ensureLLMSession) are covered by their own tests.
func TestTheRuntimePlanIsOneDecision(t *testing.T) {
	store := resumeTestStore(t)
	cline, err := store.CreateAccount(Account{
		Harness: "cline", Vendor: "deepseek", Label: "cline default", Model: "deepseek-v4-flash",
		Enabled: true, IsDefault: true, WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create cline: %v", err)
	}
	codex, err := store.CreateAccount(Account{
		Harness: "codex", Vendor: "openai", Label: "codex default", Enabled: true, IsDefault: true,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create codex: %v", err)
	}
	cursor, err := store.CreateAccount(Account{
		Harness: "cursor", Label: "cursor named", Model: "composer-2", Enabled: true,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create cursor: %v", err)
	}
	yes, no := true, false
	task := &Agent{ID: 9200, Name: "agent-9200", Role: AgentRolePlanner, Lifecycle: AgentLifecyclePersistent}
	task.adoptAccount(&cursor)

	cases := []struct {
		name      string
		agent     *Agent
		policy    agentRuntimePolicy
		wantAcc   *Account
		wantBe    llmbackend.Backend
		wantModel string
	}{
		{
			name:    "a planner with nothing recorded runs on its harness's default account",
			agent:   &Agent{ID: 9201, Name: "agent-9201", Role: AgentRolePlanner},
			policy:  agentRuntimePolicy{RequestedBackend: "codex"},
			wantAcc: &codex, wantBe: llmbackend.Codex, wantModel: "",
		},
		{
			name:    "the account a task named wins, and brings its harness and model",
			agent:   &Agent{ID: 9202, Name: "agent-9202", Role: AgentRolePlanner, AccountID: cursor.ID},
			policy:  agentRuntimePolicy{RequestedBackend: "cline", RequestedModel: "whatever"},
			wantAcc: &cursor, wantBe: llmbackend.Cursor, wantModel: "composer-2",
		},
		{
			name:    "an account with no model leaves the caller's suggestion in charge",
			agent:   &Agent{ID: 9203, Name: "agent-9203", Role: AgentRolePlanner, AccountID: codex.ID},
			policy:  agentRuntimePolicy{RequestedModel: "gpt-5-codex"},
			wantAcc: &codex, wantBe: llmbackend.Codex, wantModel: "gpt-5-codex",
		},
		{
			name:    "a worker that extends its planner runs on the task's account",
			agent:   &Agent{ID: 9204, Name: "agent-9204", Role: AgentRoleWorker},
			policy:  agentRuntimePolicy{RequestedBackend: "cursor", DelegatingAgent: task},
			wantAcc: &cursor, wantBe: llmbackend.Cursor, wantModel: "composer-2",
		},
		{
			name:    "a worker the capability refuses to extend takes its own provider's account",
			agent:   &Agent{ID: 9205, Name: "agent-9205", Role: AgentRoleWorker},
			policy:  agentRuntimePolicy{RequestedBackend: "cline", ExtendsPlannerAgent: &no, DelegatingAgent: task},
			wantAcc: &cline, wantBe: llmbackend.Cline, wantModel: "deepseek-v4-flash",
		},
		{
			name:    "a capability may ask for it against the deployment's switch",
			agent:   &Agent{ID: 9206, Name: "agent-9206", Role: AgentRoleWorker},
			policy:  agentRuntimePolicy{RequestedBackend: "cline", ExtendsPlannerAgent: &yes, DelegatingAgent: task},
			wantAcc: &cursor, wantBe: llmbackend.Cursor, wantModel: "composer-2",
		},
		{
			name:    "a worker with no delegating agent keeps its own provider",
			agent:   &Agent{ID: 9207, Name: "agent-9207", Role: AgentRoleWorker},
			policy:  agentRuntimePolicy{RequestedBackend: "cline"},
			wantAcc: &cline, wantBe: llmbackend.Cline, wantModel: "deepseek-v4-flash",
		},
		{
			name:    "a local-only agent bills nothing and runs no harness",
			agent:   &Agent{ID: 9208, Name: "agent-9208", Role: AgentRoleWorker},
			policy:  agentRuntimePolicy{RequestedBackend: "local"},
			wantAcc: nil, wantBe: llmbackend.Local, wantModel: defaultAgentModel(llmbackend.Local),
		},
	}

	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvWorkerExtendsPlannerAgent, "")
			plan, err := assignAgentRuntime(tc.agent, tc.policy)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			switch {
			case tc.wantAcc == nil && plan.Account != nil:
				t.Fatalf("account=%s want none", plan.Account.ID)
			case tc.wantAcc != nil && plan.Account == nil:
				t.Fatalf("account=nil want %s", tc.wantAcc.ID)
			case tc.wantAcc != nil && plan.Account.ID != tc.wantAcc.ID:
				t.Fatalf("account=%s want %s", plan.Account.ID, tc.wantAcc.ID)
			}
			if plan.Backend != tc.wantBe {
				t.Fatalf("backend=%q want %q", plan.Backend, tc.wantBe)
			}
			if plan.Model != tc.wantModel {
				t.Fatalf("model=%q want %q", plan.Model, tc.wantModel)
			}
			if strings.TrimSpace(plan.Why) == "" {
				t.Fatal("a plan must say how it came about (it is what logs and status print)")
			}
			// The plan is applied by one applier, and it is the only writer of these fields.
			tc.agent.applyAgentRuntime(plan)
			if tc.agent.Backend != tc.wantBe || tc.agent.Model != tc.wantModel {
				t.Fatalf("applied backend=%q model=%q want %q/%q", tc.agent.Backend, tc.agent.Model, tc.wantBe, tc.wantModel)
			}
			if tc.wantAcc != nil && tc.agent.AccountID != tc.wantAcc.ID {
				t.Fatalf("applied account=%q want %s", tc.agent.AccountID, tc.wantAcc.ID)
			}
		})
	}
}

// A recorded account that is gone or disabled is refused here — the one place every provider
// decision goes through — with the words a human can act on.
func TestThePlanRefusesAnAccountItCannotRunOn(t *testing.T) {
	store := resumeTestStore(t)
	off, err := store.CreateAccount(Account{
		Harness: "cline", Vendor: "deepseek", Label: "off", Enabled: false, WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, tc := range []struct {
		name  string
		agent *Agent
		want  string
	}{
		{
			name:  "an account that is not in the pool",
			agent: &Agent{ID: 9210, Name: "agent-9210", AccountID: "acct-gone"},
			want:  "acct-gone",
		},
		{
			name:  "an account that is disabled",
			agent: &Agent{ID: 9211, Name: "agent-9211", AccountID: off.ID},
			want:  "disabled",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := assignAgentRuntime(tc.agent, agentRuntimePolicy{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want it to name %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "/accounts") {
				t.Fatalf("err=%v want it to point at /accounts", err)
			}
		})
	}
}
