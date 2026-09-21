package autonomy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLLMTraceLivePlanTurns covers the reasoner side of the same fix: plan-mode
// sessions are resident too (the LLMReasoner reuses one per mode+cwd), so a
// second plan prompt must find the session alive and remember the first.
//
//	CLINE_LIVE=1 AUTONOMY_CLINE_PROVIDER=deepseek AUTONOMY_CLINE_MODEL=deepseek-v4-pro \
//	AUTONOMY_CLINE_API_KEY=sk-... go test ./src -run TestLLMTraceLivePlanTurns -v -timeout 5m
func TestLLMTraceLivePlanTurns(t *testing.T) {
	if os.Getenv("CLINE_LIVE") != "1" {
		t.Skip("set CLINE_LIVE=1 to run")
	}
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	agent := &Agent{ID: 9300, Name: "agent-live-plan", Lifecycle: AgentLifecycleEphemeral, Workspace: t.TempDir()}
	if err := agent.AttachCline(ctx); err != nil {
		t.Fatalf("attach: %v", err)
	}

	first := "Reply with exactly: plan-ack. Do not use any tools."
	trace := BeginLLMTrace(agent, "task-live-plan", 1, ReasonModePlan, first)
	text, runRes, err := agent.PromptLLMStream(ctx, first, ReasonModePlan, trace.Emit)
	trace.Finish(runRes)
	if err != nil {
		t.Fatalf("plan turn 1: %v", err)
	}
	if runRes.Status != LLMStatusFinished || !strings.Contains(strings.ToLower(text), "plan-ack") {
		t.Fatalf("plan turn 1: status=%s text=%q", runRes.Status, text)
	}
	session := trace.runID
	if session == "" {
		session = runRes.ProviderRunID
	}

	// Second plan turn on the same agent: only possible if the SDK kept the
	// session (before this fix it failed with session_not_found).
	second := "In one short line: what did I ask you in my first message?"
	trace2 := BeginLLMTrace(agent, "task-live-plan", 2, ReasonModePlan, second)
	text2, runRes2, err := agent.PromptLLMStream(ctx, second, ReasonModePlan, trace2.Emit)
	trace2.Finish(runRes2)
	if err != nil {
		t.Fatalf("plan turn 2 (resident session): %v", err)
	}
	t.Logf("plan turn 2: status=%s text=%q (session %s)", runRes2.Status, text2, firstNonEmptyString(runRes2.ProviderRunID, session))
	if !strings.Contains(strings.ToLower(text2), "plan-ack") {
		t.Fatalf("plan turn 2 did not remember turn 1: %q", text2)
	}
}
