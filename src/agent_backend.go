package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"fmt"
)

// This file routes an autonomy Agent's LLM calls to whichever backend backs it
// (the Cursor SDK bridge or the Cline SDK bridge), so the reasoner and the
// runtime loop stay backend-agnostic. Everything provider-specific lives in
// cursor_agent.go / cline_agent.go plus the matching llmbackend.StreamAdapter.

// effectiveBackend is the backend an agent will use: the attached one, or the
// host default while it is still unattached (agents are registered as "local"
// by AgentFactory and only get a backend when a session is attached).
func (a *Agent) effectiveBackend() llmbackend.Backend {
	if a == nil || a.Backend == "" || a.Backend == llmbackend.Local {
		return llmbackend.DefaultBackend()
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
	case llmbackend.Cline:
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
func (a *Agent) PromptLLMStream(ctx context.Context, prompt string, mode ReasonMode, onEvent func(llmbackend.Event)) (string, llmbackend.RunResult, error) {
	if a == nil {
		return "", llmbackend.RunResult{}, fmt.Errorf("nil agent")
	}
	switch a.effectiveBackend() {
	case llmbackend.Cline:
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

// defaultAgentModel is the model a backend would run on when nothing else said
// otherwise: the Cursor default (AUTONOMY_LLM_MODEL, else composer-2), or the
// Cline model id. It is empty for Cline when AUTONOMY_CLINE_MODEL is unset,
// because then the bridge resolves the model from the provider/model saved by
// `cline auth` — a fact this process does not hold. It is what /health reports
// next to the backend, so a caller can see what this runtime will run on without
// reading its environment (docs/llm-backend.md).
func defaultAgentModel(backend llmbackend.Backend) string {
	if backend == llmbackend.Cline {
		return llmbackend.ResolveClineModel()
	}
	return llmbackend.DefaultCursorModel()
}

// emptyModelResponseErr is what a run that produced no text at all is: a failed
// run, whatever the provider calls it. It is the failure whose fix is not in the
// prompt — an exhausted account, a spending limit or a model that is gone ends a
// run exactly this way, with the provider's own words in msg and nothing else —
// so the error says where the answer is: the backend belongs to the runtime
// process, not to the caller or to the agent (AUTONOMY_LLM_BACKEND, GET /health,
// docs/llm-backend.md).
func emptyModelResponseErr(status, msg string) error {
	return fmt.Errorf("empty model response (status=%s msg=%s) — the model answered nothing; "+
		"if the account is out of quota or the model is gone, the LLM backend is a runtime "+
		"setting (AUTONOMY_LLM_BACKEND, see docs/llm-backend.md)", status, msg)
}
