package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"github.com/kaulie/autonomy/src/llmbackend/cline"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// This is the conformance suite for the provider-neutral event contract: for
// every semantic (thinking, tool call, assistant text, ...) both backends must
// map onto the same Kind, the same Channel and the same neutral payload keys.
// Adding a provider means adding its samples here — the table is the contract.

type contractSample struct {
	semantic string
	// cursor event (native Cursor SDK message)
	cursor cursorsdk.RunEvent
	// cline event: an inner agent event, or a core event when clineCore is set.
	clineInner map[string]any
	clineCore  string

	wantKind    llmbackend.EventKind
	wantChannel llmbackend.EventChannel
	// wantKeys are the neutral keys both providers must fill.
	wantKeys map[string]any
	// wantCursorKeys / wantClineKeys are neutral keys only that provider can fill
	// (for example duration_ms: Cursor reports it, Cline derives it later).
	wantCursorKeys map[string]any
	wantClineKeys  map[string]any
	// clineOnly marks a shape only one backend produces (a streamed block end).
	clineOnly bool
	// wantText is the event's own text (payload text / TextDelta where relevant).
	wantText string
	// wantEmptyTextDelta pins the block-end rule: the terminator carries the text
	// in the payload but must not repeat it as a delta (that would duplicate the
	// aggregated thinking message).
	wantEmptyTextDelta bool
	// wantNoDuration asserts the CLINE leg has no duration (Cline does not report
	// one; the aggregation derives it). Cursor reports it, so the flag skips it.
	wantNoDuration bool
}

func contractSamples() []contractSample {
	return []contractSample{
		{
			semantic: "thinking block (cursor) / thinking deltas (cline)",
			cursor: cursorsdk.RunEvent{Type: "thinking", Payload: map[string]any{
				"text": "let me think", "thinking_duration_ms": float64(2500),
			}},
			clineInner:  map[string]any{"type": "content_start", "contentType": "reasoning", "reasoning": "let me think", "redacted": false},
			wantKind:    llmbackend.KindThought,
			wantChannel: llmbackend.ChannelThought,
			wantKeys:    map[string]any{llmbackend.KeyText: "let me think"},
			// Cursor reports the thinking duration on the message; Cline has no
			// such field (the aggregation derives it from the event span).
			wantCursorKeys: map[string]any{llmbackend.KeyDurationMS: int64(2500)},
			wantNoDuration: true,
			wantText:       "let me think",
		},
		{
			semantic:  "streamed thinking block end (cline only)",
			clineOnly: true,
			clineInner: map[string]any{
				"type": "content_end", "contentType": "reasoning", "reasoning": "let me think",
			},
			wantKind:           llmbackend.KindThoughtEnd,
			wantChannel:        llmbackend.ChannelThought,
			wantKeys:           map[string]any{llmbackend.KeyText: "let me think"},
			wantText:           "let me think",
			wantEmptyTextDelta: true,
		},
		{
			semantic: "assistant text block (cursor) / assistant delta (cline)",
			cursor: cursorsdk.RunEvent{Type: "assistant", Payload: map[string]any{
				"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "the answer"}}},
			}},
			clineInner:  map[string]any{"type": "content_start", "contentType": "text", "text": "the answer", "accumulated": "the answer"},
			wantKind:    llmbackend.KindAssistant,
			wantChannel: llmbackend.ChannelAssistant,
			wantText:    "the answer",
		},
		{
			semantic: "tool call started",
			cursor: cursorsdk.RunEvent{Type: "tool_call", Payload: map[string]any{
				"call_id": "c1", "name": "shell", "status": "running", "args": map[string]any{"command": "ls"},
			}},
			clineInner: map[string]any{"type": "content_start", "contentType": "tool", "toolName": "shell",
				"toolCallId": "c1", "input": map[string]any{"command": "ls"}},
			wantKind:    llmbackend.KindToolCallStarted,
			wantChannel: llmbackend.ChannelTool,
			wantKeys:    map[string]any{llmbackend.KeyCallID: "c1", llmbackend.KeyName: "shell", llmbackend.KeyStatus: "running"},
		},
		{
			semantic: "tool call completed",
			cursor: cursorsdk.RunEvent{Type: "tool_call", Payload: map[string]any{
				"call_id": "c1", "name": "shell", "status": "completed",
				"args": map[string]any{"command": "ls"}, "result": "a.go\n",
			}},
			clineInner: map[string]any{"type": "content_end", "contentType": "tool", "toolName": "shell",
				"toolCallId": "c1", "output": "a.go\n", "durationMs": float64(12)},
			wantKind:    llmbackend.KindToolCallCompleted,
			wantChannel: llmbackend.ChannelTool,
			wantKeys:    map[string]any{llmbackend.KeyCallID: "c1", llmbackend.KeyStatus: "completed"},
		},
		{
			semantic: "run status",
			cursor: cursorsdk.RunEvent{Type: "status", Payload: map[string]any{
				"status": "running", "message": "working",
			}},
			clineCore:   "status",
			clineInner:  map[string]any{"status": "running"},
			wantKind:    llmbackend.KindStatus,
			wantChannel: llmbackend.ChannelStatus,
			wantKeys:    map[string]any{llmbackend.KeyStatus: "running"},
		},
		{
			semantic: "token usage",
			cursor: cursorsdk.RunEvent{Type: "usage", Payload: map[string]any{
				"usage": map[string]any{"input_tokens": float64(100), "output_tokens": float64(7)},
			}},
			clineInner: map[string]any{"type": "usage", "inputTokens": float64(100), "outputTokens": float64(7),
				"cacheReadTokens": float64(50), "totalCost": 0.0001},
			wantKind:    llmbackend.KindUsage,
			wantChannel: llmbackend.ChannelMeta,
			wantKeys:    map[string]any{llmbackend.KeyInputTokens: int64(100), llmbackend.KeyOutputTokens: int64(7)},
		},
	}
}

