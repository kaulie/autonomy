package autonomy

import (
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
)

func clineEvent(eventType string, payload map[string]any) clinesdk.RunEvent {
	return clinesdk.RunEvent{Type: eventType, SessionID: "cls-session-1", AgentID: "cls_1", Payload: payload}
}

func clineAgentEvent(inner map[string]any) clinesdk.RunEvent {
	return clineEvent("agent_event", map[string]any{"sessionId": "cls-session-1", "event": inner})
}

func TestClineAdapterMapsChannels(t *testing.T) {
	adapter := clineStreamAdapter{}
	if adapter.Provider() != LLMProviderCline {
		t.Fatalf("provider=%q", adapter.Provider())
	}
	cases := []struct {
		name      string
		native    any
		channel   LLMEventChannel
		eventType string
		text      string
		role      string
	}{
		{
			name:      "assistant delta",
			native:    clineAgentEvent(map[string]any{"type": "content_start", "contentType": "text", "text": "po", "accumulated": "po"}),
			channel:   LLMChannelAssistant,
			eventType: "agent_event:content_start:text",
			text:      "po",
			role:      "assistant",
		},
		{
			name:      "assistant final",
			native:    clineAgentEvent(map[string]any{"type": "content_end", "contentType": "text", "text": "pong"}),
			channel:   LLMChannelAssistant,
			eventType: "agent_event:content_end:text",
			text:      "pong",
			role:      "assistant",
		},
		{
			name:      "thinking delta",
			native:    clineAgentEvent(map[string]any{"type": "content_start", "contentType": "reasoning", "reasoning": "why not"}),
			channel:   LLMChannelThought,
			eventType: "agent_event:content_start:reasoning",
			text:      "why not",
			role:      "assistant",
		},
		{
			// content_end repeats the whole block, so it must not add text again.
			name:      "thinking block end",
			native:    clineAgentEvent(map[string]any{"type": "content_end", "contentType": "reasoning", "reasoning": "why not"}),
			channel:   LLMChannelThought,
			eventType: "agent_event:content_end:reasoning",
			text:      "",
			role:      "assistant",
		},
		{
			name: "tool call",
			native: clineAgentEvent(map[string]any{
				"type": "content_start", "contentType": "tool", "toolName": "run_commands",
				"toolCallId": "call-1", "input": map[string]any{"commands": []string{"echo hi"}},
			}),
			channel:   LLMChannelTool,
			eventType: "agent_event:content_start:tool",
			role:      "tool",
		},
		{
			name: "tool result",
			native: clineAgentEvent(map[string]any{
				"type": "content_end", "contentType": "tool", "toolName": "run_commands",
				"toolCallId": "call-1", "output": "hi\n", "durationMs": 7,
			}),
			channel:   LLMChannelTool,
			eventType: "agent_event:content_end:tool",
			role:      "tool",
		},
		{
			name:      "usage",
			native:    clineAgentEvent(map[string]any{"type": "usage", "inputTokens": 11, "outputTokens": 2}),
			channel:   LLMChannelMeta,
			eventType: "agent_event:usage",
			role:      "system",
		},
		{
			name:      "done",
			native:    clineAgentEvent(map[string]any{"type": "done", "reason": "completed", "text": "pong"}),
			channel:   LLMChannelResult,
			eventType: "agent_event:done",
			role:      "assistant",
		},
		{
			name:      "provider error",
			native:    clineAgentEvent(map[string]any{"type": "error", "message": "boom"}),
			channel:   LLMChannelError,
			eventType: "agent_event:error",
			role:      "assistant",
		},
		{
			name:      "lifecycle status",
			native:    clineEvent("status", map[string]any{"status": "running"}),
			channel:   LLMChannelStatus,
			eventType: "status",
			role:      "system",
		},
		{
			name:      "stdout chunk",
			native:    clineEvent("chunk", map[string]any{"stream": "stdout", "chunk": "hi\n"}),
			channel:   LLMChannelTool,
			eventType: "chunk:stdout",
			text:      "hi\n",
			role:      "tool",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped, ok := adapter.MapEvent(tc.native, time.Now())
			if !ok {
				t.Fatal("event was dropped")
			}
			if mapped.Channel != tc.channel {
				t.Fatalf("channel=%q want %q", mapped.Channel, tc.channel)
			}
			if mapped.EventType != tc.eventType {
				t.Fatalf("eventType=%q want %q", mapped.EventType, tc.eventType)
			}
			if mapped.TextDelta != tc.text {
				t.Fatalf("textDelta=%q want %q", mapped.TextDelta, tc.text)
			}
			if mapped.Role != tc.role {
				t.Fatalf("role=%q want %q", mapped.Role, tc.role)
			}
		})
	}
}

func TestClineAdapterAnnotatesToolPayloads(t *testing.T) {
	adapter := clineStreamAdapter{}
	mapped, ok := adapter.MapEvent(clineAgentEvent(map[string]any{
		"type": "content_end", "contentType": "tool", "toolName": "run_commands",
		"toolCallId": "call-9", "input": map[string]any{"commands": []string{"ls"}},
		"output": []map[string]any{{"query": "ls", "result": "a.go", "success": true}},
	}), time.Now())
	if !ok {
		t.Fatal("event dropped")
	}
	if mapped.Name != "run_commands" {
		t.Fatalf("name=%q", mapped.Name)
	}
	if got := payloadString(mapped.Payload, "call_id"); got != "call-9" {
		t.Fatalf("call_id=%q want call-9", got)
	}
	if mapped.Payload["args"] == nil {
		t.Fatal("args alias missing")
	}
	if mapped.Payload["result"] == nil {
		t.Fatal("result alias missing")
	}
	// The native payload stays available verbatim for replay.
	if _, ok := mapped.Payload["toolCallId"]; !ok {
		t.Fatal("native payload was not preserved")
	}
}

