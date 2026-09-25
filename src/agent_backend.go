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
// This is also where the agent's **account** is resolved (src/agent_account.go): the pool
// entry it runs on — its harness, its model, and the credential the session is built from.
// Resolving in the one place a session is attached is what makes "credentials come from the
// pool, never from the environment" true for every path: a task's first run, a resumed run,
// and a delegated worker all come through here. No account to run on is a refusal with a
// pointer at /accounts, not a quiet fallback.
//
// For a worker the *account* is not resolved here: it is inherited from the agent that
// delegated to it, at acquisition (Runtime.AcquireAgent → workerAccount), so a worker resolves
// the same entry here — and a task's account governs its whole delegation chain.
//
// model is what the caller suggested (a Cursor-oriented default from LLMReasoner); the
// account's own model wins, and an account without one leaves the model to its harness. cwd is
// the caller's suggestion for where the run happens, and the account's root is what usually
// decides it (see below).
func (a *Agent) ensureLLMSession(ctx context.Context, model, cwd string, mode ReasonMode) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil agent")
	}
	account, err := resolveAccountFor(a)
	if err != nil {
		return "", err
	}
	a.adoptAccount(account)
	if account.Model == "" {
		a.SetModel(model)
	}
	// The account owns the workspace root: adoptAccount has just given the agent its own
	// directory under it (its root + the agent's name). cwd is the caller's suggestion — the
	// agent's workspace as it was *before* the account was resolved — so it is only what an
	// agent whose account names no root runs in, or one whose directory was chosen for it
	// (Agent.workspaceChosen, which cwd then is). Applying it unconditionally put every agent
	// back on the runtime's default root and undid the account's directory on its first run
	// (2026-09-24: the pool's roots were ignored unless the task had named the account at
	// accept time).
	if strings.TrimSpace(account.WorkspaceRoot) == "" || a.workspaceChosen {
		a.SetWorkspace(cwd)
	}
	if a.AccountID != "" {
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
