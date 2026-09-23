package autonomy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLLMTraceLiveMessages is the end-to-end proof for the live message layer:
// the real Cline bridge + provider, driven exactly like the runtime drives it
// (BeginLLMTrace → PromptLLMStream(trace.Emit) → Finish), must leave the
// conversation in llm_messages while it runs and log each row.
//
//	CLINE_LIVE=1 AUTONOMY_CLINE_PROVIDER=deepseek AUTONOMY_CLINE_MODEL=deepseek-v4-pro \
//	AUTONOMY_CLINE_API_KEY=sk-... go test ./src -run TestLLMTraceLiveMessages -v -timeout 5m
func TestLLMTraceLiveMessages(t *testing.T) {
	if os.Getenv("CLINE_LIVE") != "1" {
		t.Skip("set CLINE_LIVE=1 to run")
	}
	store, err := openStore(t, filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	agent := &Agent{ID: 9200, Name: "agent-live-messages", Lifecycle: AgentLifecycleEphemeral, Workspace: t.TempDir()}
	if err := agent.AttachCline(ctx); err != nil {
		t.Fatalf("attach: %v", err)
	}

	prompt := "Use the shell tool to run exactly: echo hi-from-llm-trace — then reply with just the output."
	trace := BeginLLMTrace(agent, "task-live-messages", 1, ReasonModeAgent, prompt)
	turnID := trace.handle.TurnID
	text, runRes, err := agent.PromptLLMStream(ctx, prompt, ReasonModeAgent, trace.Emit)
	if err != nil {
		trace.Finish(runRes)
		t.Fatalf("prompt: %v (turn %d had %d messages)", err, turnID, len(mustListMessages(t, store, turnID)))
	}
	trace.Finish(runRes)
	t.Logf("text=%q status=%s", text, runRes.Status)

	msgs := mustListMessages(t, store, turnID)
	if len(msgs) < 3 {
		t.Fatalf("messages=%d, want user + tool + assistant: %+v", len(msgs), msgs)
	}
	t.Logf("conversation:")
	for _, m := range msgs {
		t.Logf("  seq=%d role=%s content=%q normalized=%q", m.Seq, m.Role, m.Content, m.NormalizedContent)
	}
	if msgs[0].Role != LLMMessageRoleUser {
		t.Fatalf("first message=%+v, want the user input", msgs[0])
	}
	last := msgs[len(msgs)-1]
	if last.Role != LLMMessageRoleAssistant || last.Seq != len(msgs)-1 {
		t.Fatalf("last message=%+v, want the assistant return at seq %d", last, len(msgs)-1)
	}
	if !strings.Contains(strings.ToLower(last.Content), "hi-from-llm-trace") {
		t.Fatalf("assistant content=%q, want the tool output", last.Content)
	}
	var tool *LLMMessage
	for i := range msgs {
		if msgs[i].Role == LLMMessageRoleTool {
			tool = &msgs[i]
			break
		}
	}
	if tool == nil {
		t.Fatalf("no tool message recorded: %+v", msgs)
	}
	if !strings.Contains(tool.Content, "hi-from-llm-trace") {
		t.Fatalf("tool message content=%q, want the command output", tool.Content)
	}
}