func TestClineAdapterDropsAgentEchoChunks(t *testing.T) {
	adapter := clineStreamAdapter{}
	if _, ok := adapter.MapEvent(clineEvent("chunk", map[string]any{"stream": "agent", "chunk": `{"type":"done"}`}), time.Now()); ok {
		t.Fatal("agent echo chunk should be dropped (it duplicates agent_event)")
	}
	if _, ok := adapter.MapEvent("not-a-cline-event", time.Now()); ok {
		t.Fatal("foreign native events must be dropped")
	}
}

func TestClineRunResultToLLMRun(t *testing.T) {
	reasoning := int64(42)
	usage := clinesdk.RunUsage{
		InputTokens: 1637, OutputTokens: 3, CacheReadTokens: 1536,
		TotalTokens: 1640, CostUSD: 0.000052113, HasCost: true, ReasoningTokens: &reasoning,
	}
	started := time.Now().Add(-2 * time.Second)
	meta := clineRunResultToLLMRun(clinesdk.RunResult{
		AgentID: "cls_1", SessionID: "cls-session-1", Status: "finished",
		Text: "pong", Usage: usage, StartedAt: started,
	}, started)
	if meta.Status != LLMStatusFinished {
		t.Fatalf("status=%q", meta.Status)
	}
	if meta.ProviderRunID != "cls-session-1" || meta.LLMAgentID != "cls_1" {
		t.Fatalf("ids: run=%q agent=%q", meta.ProviderRunID, meta.LLMAgentID)
	}
	if meta.Usage.InputTokens != 1637 || meta.Usage.CacheReadTokens != 1536 || meta.Usage.ReasoningTokens != 42 {
		t.Fatalf("usage=%+v", meta.Usage)
	}
	if !meta.Usage.CostKnown || meta.Usage.CostCents < 0.005 || meta.Usage.CostCents > 0.006 {
		t.Fatalf("cost=%+v want ~0.005 cents", meta.Usage)
	}
	if meta.RawOutput != "pong" {
		t.Fatalf("raw output=%q", meta.RawOutput)
	}
}

func TestClineRunResultWithoutCostStaysUnknown(t *testing.T) {
	meta := clineRunResultToLLMRun(clinesdk.RunResult{Status: "finished", Text: "ok"}, time.Now())
	if meta.Usage.CostKnown {
		t.Fatal("cost must stay unknown when the provider reports none")
	}
	if meta.Status != LLMStatusFinished || !strings.Contains(meta.RawOutput, "ok") {
		t.Fatalf("meta=%+v", meta)
	}
}

// TestThinkingIsAlignedAcrossBackends locks the contract a UI relies on: a
// thinking block becomes exactly one message whose text appears once, and its
// duration is exposed the same way whether the backend reports it (Cursor's
// thinking_duration_ms) or only streams deltas (Cline).
func TestThinkingIsAlignedAcrossBackends(t *testing.T) {
	start := time.Now()
	adapter := clineStreamAdapter{}

	// Cline: reasoning deltas plus a block-end event repeating the whole text.
	var clineEvents []LLMEvent
	for i, delta := range []string{"Let", " me", " think"} {
		ev, ok := adapter.MapEvent(clineAgentEvent(map[string]any{
			"type": "content_start", "contentType": "reasoning", "reasoning": delta,
		}), start)
		if !ok {
			t.Fatal("cline delta dropped")
		}
		ev.Seq = i
		ev.CreatedAt = start.Add(time.Duration(i) * 400 * time.Millisecond)
		clineEvents = append(clineEvents, ev)
	}
	end, ok := adapter.MapEvent(clineAgentEvent(map[string]any{
		"type": "content_end", "contentType": "reasoning", "reasoning": "Let me think",
	}), start)
	if !ok {
		t.Fatal("cline block end dropped")
	}
	end.Seq = 3
	end.CreatedAt = start.Add(1200 * time.Millisecond)
	clineEvents = append(clineEvents, end)

	msgs := AggregateChatMessages(clineEvents)
	if len(msgs) != 1 {
		t.Fatalf("cline thinking messages=%d want 1: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "Let me think" {
		t.Fatalf("cline thinking text=%q want the block exactly once", msgs[0].Content)
	}
	if msgs[0].NormalizedContent != `{"duration_ms":1200}` {
		t.Fatalf("cline thinking normalized=%q want the derived 1200ms span", msgs[0].NormalizedContent)
	}

	// Cursor: one thinking message per block, duration reported by the provider.
	cursorMsgs := AggregateChatMessages([]LLMEvent{{
		Seq: 0, Channel: LLMChannelThought, EventType: "thinking", TextDelta: "hmm", CreatedAt: start,
		Payload: map[string]any{"text": "hmm", "thinking_duration_ms": float64(3456)},
	}})
	if len(cursorMsgs) != 1 || cursorMsgs[0].Content != "hmm" {
		t.Fatalf("cursor thinking messages=%+v", cursorMsgs)
	}
	if cursorMsgs[0].NormalizedContent != `{"duration_ms":3456}` {
		t.Fatalf("cursor thinking normalized=%q want the reported 3456ms", cursorMsgs[0].NormalizedContent)
	}
}
