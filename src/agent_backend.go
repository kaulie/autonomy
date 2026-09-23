package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"fmt"
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
// model is the Cursor-oriented model default from the caller (AUTONOMY_LLM_MODEL or
// LLMReasoner); the Cline backend ignores it and uses AUTONOMY_CLINE_MODEL or the
// provider/model saved by `cline auth`, because Cursor model ids mean nothing to a Cline
// provider.
func (a *Agent) ensureLLMSession(ctx context.Context, model, cwd string, mode ReasonMode) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil agent")
	}
	a.SetModel(model)
	a.SetWorkspace(cwd)
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

// defaultAgentModel is the model a backend would run on when nothing else said
// otherwise: the Cursor default (AUTONOMY_LLM_MODEL, else composer-2), or the
// Cline model id. It is empty for Cline when AUTONOMY_CLINE_MODEL is unset,
// because then the bridge resolves the model from the provider/model saved by
// `cline auth` — a fact this process does not hold. It is what /health reports
// next to the backend, so a caller can see what this runtime will run on without
// reading its environment (docs/llm-backend.md).
func defaultAgentModel(backend llmbackend.Backend) string {
	return llmbackend.ModelDefault(backend)
}

// bridgeCallErr and emptyModelResponseErr live in llmbackend now; these two wrappers keep
// the runtime's own callers (and tests) reading the same thing.
func bridgeCallErr(ctx context.Context, err error) error { return llmbackend.BridgeCallErr(ctx, err) }

func emptyModelResponseErr(status, msg string) error {
	return llmbackend.EmptyModelResponseErr(status, msg)
}
