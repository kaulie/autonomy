package autonomy

import (
	"context"
	"fmt"
)

// This file routes an autonomy Agent's LLM calls to whichever backend backs it
// (the Cursor SDK bridge or the Cline SDK bridge), so the reasoner and the
// runtime loop stay backend-agnostic. Everything provider-specific lives in
// cursor_agent.go / cline_agent.go plus the matching LLMStreamAdapter.

// ensureLLMSession attaches the agent's backend session (idempotent) and returns
// a label for logs: the provider-side session/agent id when known.
func (a *Agent) ensureLLMSession(ctx context.Context, model, cwd string, mode ReasonMode) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil agent")
	}
	switch a.Backend {
	case AgentBackendCline:
		return a.ensureClineSession(ctx, model, cwd, clineModeFor(mode))
	case AgentBackendCursor, "":
		agent, err := a.ensureCursorSession(ctx, model, cwd)
		if err != nil {
			return "", err
		}
		return agent.ID, nil
	default:
		return "", fmt.Errorf("agent backend %q has no LLM session", a.Backend)
	}
}

// PromptLLMStream runs one prompt on the agent's backend and reports neutral
// LLMEvents as they arrive. mode is the autonomy reasoning mode; backends that
// distinguish plan from act (Cline) map it onto their own session mode.
func (a *Agent) PromptLLMStream(ctx context.Context, prompt string, mode ReasonMode, onEvent func(LLMEvent)) (string, LLMRunResult, error) {
	if a == nil {
		return "", LLMRunResult{}, fmt.Errorf("nil agent")
	}
	switch a.Backend {
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
