package deepseek

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
)

func TestEventMapping(t *testing.T) {
	started := time.Now().Add(-time.Second)
	cases := []struct {
		native  map[string]any
		channel llmbackend.EventChannel
		kind    llmbackend.EventKind
		check   func(llmbackend.Event) bool
	}{
		{map[string]any{"type": "content_delta", "text": "hi"}, llmbackend.ChannelAssistant, llmbackend.KindAssistantDelta,
			func(e llmbackend.Event) bool {
				return e.TextDelta == "hi" && e.Payload[llmbackend.KeyText] == "hi" && e.Role == "assistant"
			}},
		{map[string]any{"type": "reasoning_delta", "text": "hmm"}, llmbackend.ChannelThought, llmbackend.KindThoughtDelta,
			func(e llmbackend.Event) bool { return e.TextDelta == "hmm" }},
		{map[string]any{"type": "reasoning_end", "text": "all of it"}, llmbackend.ChannelThought, llmbackend.KindThoughtEnd,
			func(e llmbackend.Event) bool { return e.Payload[llmbackend.KeyText] == "all of it" }},
		{map[string]any{"type": "tool_call", "id": "c1", "name": "read_file", "arguments": `{"path":"x"}`}, llmbackend.ChannelTool, llmbackend.KindToolCallStarted,
			func(e llmbackend.Event) bool {
				args, _ := e.Payload[llmbackend.KeyArgs].(map[string]any)
				return e.Name == "read_file" && e.Payload[llmbackend.KeyCallID] == "c1" && args["path"] == "x" && e.Payload[llmbackend.KeyStatus] == "running"
			}},
		{map[string]any{"type": "tool_result", "id": "c1", "name": "read_file", "content": "boom", "is_error": true, "duration_ms": int64(7)}, llmbackend.ChannelTool, llmbackend.KindToolCallCompleted,
			func(e llmbackend.Event) bool {
				return e.Payload[llmbackend.KeyStatus] == "failed" && e.Payload[llmbackend.KeyResult] == "boom" && e.Payload[llmbackend.KeyDurationMS] == int64(7)
			}},
		{map[string]any{"type": "usage", "usage": map[string]any{"prompt_tokens": 3.0, "completion_tokens": 4.0, "prompt_cache_hit_tokens": 1.0}}, llmbackend.ChannelStatus, llmbackend.KindUsage,
			func(e llmbackend.Event) bool {
				return e.Payload[llmbackend.KeyInputTokens] == int64(3) && e.Payload[llmbackend.KeyOutputTokens] == int64(4) &&
					e.Payload[llmbackend.KeyCacheReadTokens] == int64(1) && e.Payload[llmbackend.KeyTotalTokens] == int64(7)
			}},
		{map[string]any{"type": "finish", "finish_reason": "stop"}, llmbackend.ChannelStatus, llmbackend.KindStatus,
			func(e llmbackend.Event) bool { return e.Payload[llmbackend.KeyStatus] == "stop" }},
		{map[string]any{"type": "error", "message": "HTTP 402: Insufficient Balance"}, llmbackend.ChannelError, llmbackend.KindError,
			func(e llmbackend.Event) bool {
				return e.Payload[llmbackend.KeyMessage] == "HTTP 402: Insufficient Balance"
			}},
		{map[string]any{"type": "tool_call_delta", "name": "read_file", "arguments": "{"}, llmbackend.ChannelMeta, llmbackend.KindMeta,
			func(e llmbackend.Event) bool { return e.Name == "read_file" }},
	}
	for _, c := range cases {
		// Through the core's registry, the way a harness Prompt reaches its adapter.
		ev, ok := llmbackend.MapNativeLLMEvent(llmbackend.ProviderDeepseekHarness, c.native, started)
		if !ok || ev.Channel != c.channel || ev.Kind != c.kind || !c.check(ev) {
			t.Errorf("%v -> ok=%v channel=%s kind=%s payload=%v", c.native["type"], ok, ev.Channel, ev.Kind, ev.Payload)
		}
		if ev.EventType != c.native["type"] || ev.ElapsedMS < 1000 {
			t.Errorf("%v: event type %q elapsed %d", c.native["type"], ev.EventType, ev.ElapsedMS)
		}
		if c.native["type"] == "tool_call" && c.native["arguments"] != `{"path":"x"}` {
			t.Errorf("the provider payload was modified: %v", c.native)
		}
	}
	if _, ok := (streamAdapter{}).MapEvent("not a map", started); ok {
		t.Error("a non-map native event was mapped")
	}
	if _, ok := (streamAdapter{}).MapEvent(map[string]any{"text": "no type"}, started); ok {
		t.Error("an untyped native event was mapped")
	}
}

