package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"fmt"
	"strings"
)

// This file is the runtime's door onto llmbackend: an agent's turns go through one
// Session, whichever backend backs it (src/llmbackend owns both, and everything else the
// providers differ in). What stays here is what the runtime knows and the backend does not:
// which backend an agent *is* (its row), and what a turn's mode means.

// effectiveBackend is the backend an agent will use: the attached one, or the
// host default while it is still unattached (agents are registered as "local"
// by AgentFactory and only get a backend when a session is attached).
func (a *Agent) effectiveBackend() llmbackend.Backend {
	if a == nil || a.Backend == "" || a.Backend == llmbackend.Local {
		return llmbackend.DefaultBackend()
	}
	return a.Backend
}

// llmMode is a reasoning mode as the backend's own vocabulary (they are the same words:
// plan work stays read-only, everything else may act).
func llmMode(mode ReasonMode) llmbackend.Mode { return llmbackend.Mode(mode) }

// ensureLLMSession attaches the agent's backend session (idempotent) and returns a label
// for logs: the provider-side session/agent id when known.
//
// What the agent runs on — provider, model, account, workspace root — is not decided here: it
// is the runtime plan (assignAgentRuntime, src/agent_runtime.go), which is also what
// Runtime.AcquireAgent consults for a worker. A turn adds two things to the policy it passes:
// the model the caller suggests (a Cursor-oriented default from LLMReasoner), and the harness
// the agent already ran on — a resumed row knows its backend, and that is what the pool then
// resolves an account for. No account to run on is a refusal with a pointer at /accounts, not a
// quiet fallback.
func (a *Agent) ensureLLMSession(ctx context.Context, model, cwd string, mode ReasonMode) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil agent")
	}
	policy := a.runtimePolicy
	policy.DelegatingAgent = nil // a turn has no delegation in flight
	policy.RequestedModel = model
	if strings.TrimSpace(policy.RequestedBackend) == "" {
		policy.RequestedBackend = string(a.effectiveBackend())
	}
	plan, err := assignAgentRuntime(a, policy)
	if err != nil {
		return "", err
	}
	a.applyAgentRuntime(plan)
	// The plan's account owns the workspace root: applyAgentRuntime has just given the agent its
	// own directory under it (its root + the agent's name). cwd is the caller's suggestion — the
	// agent's workspace as it was *before* the plan was applied — so it is only what an agent
	// with no account root runs in, or one whose directory was chosen for it
	// (Agent.workspaceChosen, which cwd then is). Applying it unconditionally put every agent
	// back on the runtime's default root and undid the account's directory on its first run
	// (2026-09-24: the pool's roots were ignored unless the task had named the account at
	// accept time).
	if plan.Account == nil || strings.TrimSpace(plan.Account.WorkspaceRoot) == "" || a.workspaceChosen {
		a.SetWorkspace(cwd)
	}
	if plan.Account != nil {
		// The account is what the row keeps: the next process resumes the same entry.
		a.Persist()
	}
	id, _, err := a.llmSession().Attach(ctx, llmMode(mode))
	return id, err
}

// PromptLLMStream runs one prompt on the agent's backend and reports neutral LLMEvents as
// they arrive.
func (a *Agent) PromptLLMStream(ctx context.Context, prompt string, mode ReasonMode, onEvent func(llmbackend.Event)) (string, llmbackend.RunResult, error) {
	if a == nil {
		return "", llmbackend.RunResult{}, fmt.Errorf("nil agent")
	}
	return a.llmSession().Prompt(ctx, prompt, llmMode(mode), onEvent)
}

// PromptLLMText is PromptLLMStream without event delivery.
func (a *Agent) PromptLLMText(ctx context.Context, prompt string, mode ReasonMode) (string, error) {
	text, _, err := a.PromptLLMStream(ctx, prompt, mode, nil)
	return text, err
}

// defaultAgentModel is the model a backend falls back to when nothing else said: the
// harness's own default (empty for cline and codex, which ask their bridge/CLI, and
// "composer-2" for cursor). An account's model wins over it (src/account_service.go):
// credentials and models come from the pool, not from this process's environment.
func defaultAgentModel(backend llmbackend.Backend) string {
	return llmbackend.ModelDefault(backend)
}

// bridgeCallErr and emptyModelResponseErr live in llmbackend now; these two wrappers keep
// the runtime's own callers (and tests) reading the same thing.
func bridgeCallErr(ctx context.Context, err error) error { return llmbackend.BridgeCallErr(ctx, err) }

func emptyModelResponseErr(status, msg string) error {
	return llmbackend.EmptyModelResponseErr(status, msg)
}
