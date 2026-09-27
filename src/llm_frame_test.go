package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"strings"
	"testing"
)

// An agent's life has **two prompt injections**, in this order:
//
//	agent created → its first prompt: the frame  (LLMSession.GiveFirstPrompt)
//	              → the task's prompt: the delta (LLMSession.Say / reasoningPrompt)
//
// These tests pin that contract: the frame is never part of a task's prompt, and a session is
// given the frame before it is asked anything about a task. See docs/prompt.md.

// The task prompt is the delta alone: the current task / context entity / world / runtime
// context and the cycle it is. The instructions that do not change per cycle are not in it —
// they went out as the session's first prompt.
func TestTaskPromptIsTheDeltaAndCarriesNoFrame(t *testing.T) {
	t.Setenv("PROJECT_ROOT", preparePolicyRoot(t))

	agent := &Agent{ID: 8801, Name: "agent-8801", Lifecycle: AgentLifecycleEphemeral, Workspace: t.TempDir()}
	ctx := DecisionContext{
		Task:  &Task{ID: "t-frame", Description: "add a /healthz endpoint", GoalType: GoalType_FEATURE},
		Agent: agent,
		Cycle: 2,
	}

	taskPrompt, err := reasoningPrompt(ctx, ReasoningInput{})
	if err != nil {
		t.Fatalf("task prompt: %v", err)
	}
	if !strings.Contains(taskPrompt, "## Decision Cycle 2") {
		t.Fatalf("the task prompt must be this cycle's delta:\n%s", taskPrompt)
	}
	if !strings.Contains(taskPrompt, `"id": "t-frame"`) {
		t.Fatalf("the task prompt must carry the current task:\n%s", taskPrompt)
	}
	if strings.Contains(taskPrompt, "# Autonomy Bootstrap Prompt") {
		t.Fatalf("the task prompt must not carry the frame:\n%s", taskPrompt)
	}

	// The first prompt is the frame, and it renders **without a task**: the per-cycle values
	// are marked as coming with the task prompt, which is why initialization can give it
	// before anything has been accepted for the agent.
	firstPrompt, err := buildReasoningFrame(DecisionContext{Agent: agent}, ReasoningInput{})
	if err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	if !strings.Contains(firstPrompt, "# Autonomy Bootstrap Prompt") {
		t.Fatalf("the first prompt is where the policy rides:\n%s", firstPrompt)
	}
	if !strings.Contains(firstPrompt, reasoningDeltaMarker) {
		t.Fatalf("the first prompt must mark where the per-cycle values come from:\n%s", firstPrompt)
	}
	if strings.Contains(firstPrompt, "## Decision Cycle") {
		t.Fatalf("the first prompt must not carry a decision cycle:\n%s", firstPrompt)
	}
	if strings.Contains(firstPrompt, `"id": "t-frame"`) {
		t.Fatalf("the first prompt must not carry a task's values:\n%s", firstPrompt)
	}
}

// recordingSession is an agent's backend with the prompts it was given kept in order: what a
// session actually received, without a provider in the loop.
type recordingSession struct{ prompts []string }

func (r *recordingSession) Attach(context.Context, llmbackend.Mode) (string, bool, error) {
	return "sess-recording", false, nil
}

func (r *recordingSession) Prompt(_ context.Context, text string, _ llmbackend.Mode, _ func(llmbackend.Event)) (string, llmbackend.RunResult, error) {
	r.prompts = append(r.prompts, text)
	return `{"type":"need_input","reason":"no task yet"}`, llmbackend.RunResult{Status: llmbackend.StatusFinished}, nil
}

func (r *recordingSession) PromptText(ctx context.Context, text string, mode llmbackend.Mode) (string, error) {
	out, _, err := r.Prompt(ctx, text, mode, nil)
	return out, err
}

func (r *recordingSession) Dispose(context.Context)   {}
func (r *recordingSession) ProviderSessionID() string { return "sess-recording" }
func (r *recordingSession) Resumed() bool             { return false }
func (r *recordingSession) ResumedFrom() string       { return "" }
func (r *recordingSession) Mode() string              { return "" }

// A session's first prompt is the frame, and it goes out before the task's prompt: an agent
// that was created and then handed a task is never asked about the task first. It is sent
// once per session — a session created later (a restart, a mode change) asks for it again,
// and gets it before whatever prompted it.
func TestFirstPromptIsTheFrameAndGoesBeforeTheTaskPrompt(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_CLINE_PROVIDER", "deepseek")
	t.Setenv("AUTONOMY_CLINE_MODEL", "deepseek-v4-pro")
	t.Setenv("PROJECT_ROOT", preparePolicyRoot(t))

	backend := &recordingSession{}
	agent := &Agent{
		ID: 8804, Name: "agent-8804", Role: AgentRolePlanner, Lifecycle: AgentLifecycleEphemeral,
		Backend: llmbackend.Cline, Workspace: t.TempDir(), llm: backend,
	}

	sess := NewLLMSession(nil, agent, SessionOpts{})
	sent, err := sess.GiveFirstPrompt(context.Background())
	if err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	if !sent {
		t.Fatal("a fresh session must be given its first prompt")
	}
	if len(backend.prompts) != 1 || !strings.Contains(backend.prompts[0], "# Autonomy Bootstrap Prompt") {
		t.Fatalf("the first prompt must be the frame, got %d prompts: %q", len(backend.prompts), backend.prompts)
	}
	if agent.needsLLMFrame() {
		t.Fatal("the session was given the frame: it must not ask for it again")
	}

	// Idempotent: asking again sends nothing.
	again, err := sess.GiveFirstPrompt(context.Background())
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if again || len(backend.prompts) != 1 {
		t.Fatalf("the first prompt is once per session, prompts=%q", backend.prompts)
	}

	// A session created later has been told nothing: the frame goes again, ahead of the prompt
	// that needed the session (here a task's prompt).
	agent.resetLLMFrame()
	if _, err := sess.Say(context.Background(), "## Decision Cycle 1 — current values", 1); err != nil {
		t.Fatalf("say: %v", err)
	}
	if len(backend.prompts) != 3 {
		t.Fatalf("want the task prompt sent after a fresh frame, got %d prompts: %q", len(backend.prompts), backend.prompts)
	}
	if !strings.Contains(backend.prompts[1], "# Autonomy Bootstrap Prompt") {
		t.Fatalf("a session's first prompt must be the frame:\n%s", backend.prompts[1])
	}
	if !strings.Contains(backend.prompts[2], "## Decision Cycle 1") || strings.Contains(backend.prompts[2], "# Autonomy Bootstrap Prompt") {
		t.Fatalf("the turn the caller asked for must be the task prompt alone:\n%s", backend.prompts[2])
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

	// A different mode is a different session key — the Cline session is sticky in (mode, cwd)
	// — so this is a new SDK session and the frame goes again. (The workspace half of that key
	// is the agent's account's business, src/agent_backend.go: it cannot be moved by passing a
	// cwd, which is what this test used to do.)
	if _, err := agent.ensureLLMSession(context.Background(), "composer-2", agent.Workspace, ReasonModeAgent); err != nil {
		t.Fatalf("new session: %v", err)
	}
	if !agent.needsLLMFrame() {
		t.Fatal("a newly created session must resend the frame")
	}
}
