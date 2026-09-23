package llmbackend

import (
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// CursorStreamAdapter maps the Cursor SDK bridge run stream onto neutral
// LLMEvents. It is the only place that knows Cursor's payload shapes, so other
// backends can be added without touching the trace or store layers.
type CursorStreamAdapter struct{}

func (CursorStreamAdapter) Provider() Provider { return ProviderCursor }

// MapEvent converts one Cursor RunEvent. Cursor events are always kept (ok is
// true) because callers want the full raw stream. The payload is copied with the
// neutral keys added (call_id/args/result/text/duration_ms/usage), so a consumer
// sees the same shape as the Cline backend.
func (CursorStreamAdapter) MapEvent(native any, _ time.Time) (Event, bool) {
	ev, ok := native.(cursorsdk.RunEvent)
	if !ok {
		return Event{}, false
	}
	typ := strings.TrimSpace(ev.Type)
	ch := classifyLLMChannel(typ)
	mapped := Event{
		OffsetToken: ev.Offset,
		Channel:     ch,
		Kind:        cursorEventKind(typ, ev.Payload),
		EventType:   typ,
		Payload:     cursorNeutralPayload(typ, ev.Payload),
	}
	mapped.Role = FirstNonEmptyString(
		PayloadString(ev.Payload, "role"),
		defaultRoleForChannel(ch),
	)
	mapped.Name = FirstNonEmptyString(
		PayloadString(ev.Payload, "name"),
		PayloadString(ev.Payload, "tool"),
		PayloadString(ev.Payload, "tool_name"),
	)
	mapped.TextDelta = cursorEventText(ev.Payload)
	return mapped, true
}

// cursorEventKind classifies one native Cursor message. Cursor reports thinking
// and assistant text as whole blocks (no deltas) and tool calls as
// running/completed messages sharing a call_id.
func cursorEventKind(typ string, payload map[string]any) EventKind {
	switch strings.ToLower(typ) {
	case "assistant", "assistant_message":
		return KindAssistant
	case "thinking", "thought", "reasoning":
		return KindThought
	case "tool_call", "tool_use":
		if strings.EqualFold(PayloadString(payload, "status"), "running") {
			return KindToolCallStarted
		}
		return KindToolCallCompleted
	case "status":
		return KindStatus
	case "usage":
		return KindUsage
	case "result", "done", "task":
		return KindRunResult
	case "error", "run_error":
		return KindError
	}
	switch ch := classifyLLMChannel(typ); ch {
	case ChannelAssistant:
		return KindAssistantDelta
	case ChannelThought:
		return KindThought
	case ChannelTool:
		return KindToolCallDelta
	case ChannelStatus:
		return KindStatus
	case ChannelResult:
		return KindRunResult
	case ChannelError:
		return KindError
	default:
		return KindMeta
	}
}

// cursorNeutralPayload adds the neutral keys to Cursor's payload.
func cursorNeutralPayload(typ string, payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return payload
	}
	switch strings.ToLower(typ) {
	case "assistant", "assistant_message":
		if text := cursorEventText(payload); text != "" {
			return withNeutralText(payload, text)
		}
		return payload
	case "thinking", "thought", "reasoning":
		// Cursor reports the thinking duration; expose it under the neutral key.
		out := withNeutralText(payload, PayloadString(payload, "text"))
		if ms, ok := payloadNumber(payload, "thinking_duration_ms", "thinkingDurationMs", "duration_ms"); ok {
			out = withNeutralKV(out, KeyDurationMS, int64(ms))
		}
		return out
	case "tool_call", "tool_use":
		status := normalizeToolStatus(PayloadString(payload, "status"))
		out := withNeutralKV(payload,
			KeyCallID, FirstNonEmptyString(PayloadString(payload, "call_id"), PayloadString(payload, "callId"), PayloadString(payload, "id")),
			KeyName, PayloadString(payload, "name"),
			KeyStatus, status,
		)
		return withNeutralKV(out,
			KeyArgs, payload["args"],
			KeyResult, payload["result"],
			KeyDurationMS, payloadInt(payload, "duration_ms", "durationMs"),
		)
	case "usage":
		return withUsageKeys(payload, PayloadMap(payload, "usage"))
	}
	return payload
}

