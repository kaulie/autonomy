package autonomy

import (
	"context"
	"fmt"
)

// This file routes an autonomy Agent's LLM calls to whichever backend backs it
// (the Cursor SDK bridge or the Cline SDK bridge), so the reasoner and the
// runtime loop stay backend-agnostic. Everything provider-specific lives in
// cursor_agent.go / cline_agent.go plus the matching LLMStreamAdapter.

// effectiveBackend is the backend an agent will use: the attached one, or the
// host default while it is still unattached (agents are registered as "local"
// by AgentFactory and only get a backend when a session is attached).
func (a *Agent) effectiveBackend() AgentBackend {
	if a == nil || a.Backend == "" || a.Backend == AgentBackendLocal {
		return defaultAgentBackend()
	}
	return a.Backend
}

// ensureLLMSession attaches the agent's backend session (idempotent) and returns
// a label for logs: the provider-side session/agent id when known.
//
// model is the Cursor-oriented model default from the caller (AUTONOMY_LLM_MODEL
// or LLMReasoner); the Cline backend ignores it and uses AUTONOMY_CLINE_MODEL or
// the provider/model saved by `cline auth`, because Cursor model ids mean
// nothing to a Cline provider.
func (a *Agent) ensureLLMSession(ctx context.Context, model, cwd string, mode ReasonMode) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil agent")
	}
	switch a.effectiveBackend() {
	case AgentBackendCline:
		return a.ensureClineSession(ctx, cwd, clineModeFor(mode))
	default:
		agent, err := a.ensureCursorSession(ctx, model, cwd)
		if err != nil {
			return "", err
		}
		return agent.ID, nil
	}
}

// PromptLLMStream runs one prompt on the agent's backend and reports neutral
// LLMEvents as they arrive. mode is the autonomy reasoning mode; backends that
// distinguish plan from act (Cline) map it onto their own session mode.
func (a *Agent) PromptLLMStream(ctx context.Context, prompt string, mode ReasonMode, onEvent func(LLMEvent)) (string, LLMRunResult, error) {
	if a == nil {
		return "", LLMRunResult{}, fmt.Errorf("nil agent")
	}
	switch a.effectiveBackend() {
	case AgentBackendCline:
		return a.PromptClineStream(ctx, prompt, clineModeFor(mode), onEvent)
	default:
		return a.PromptCursorStream(ctx, prompt, onEvent)
	}
}

// PromptLLMText is PromptLLMStream without event delivery.
func (a *Agent) PromptLLMText(ctx context.Context, prompt string, mode ReasonMode) (string, error) {
	text, _, err := a.PromptLLMStream(ctx, prompt, mode, nil)
	return text, err
}
