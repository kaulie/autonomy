package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"testing"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// This file covers the wiring bugs found while running the Cline backend for
// real: an unattached agent must still get a session (the LLM reasoner relies on
// it), and Cursor-oriented model ids must not leak into a Cline provider.
var _ = clinesdk.DefaultMode

func TestEnsureLLMSessionAttachesDefaultBackend(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_CLINE_PROVIDER", "deepseek")
	t.Setenv("AUTONOMY_CLINE_MODEL", "deepseek-v4-pro")

	// A freshly registered agent is "local" until a session is attached; the
	// LLM reasoner relies on ensureLLMSession doing that attach (this is the
	// regression that broke `AUTONOMY_LLM_BACKEND=cline go run ./cmd/autonomy`).
	agent := &Agent{ID: 9002, Name: "agent-9002", Lifecycle: AgentLifecycleEphemeral, Backend: llmbackend.Local, Workspace: t.TempDir()}
	agent.adoptAccount(testClineAccount())
	if got := agent.effectiveBackend(); got != llmbackend.Cline {
		t.Fatalf("effectiveBackend=%q want cline", got)
	}
	session, err := agent.ensureLLMSession(context.Background(), "composer-2", agent.Workspace, ReasonModePlan)
	if err != nil {
		t.Fatalf("ensureLLMSession: %v", err)
	}
	if session == "" || agent.llm == nil || agent.llm.ProviderSessionID() == "" {
		t.Fatal("no session was attached")
	}
	if agent.Backend != llmbackend.Cline {
		t.Fatalf("backend=%q want cline", agent.Backend)
	}
	// The Cursor-oriented model must not leak into the Cline session.
	if agent.Model != "deepseek-v4-pro" {
		t.Fatalf("model=%q want the cline model", agent.Model)
	}
}

func TestEnsureClineSessionAlsoServesReasonerBeforeAgentAttach(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	agent := newClineTestAgent(t)
	// Drop the yolo session, then ask the reasoner path for a plan session.
	agent.llm = nil
	agent.Backend = llmbackend.Local
	session, err := agent.ensureLLMSession(context.Background(), "composer-2", agent.Workspace, ReasonModePlan)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if session == "" || agent.llm == nil || agent.llm.Mode() != "plan" {
		t.Fatalf("session=%q mode=%q want a plan session", session, agent.llm.Mode())
	}
}