// withUsageKeys copies the provider's usage numbers into the neutral keys,
// looking at both the nested usage object and the top-level payload.
func withUsageKeys(payload map[string]any, usage map[string]any) map[string]any {
	get := func(keys ...string) (any, bool) {
		if v, ok := payloadNumber(usage, keys...); ok {
			return int64(v), true
		}
		if v, ok := payloadNumber(payload, keys...); ok {
			return int64(v), true
		}
		return nil, false
	}
	out := payload
	add := func(key string, keys ...string) {
		if v, ok := get(keys...); ok {
			out = withNeutralKV(out, key, v)
		}
	}
	add(KeyInputTokens, "input_tokens", "inputTokens", "total_input_tokens", "totalInputTokens")
	add(KeyOutputTokens, "output_tokens", "outputTokens", "total_output_tokens", "totalOutputTokens")
	add(KeyCacheReadTokens, "cache_read_tokens", "cacheReadTokens", "total_cache_read_tokens", "totalCacheReadTokens")
	add(KeyCacheWriteTokens, "cache_write_tokens", "cacheWriteTokens", "total_cache_write_tokens", "totalCacheWriteTokens")
	add(KeyTotalTokens, "total_tokens", "totalTokens")
	// Cost is deliberately not normalized here: the unit depends on the provider
	// (Cline reports USD, Cursor's stream does not report cost at all), so each
	// adapter fills KeyCostUSD when it knows the unit.
	return out
}

// normalizeToolStatus maps a provider's tool status onto running/completed/failed.
func normalizeToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "started", "in_progress", "pending":
		return "running"
	case "", "completed", "success", "succeeded", "finished", "done":
		return "completed"
	case "error", "failed", "failure", "cancelled", "canceled", "aborted":
		return "failed"
	default:
		return "completed"
	}
}

// payloadInt reads an integer payload value.
func payloadInt(payload map[string]any, keys ...string) any {
	if v, ok := payloadNumber(payload, keys...); ok {
		return int64(v)
	}
	return nil
}

// CursorRunResultToLLMRun captures run-level metadata (status, usage, timing)
// from a finished Cursor run.
func CursorRunResultToLLMRun(res cursorsdk.RunResult, startedAt time.Time) RunResult {
	status := LLMStatus(strings.ToLower(strings.TrimSpace(res.Status)))
	switch status {
	case StatusRunning, StatusFinished, StatusError, StatusCancelled, StatusExpired:
	default:
		status = StatusError
	}
	return RunResult{
		ProviderRunID: res.RunID,
		LLMAgentID:    res.AgentID,
		Status:        status,
		ErrorCode:     res.ErrorCode,
		ErrorMessage:  res.ErrorMessage,
		DurationMS:    res.DurationMS,
		StartedAt:     startedAt,
		EndedAt:       time.Now(),
		Usage: Usage{
			InputTokens:      res.Usage.InputTokens,
			OutputTokens:     res.Usage.OutputTokens,
			CacheReadTokens:  res.Usage.CacheReadTokens,
			CacheWriteTokens: res.Usage.CacheWriteTokens,
			ReasoningTokens:  res.Usage.ReasoningTokens,
			TotalTokens:      res.Usage.TotalTokens,
		},
	}
}

func defaultRoleForChannel(ch EventChannel) string {
	switch ch {
	case ChannelAssistant, ChannelThought:
		return "assistant"
	case ChannelTool:
		return "tool"
	case ChannelStatus, ChannelResult, ChannelError, ChannelMeta:
		return "system"
	default:
		return ""
	}
}

func PayloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if s, ok := payload[key].(string); ok {
		return s
	}
	return ""
}

func FirstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// cursorEventText extracts incremental assistant text from a Cursor payload.
// Shapes handled (in order): {"text": ...}, {"message": {"text"|"content": ...}},
// {"content": [...]}. Unknown shapes return "".
func cursorEventText(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	if t, ok := payload["text"].(string); ok && t != "" {
		return t
	}
	if msg, ok := payload["message"].(map[string]any); ok {
		if t := mapText(msg); t != "" {
			return t
		}
	}
	return mapText(payload)
}

func mapText(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	if t, ok := m["text"].(string); ok && t != "" {
		return t
	}
	return contentText(m["content"])
}

func contentText(content any) string {
	items, ok := content.([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if typ, _ := m["type"].(string); typ == "" || typ == "text" {
			if t, ok := m["text"].(string); ok {
				b.WriteString(t)
			}
		}
	}
	return b.String()
}

func init() {
	RegisterAdapter(CursorStreamAdapter{})
}
