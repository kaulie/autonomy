package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"strings"
	"testing"
)

// A reasoning session is multi-turn (the Cline session / Cursor agent keeps the
// conversation), so the AGENT_V2 frame — the instructions that do not change per
// cycle — is sent once per session, and every later decision cycle sends only the
// delta (current task / context entity / world / runtime context). These tests
// pin that contract; see docs/execution-loop.md.

func TestReasoningPromptSendsFrameOncePerSession(t *testing.T) {
	t.Setenv("PROJECT_ROOT", preparePolicyRoot(t))

	agent := &Agent{ID: 8801, Name: "agent-8801", Lifecycle: AgentLifecycleEphemeral, Workspace: t.TempDir()}
	ctx := DecisionContext{
		Task:  &Task{ID: "t-frame", Description: "add a /healthz endpoint", GoalType: GoalType_FEATURE},
		Agent: agent,
		Cycle: 1,
	}

	first, sentFrame, err := reasoningPrompt(ctx, ReasoningInput{})
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if !sentFrame {
		t.Fatal("cycle 1 of a new session must carry the frame")
	}
	if !strings.Contains(first, "# Autonomy Bootstrap Prompt") {
		t.Fatalf("cycle 1 must carry the AGENT_V2 frame:\n%s", first)
	}
	if !strings.Contains(first, "## Decision Cycle 1") {
		t.Fatalf("cycle 1 must carry its delta:\n%s", first)
	}

	// The session answered, so the frame is delivered for good.
	agent.markLLMFrameSent()

	second, sentFrame2, err := reasoningPrompt(ctx, ReasoningInput{})
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if sentFrame2 {
		t.Fatal("cycle 2 on the same session must not repeat the frame")
	}
	if strings.Contains(second, "# Autonomy Bootstrap Prompt") {
		t.Fatalf("cycle 2 must not repeat the frame:\n%s", second)
	}
	if !strings.Contains(second, "## Decision Cycle 1") {
		t.Fatalf("cycle 2 must still carry the delta:\n%s", second)
	}
	if len(second) >= len(first)/2 {
		t.Fatalf("cycle 2 (%d bytes) must be a fraction of cycle 1 (%d bytes)", len(second), len(first))
	}

	// The delta follows the state: next cycle, plus what the last one did.
	ctx.Cycle = 2
	ctx.History = []Result{{Message: "executed 1 action(s): code_edit"}}
	third, sentFrame3, err := reasoningPrompt(ctx, ReasoningInput{})
	if err != nil {
		t.Fatalf("cycle 3: %v", err)
	}
	if sentFrame3 {
		t.Fatal("cycle 3 must not repeat the frame either")
	}
	if !strings.Contains(third, "## Decision Cycle 2") {
		t.Fatalf("cycle 3 delta must name its cycle:\n%s", third)
	}
	if !strings.Contains(third, "previous_actions") {
		t.Fatalf("cycle 3 delta must carry previous_actions:\n%s", third)
	}
	if !strings.Contains(third, `"id": "t-frame"`) {
		t.Fatalf("cycle 3 delta must carry the current task:\n%s", third)
	}
}

func TestLLMFrameFlagsAreNilSafe(t *testing.T) {
	agent := &Agent{ID: 8802, Name: "agent-8802", Lifecycle: AgentLifecycleEphemeral, Workspace: t.TempDir()}
	if !agent.needsLLMFrame() {
		t.Fatal("a fresh agent has no frame yet")
	}
	agent.markLLMFrameSent()
	if agent.needsLLMFrame() {
		t.Fatal("the frame stays sent for the session")
	}
	agent.resetLLMFrame()
	if !agent.needsLLMFrame() {
		t.Fatal("a recreated session asks for the frame again")
	}

	var nilAgent *Agent
	if !nilAgent.needsLLMFrame() {
		t.Fatal("a nil agent must ask for the frame")
	}
	nilAgent.markLLMFrameSent() // must not panic
	nilAgent.resetLLMFrame()    // must not panic
}

// A Cline session is sticky in (mode, cwd): re-entering the same pair reuses the
// session (frame already sent), while a new pair is a new SDK session and needs
// the frame again. Hermetic: the fake bridge replaces Node/`@cline/sdk`.
func TestLLMFrameResetsOnNewClineSession(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_CLINE_PROVIDER", "deepseek")
	t.Setenv("AUTONOMY_CLINE_MODEL", "deepseek-v4-pro")

	ws := t.TempDir()
	agent := &Agent{ID: 8803, Name: "agent-8803", Lifecycle: AgentLifecycleEphemeral, Backend: llmbackend.Local, Workspace: ws}
	if _, err := agent.ensureLLMSession(context.Background(), "composer-2", ws, ReasonModePlan); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	if agent.llm == nil || agent.llm.ProviderSessionID() == "" {
		t.Fatal("no cline session was attached")
	}
	if !agent.needsLLMFrame() {
		t.Fatal("a new session needs the frame")
	}
	agent.markLLMFrameSent()

	// Same (mode, cwd): the resident session is reused, the frame stays sent.
	if _, err := agent.ensureLLMSession(context.Background(), "composer-2", ws, ReasonModePlan); err != nil {
		t.Fatalf("reuse session: %v", err)
	}
	if agent.needsLLMFrame() {
		t.Fatal("reusing a session must not resend the frame")
	}

	// Different cwd → different session key → new session → frame again.
	other := t.TempDir()
	if _, err := agent.ensureLLMSession(context.Background(), "composer-2", other, ReasonModePlan); err != nil {
		t.Fatalf("new session: %v", err)
	}
	if !agent.needsLLMFrame() {
		t.Fatal("a newly created session must resend the frame")
	}
}
