package autonomy

import (
	"fmt"
	"strings"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// What an agent runs on — which provider (harness), which model, whose credentials and which
// workspace root — is **one decision**, and this file is the one place that makes it:
//
//	assignAgentRuntime(agent, policy) → agentRuntimePlan
//
// Its inputs are the agent itself (its role, and what it already recorded) plus the policies in
// force: what the piece that created the agent asked for (a capability's Backend/Model when it
// acquired a worker, an initializer's Model, the reasoner's model suggestion), the switches a
// deployment controls (AUTONOMY_LLM_BACKEND, AUTONOMY_WORKER_EXTENDS_PLANNER_AGENT), and the
// account pool. The answer is applied by exactly one applier — `Agent.applyAgentRuntime` — and
// the attach helpers (AttachCursor / AttachCline / AttachCodex) only open the session the plan
// asks for. Nothing else in the runtime picks a provider or a model.
//
// The rule, in one sentence: **the account decides the provider and the model; the role decides
// whose account.** A task's own agent (planner) runs on the account the task named, else its
// harness's default account; a worker runs on its planner's account unless the acquisition says
// otherwise. What a caller *asks* for is a hint: the pool outranks it, and it fills the gaps the
// pool leaves (2026-09-24: a task on a codex account had its work done by a cline worker, because
// a worker picked the process default backend and that harness's own default account).

// agentRuntimePlan is what one agent runs on: the answer assignAgentRuntime gives.
type agentRuntimePlan struct {
	// Account is the pool entry the agent bills: its harness, credential, model and root. nil
	// when nothing in the pool applies (a local-only agent has no provider to bill).
	Account *Account
	// Backend is the harness its session is built on, Provider who the interaction is with.
	Backend  llmbackend.Backend
	Provider llmbackend.Provider
	// Model is what the session opens with; "" means "the harness decides" (its own default).
	Model string
	// Why is how the plan came about, for logs and status: "the account acct-… recorded on
	// agent-12" vs "the pool's default cline account" vs "inherited from agent-7".
	Why string
}

// agentRuntimePolicy is what the pieces around an agent ask for, and what the deployment
// switches on. The zero value is "nothing asked for, deployment defaults apply" — which is
// exactly a task's own agent being woken for a turn.
type agentRuntimePolicy struct {
	// RequestedBackend is what the caller asked for, in broker.AcquireAgentOpts.Backend's
	// vocabulary: "" or "cursor" mean "the host default" (the legacy label), anything else
	// names a harness. The pool outranks it when there is an account.
	RequestedBackend string
	// RequestedModel is the model the caller suggested (a capability, the reasoner). The pool
	// outranks it; it is what a harness with no model of its own opens with.
	RequestedModel string
	// ExtendsPlannerAgent is the acquisition-time policy for a worker: nil = the deployment's
	// default (AUTONOMY_WORKER_EXTENDS_PLANNER_AGENT), true/false = this acquisition's answer.
	ExtendsPlannerAgent *bool
	// DelegatingAgent is the agent that delegated to this one, when there is one (the runtime
	// is inside that agent's cycle). It is whose account a worker that extends its planner
	// inherits — and nil outside a delegation, which is why a *turn* never needs it: a worker
	// that inherited has the account recorded by then.
	DelegatingAgent *Agent
}

// assignAgentRuntime decides what one agent runs on. Every caller that builds a session goes
// through here (Runtime.AcquireAgent for a capability-acquired worker, Agent.ensureLLMSession
// for a turn), so "which provider, which model" has one answer and one place to read it.
func assignAgentRuntime(agent *Agent, policy agentRuntimePolicy) (agentRuntimePlan, error) {
	if agent == nil {
		return agentRuntimePlan{}, fmt.Errorf("nil agent")
	}
	account, why, err := accountForAgent(agent, policy)
	if err != nil {
		return agentRuntimePlan{}, err
	}
	// The harness: the account's, else what the caller asked for, else the deployment default.
	backend := requestedBackend(policy.RequestedBackend)
	if account != nil {
		if harness := llmbackend.Backend(account.Harness); harness != "" {
			backend = harness
			why += " → harness " + string(harness)
		}
	}
	// The model: the account's, else what the caller asked for, else the harness's own default
	// ("" for a harness that asks its bridge or CLI, "composer-2" for cursor).
	model := ""
	switch {
	case account != nil && strings.TrimSpace(account.Model) != "":
		model = strings.TrimSpace(account.Model)
	case strings.TrimSpace(policy.RequestedModel) != "":
		model = strings.TrimSpace(policy.RequestedModel)
	default:
		model = defaultAgentModel(backend)
	}
	return agentRuntimePlan{
		Account:  account,
		Backend:  backend,
		Provider: providerFor(backend),
		Model:    model,
		Why:      why,
	}, nil
}

// accountForAgent is the "whose account" half of the plan: which pool entry the agent bills.
//
//  1. the account the agent recorded (the task named it, or it inherited one earlier) — and if
//     that one is gone or disabled, that is said plainly instead of quietly running somewhere
//     else, because a task that named an account must not bill another;
//  2. for a worker that extends its planner: the account the delegating agent resolves to;
//  3. otherwise the harness's default account, else its first enabled one — the pool's own order;
//  4. otherwise nothing can run: the error names the harness and points at /accounts.
//
// A local-only agent (the requested backend is "local") has no provider to bill and no account
// to resolve: it answers nil, and the plan is "local, no account".
func accountForAgent(agent *Agent, policy agentRuntimePolicy) (*Account, string, error) {
	if requestedBackend(policy.RequestedBackend) == llmbackend.Local {
		return nil, "a local-only agent: no provider, nothing to bill", nil
	}
	store := activeAccountStore()
	if store == nil {
		return nil, "", fmt.Errorf("store not ready")
	}
	if id := strings.TrimSpace(agent.AccountID); id != "" {
		account, err := store.GetAccount(id)
		if err != nil {
			return nil, "", err
		}
		if account == nil {
			return nil, "", fmt.Errorf("account %s (recorded on %s) is gone: add one at /accounts", id, agent.Name)
		}
		if !account.Enabled {
			return nil, "", fmt.Errorf("account %s (%s) is disabled: enable it, or point the agent at another one, at /accounts", account.ID, account.Label)
		}
		return account, "the account " + account.ID + " recorded on " + agent.Name, nil
	}
	// A worker that extends its planner runs where the task runs, so the account comes from the
	// agent that delegated to it (src/worker_account.go holds the switch's own documentation).
	if agent.Role == AgentRoleWorker && workerExtendsPlannerAgent(policy.ExtendsPlannerAgent) && policy.DelegatingAgent != nil {
		inherited, _, err := accountForAgent(policy.DelegatingAgent, agentRuntimePolicy{})
		if err != nil {
			return nil, "", err
		}
		if inherited != nil {
			return inherited, "inherited from " + policy.DelegatingAgent.Name + " (" + inherited.ID + ")", nil
		}
	}
	// The harness's default account, else its first enabled one.
	harness := requestedBackend(policy.RequestedBackend)
	enabled := true
	pool, err := store.ListAccounts(AccountFilter{Harness: string(harness), Enabled: &enabled})
	if err != nil {
		return nil, "", err
	}
	if len(pool) == 0 {
		return nil, "", fmt.Errorf("no enabled %s account in the pool: add one at /accounts (POST /api/accounts) — the runtime reads credentials from the pool, not from the environment", harness)
	}
	for i := range pool {
		if pool[i].IsDefault {
			return &pool[i], "the pool's default " + string(harness) + " account", nil
		}
	}
	return &pool[0], "the pool's first enabled " + string(harness) + " account", nil
}

// requestedBackend is what a caller's Backend request means: "" and the legacy "cursor" label
// stand for "the host default", which AUTONOMY_LLM_BACKEND selects at runtime; anything else
// names a harness (capabilities say "an autonomous coding agent", not a vendor).
func requestedBackend(requested string) llmbackend.Backend {
	backend := llmbackend.Backend(strings.ToLower(strings.TrimSpace(requested)))
	if backend == "" || backend == llmbackend.Cursor {
		return llmbackend.DefaultBackend()
	}
	return backend
}

// providerFor is the provider a backend reports itself as (they are the same words; a local-only
// agent has none).
func providerFor(backend llmbackend.Backend) llmbackend.Provider {
	if backend == llmbackend.Local {
		return ""
	}
	return llmbackend.Provider(backend)
}

// applyAgentRuntime makes one plan the agent's own: what it bills (the account: id, label,
// credential, workspace root), what it runs (backend, provider) and what it opens its session
// with (model). It is the only writer of those fields on the runtime's own path.
func (a *Agent) applyAgentRuntime(plan agentRuntimePlan) {
	if a == nil {
		return
	}
	if plan.Account != nil {
		a.adoptAccount(plan.Account)
	}
	if plan.Backend != "" {
		a.SetBackend(plan.Backend, plan.Provider)
	}
	if plan.Model != "" {
		a.SetModel(plan.Model)
	}
}