func TestProviderEventContract(t *testing.T) {
	for _, tc := range contractSamples() {
		t.Run(tc.semantic, func(t *testing.T) {
			if !tc.clineOnly {
				t.Run("cursor", func(t *testing.T) {
					ev, ok := llmbackend.MapNativeLLMEvent(llmbackend.ProviderCursor, tc.cursor, time.Now())
					if !ok {
						t.Fatal("cursor event dropped")
					}
					assertContract(t, ev, tc, tc.wantCursorKeys, "cursor")
				})
			}
			t.Run("cline", func(t *testing.T) {
				cline := cline.ClineStreamAdapter{}
				var native any
				if tc.clineCore != "" {
					native = clineEvent(tc.clineCore, tc.clineInner)
				} else {
					native = clineAgentEvent(tc.clineInner)
				}
				ev, ok := cline.MapEvent(native, time.Now())
				if !ok {
					t.Fatal("cline event dropped")
				}
				assertContract(t, ev, tc, tc.wantClineKeys, "cline")
			})
		})
	}
}

func assertContract(t *testing.T, ev llmbackend.Event, tc contractSample, extraKeys map[string]any, leg string) {
	t.Helper()
	// Same semantic, same neutral keys; granularity may differ (Cursor blocks vs
	// Cline deltas), so compare the kind family.
	if got := ev.Kind.Family(); got != tc.wantKind.Family() {
		t.Fatalf("kind=%q (family %q) want family %q (event_type=%q)", ev.Kind, got, tc.wantKind.Family(), ev.EventType)
	}
	if ev.Kind == "" {
		t.Fatalf("event has no kind (event_type=%q)", ev.EventType)
	}
	if ev.Channel != tc.wantChannel {
		t.Fatalf("channel=%q want %q", ev.Channel, tc.wantChannel)
	}
	for key, want := range tc.wantKeys {
		got, ok := ev.Payload[key]
		if !ok {
			t.Fatalf("neutral key %q missing from payload %v", key, ev.Payload)
		}
		if got != want {
			t.Fatalf("payload[%q]=%#v want %#v", key, got, want)
		}
	}
	for key, want := range extraKeys {
		got, ok := ev.Payload[key]
		if !ok {
			t.Fatalf("provider-neutral key %q missing from payload %v", key, ev.Payload)
		}
		if got != want {
			t.Fatalf("payload[%q]=%#v want %#v", key, got, want)
		}
	}
	if tc.wantNoDuration && leg == "cline" {
		if _, present := ev.Payload[llmbackend.KeyDurationMS]; present {
			t.Fatalf("unexpected %s in payload %v", llmbackend.KeyDurationMS, ev.Payload)
		}
	}
	if tc.wantEmptyTextDelta && ev.TextDelta != "" {
		t.Fatalf("text_delta=%q want empty (block end must not repeat the text)", ev.TextDelta)
	}
	if tc.wantText != "" {
		text := ev.TextDelta
		if text == "" {
			text, _ = ev.Payload[llmbackend.KeyText].(string)
		}
		if text != tc.wantText {
			t.Fatalf("text=%q want %q", text, tc.wantText)
		}
	}
}

// TestProviderEventContractKeepsNativePayload guards the fidelity rule: neutral
// keys are added next to the provider's own fields, never instead of them.
func TestProviderEventContractKeepsNativePayload(t *testing.T) {
	cursor, ok := llmbackend.MapNativeLLMEvent(llmbackend.ProviderCursor, cursorsdk.RunEvent{
		Type: "tool_call",
		Payload: map[string]any{
			"call_id": "c1", "name": "shell", "status": "completed", "args": map[string]any{"command": "ls"},
		},
	}, time.Now())
	if !ok {
		t.Fatal("cursor event dropped")
	}
	for _, key := range []string{"call_id", "name", "status", "args"} {
		if _, ok := cursor.Payload[key]; !ok {
			t.Fatalf("cursor payload lost native key %q", key)
		}
	}

	cline, ok := cline.ClineStreamAdapter{}.MapEvent(clineAgentEvent(map[string]any{
		"type": "content_end", "contentType": "tool", "toolName": "run_commands",
		"toolCallId": "c9", "output": "hi\n", "durationMs": float64(7),
	}), time.Now())
	if !ok {
		t.Fatal("cline event dropped")
	}
	for _, key := range []string{"toolCallId", "toolName", "output", "durationMs"} {
		if _, ok := cline.Payload[key]; !ok {
			t.Fatalf("cline payload lost native key %q", key)
		}
	}
}
