package claude

import (
	"github.com/kaulie/autonomy/src/llmbackend"
	"time"
)

type streamAdapter struct{}

func (streamAdapter) Provider() llmbackend.Provider { return llmbackend.ProviderClaude }
func (streamAdapter) MapEvent(native any, _ time.Time) (llmbackend.Event, bool) {
	n, ok := native.(map[string]any)
	if !ok || llmbackend.PayloadString(n, "type") == "" {
		return llmbackend.Event{}, false
	}
	ev := llmbackend.Event{Channel: llmbackend.ChannelMeta, Kind: llmbackend.KindMeta, Role: "system", EventType: llmbackend.PayloadString(n, "type"), Payload: n}
	switch ev.EventType {
	case "text":
		ev.Channel, ev.Kind, ev.Role, ev.TextDelta = llmbackend.ChannelAssistant, llmbackend.KindAssistant, "assistant", llmbackend.PayloadString(n, "text")
	case "thinking":
		ev.Channel, ev.Kind, ev.Role, ev.TextDelta = llmbackend.ChannelThought, llmbackend.KindThoughtEnd, "assistant", llmbackend.PayloadString(n, "thinking")
	case "tool_use":
		ev.Channel, ev.Kind, ev.Role = llmbackend.ChannelTool, llmbackend.KindToolCallStarted, "tool"
		ev.Name = llmbackend.PayloadString(n, "name")
		ev.Payload = llmbackend.WithNeutralKV(n, llmbackend.KeyCallID, n["id"])
		ev.Payload = llmbackend.WithNeutralKV(ev.Payload, llmbackend.KeyArgs, n["input"])
	case "tool_result":
		ev.Channel, ev.Kind, ev.Role = llmbackend.ChannelTool, llmbackend.KindToolCallCompleted, "tool"
		ev.Payload = llmbackend.WithNeutralKV(n, llmbackend.KeyCallID, n["tool_use_id"])
		ev.Payload = llmbackend.WithNeutralKV(ev.Payload, llmbackend.KeyResult, n["content"])
		status := "completed"
		if n["is_error"] == true {
			status = "failed"
		}
		ev.Payload = llmbackend.WithNeutralKV(ev.Payload, llmbackend.KeyStatus, status)
	case "result":
		ev.Channel, ev.Kind = llmbackend.ChannelStatus, llmbackend.KindUsage
		u := usage(n)
		for key, value := range map[string]any{llmbackend.KeyInputTokens: u.InputTokens, llmbackend.KeyOutputTokens: u.OutputTokens, llmbackend.KeyCacheReadTokens: u.CacheReadTokens, llmbackend.KeyCacheWriteTokens: u.CacheWriteTokens, llmbackend.KeyTotalTokens: u.TotalTokens} {
			ev.Payload = llmbackend.WithNeutralKV(ev.Payload, key, value)
		}
		if u.CostKnown {
			ev.Payload = llmbackend.WithNeutralKV(ev.Payload, llmbackend.KeyCostUSD, u.CostCents/100)
		}
		if n["is_error"] == true || n["subtype"] != "success" {
			ev.Channel, ev.Kind = llmbackend.ChannelError, llmbackend.KindError
			ev.Payload = llmbackend.WithNeutralKV(ev.Payload, llmbackend.KeyMessage, n["subtype"])
		}
	}
	return ev, true
}

// Keep the original envelope for replay and emit every content block individually.
func events(native map[string]any) []llmbackend.Event {
	adapter := streamAdapter{}
	ev, ok := adapter.MapEvent(native, time.Time{})
	if !ok {
		return nil
	}
	out := []llmbackend.Event{ev}
	message := llmbackend.PayloadMap(native, "message")
	blocks, _ := message["content"].([]any)
	for _, block := range blocks {
		if ev, ok := adapter.MapEvent(block, time.Time{}); ok {
			out = append(out, ev)
		}
	}
	return out
}
