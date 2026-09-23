package autonomy_test

import (
	"context"
	"github.com/kaulie/autonomy/src/llmbackend"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src"
)

// TestClineAgentLive drives the whole autonomy Cline path against the real
// bridge and provider: Agent.AttachCline → PromptLLMStream → neutral events.
// It is opt-in (Node + bridge deps + provider credentials):
//
//	CLINE_LIVE=1 AUTONOMY_CLINE_PROVIDER=deepseek AUTONOMY_CLINE_MODEL=deepseek-v4-pro \
//	AUTONOMY_CLINE_API_KEY=sk-... go test ./src -run TestClineAgentLive -v -timeout 5m
func TestClineAgentLive(t *testing.T) {
	if os.Getenv("CLINE_LIVE") != "1" {
		t.Skip("set CLINE_LIVE=1 to run")
	}
	if os.Getenv("AUTONOMY_CLINE_PROVIDER") == "" || os.Getenv("AUTONOMY_CLINE_MODEL") == "" {
		// Allowed: the bridge falls back to the provider/model saved by
		// `cline auth`, so an authenticated machine can run with no env at all.
		t.Logf("AUTONOMY_CLINE_PROVIDER/MODEL unset; relying on the saved cline auth config")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	agent := &autonomy.Agent{
		ID:        9001,
		Name:      "agent-cline-live",
		Lifecycle: autonomy.AgentLifecycleEphemeral,
		Workspace: t.TempDir(),
	}
	if err := agent.AttachCline(ctx); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if agent.LLMProvider != llmbackend.ProviderCline || agent.LLMAgentID == "" {
		t.Fatalf("agent not bound to cline: provider=%q id=%q", agent.LLMProvider, agent.LLMAgentID)
	}

	channels := map[llmbackend.EventChannel]int{}
	text, meta, err := agent.PromptLLMStream(ctx, "Reply with exactly: pong. Do not use any tools.", autonomy.ReasonModeAgent,
		func(ev llmbackend.Event) { channels[ev.Channel]++ })
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	t.Logf("text=%q status=%s usage=%+v channels=%v", text, meta.Status, meta.Usage, channels)
	if !strings.Contains(strings.ToLower(text), "pong") {
		t.Fatalf("unexpected text: %q", text)
	}
	if meta.Status != llmbackend.StatusFinished {
		t.Fatalf("status=%q", meta.Status)
	}
	if meta.Usage.InputTokens <= 0 || meta.Usage.OutputTokens <= 0 {
		t.Fatalf("usage not reported: %+v", meta.Usage)
	}
	if channels[llmbackend.ChannelAssistant] == 0 {
		t.Fatalf("no assistant events streamed: %v", channels)
	}
	if meta.ProviderRunID == "" {
		t.Fatal("no provider run/session id recorded")
	}
}