func TestRegistration(t *testing.T) {
	found := false
	for _, b := range llmbackend.Harnesses() {
		found = found || b == llmbackend.Deepseek
	}
	if !found {
		t.Fatalf("deepseek not registered: %v", llmbackend.Harnesses())
	}
	if got := llmbackend.ModelDefault(llmbackend.Deepseek); got != DefaultModel {
		t.Fatalf("default model = %q", got)
	}
	if llmbackend.PlacesSystemAtSession(llmbackend.Deepseek) {
		t.Fatal("deepseek places the role prompt as the first turn")
	}
	t.Setenv("AUTONOMY_LLM_BACKEND", "deepseek")
	if got := llmbackend.DefaultBackend(); got != llmbackend.Deepseek {
		t.Fatalf("AUTONOMY_LLM_BACKEND=deepseek selected %q", got)
	}
	if vendors, _ := llmbackend.VendorsFor(context.Background(), llmbackend.Deepseek, llmbackend.Creds{}); len(vendors) != 1 || vendors[0] != "deepseek" {
		t.Fatalf("vendors = %v", vendors)
	}
	if models, _ := llmbackend.ModelsFor(context.Background(), llmbackend.Deepseek, llmbackend.Creds{}); len(models) == 0 || models[0] != DefaultModel {
		t.Fatalf("models = %v", models)
	}

	// The registry builds a working session for an agent on this backend.
	f := newFake(t, []string{delta("content", "OK"), finish("stop")})
	host := f.host(t.TempDir())
	sess := llmbackend.New(host)
	if _, _, err := sess.Attach(context.Background(), llmbackend.ModeAgent); err != nil {
		t.Fatal(err)
	}
	if out, err := sess.PromptText(context.Background(), "ping", llmbackend.ModeAgent); err != nil || out != "OK" {
		t.Fatalf("out=%q err=%v", out, err)
	}

	// The account probe: the key is checked against GET /models.
	creds := llmbackend.Creds{APIKey: "good-key", BaseURL: f.srv.URL}
	if res, err := llmbackend.ProbeHarness(context.Background(), llmbackend.Deepseek, creds, false); err != nil || !res.Load || res.Model != DefaultModel {
		t.Fatalf("probe = %+v, %v", res, err)
	}
	creds.APIKey = "bad-key"
	if _, err := llmbackend.ProbeHarness(context.Background(), llmbackend.Deepseek, creds, false); err == nil || !strings.Contains(err.Error(), "Authentication Fails") {
		t.Fatalf("bad key probe err = %v", err)
	}
}

func TestAttachNeedsAccountKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "ambient-must-not-be-used")
	host := &probeHost{facts: llmbackend.Facts{Workspace: t.TempDir()}}
	if _, err := newSession(host).Attach(context.Background(), llmbackend.ModeAgent); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("err = %v", err)
	}
}

// TestLive runs one real turn when DEEPSEEK_API_KEY is set (DEEPSEEK_BASE_URL, DEEPSEEK_MODEL
// optional). It is skipped otherwise — the runtime itself never reads these variables.
func TestLive(t *testing.T) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set")
	}
	t.Setenv("AUTONOMY_DEEPSEEK_SESSIONS_DIR", t.TempDir())
	creds := llmbackend.Creds{APIKey: key, BaseURL: os.Getenv("DEEPSEEK_BASE_URL"), Model: os.Getenv("DEEPSEEK_MODEL")}
	res, err := probe(context.Background(), creds, true)
	if err != nil || !res.Live || !strings.Contains(res.Text, "OK") {
		t.Fatalf("live probe = %+v, %v", res, err)
	}
}
