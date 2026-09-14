package autonomy

import (
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// cursorStreamAdapter maps the Cursor SDK bridge run stream onto neutral
// LLMEvents. It is the only place that knows Cursor's payload shapes, so other
// backends can be added without touching the trace or store layers.
type cursorStreamAdapter struct{}

func (cursorStreamAdapter) Provider() LLMProvider { return LLMProviderCursor }

// MapEvent converts one Cursor RunEvent. Cursor events are always kept (ok is
// true) because callers want the full raw stream. The payload is copied with the
// neutral keys added (call_id/args/result/text/duration_ms/usage), so a consumer
// sees the same shape as the Cline backend.
func (cursorStreamAdapter) MapEvent(native any, _ time.Time) (LLMEvent, bool) {
	ev, ok := native.(cursorsdk.RunEvent)
	if !ok {
		return LLMEvent{}, false
	}
	typ := strings.TrimSpace(ev.Type)
	ch := classifyLLMChannel(typ)
	mapped := LLMEvent{
		OffsetToken: ev.Offset,
		Channel:     ch,
		Kind:        cursorEventKind(typ, ev.Payload),
		EventType:   typ,
		Payload:     cursorNeutralPayload(typ, ev.Payload),
	}
	mapped.Role = firstNonEmptyString(
		payloadString(ev.Payload, "role"),
		defaultRoleForChannel(ch),
	)
	mapped.Name = firstNonEmptyString(
		payloadString(ev.Payload, "name"),
		payloadString(ev.Payload, "tool"),
		payloadString(ev.Payload, "tool_name"),
	)
	mapped.TextDelta = cursorEventText(ev.Payload)
	return mapped, true
}

// cursorEventKind classifies one native Cursor message. Cursor reports thinking
// and assistant text as whole blocks (no deltas) and tool calls as
// running/completed messages sharing a call_id.
func cursorEventKind(typ string, payload map[string]any) LLMEventKind {
	switch strings.ToLower(typ) {
	case "assistant", "assistant_message":
		return LLMKindAssistant
	case "thinking", "thought", "reasoning":
		return LLMKindThought
	case "tool_call", "tool_use":
		if strings.EqualFold(payloadString(payload, "status"), "running") {
			return LLMKindToolCallStarted
		}
		return LLMKindToolCallCompleted
	case "status":
		return LLMKindStatus
	case "usage":
		return LLMKindUsage
	case "result", "done", "task":
		return LLMKindRunResult
	case "error", "run_error":
		return LLMKindError
	}
	switch ch := classifyLLMChannel(typ); ch {
	case LLMChannelAssistant:
		return LLMKindAssistantDelta
	case LLMChannelThought:
		return LLMKindThought
	case LLMChannelTool:
		return LLMKindToolCallDelta
	case LLMChannelStatus:
		return LLMKindStatus
	case LLMChannelResult:
		return LLMKindRunResult
	case LLMChannelError:
		return LLMKindError
	default:
		return LLMKindMeta
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
		out := withNeutralText(payload, payloadString(payload, "text"))
		if ms, ok := payloadNumber(payload, "thinking_duration_ms", "thinkingDurationMs", "duration_ms"); ok {
			out = withNeutralKV(out, LLMKeyDurationMS, int64(ms))
		}
		return out
	case "tool_call", "tool_use":
		status := normalizeToolStatus(payloadString(payload, "status"))
		out := withNeutralKV(payload,
			LLMKeyCallID, firstNonEmptyString(payloadString(payload, "call_id"), payloadString(payload, "callId"), payloadString(payload, "id")),
			LLMKeyName, payloadString(payload, "name"),
			LLMKeyStatus, status,
		)
		return withNeutralKV(out,
			LLMKeyArgs, payload["args"],
			LLMKeyResult, payload["result"],
			LLMKeyDurationMS, payloadInt(payload, "duration_ms", "durationMs"),
		)
	case "usage":
		return withUsageKeys(payload, payloadMap(payload, "usage"))
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
	add(LLMKeyInputTokens, "input_tokens", "inputTokens", "total_input_tokens", "totalInputTokens")
	add(LLMKeyOutputTokens, "output_tokens", "outputTokens", "total_output_tokens", "totalOutputTokens")
	add(LLMKeyCacheReadTokens, "cache_read_tokens", "cacheReadTokens", "total_cache_read_tokens", "totalCacheReadTokens")
	add(LLMKeyCacheWriteTokens, "cache_write_tokens", "cacheWriteTokens", "total_cache_write_tokens", "totalCacheWriteTokens")
	add(LLMKeyTotalTokens, "total_tokens", "totalTokens")
	// Cost is deliberately not normalized here: the unit depends on the provider
	// (Cline reports USD, Cursor's stream does not report cost at all), so each
	// adapter fills LLMKeyCostUSD when it knows the unit.
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

// cursorRunResultToLLMRun captures run-level metadata (status, usage, timing)
// from a finished Cursor run.
func cursorRunResultToLLMRun(res cursorsdk.RunResult, startedAt time.Time) LLMRunResult {
	status := LLMStatus(strings.ToLower(strings.TrimSpace(res.Status)))
	switch status {
	case LLMStatusRunning, LLMStatusFinished, LLMStatusError, LLMStatusCancelled, LLMStatusExpired:
	default:
		status = LLMStatusError
	}
	return LLMRunResult{
		ProviderRunID: res.RunID,
		LLMAgentID:    res.AgentID,
		Status:        status,
		ErrorCode:     res.ErrorCode,
		ErrorMessage:  res.ErrorMessage,
		DurationMS:    res.DurationMS,
		StartedAt:     startedAt,
		EndedAt:       time.Now(),
		Usage: LLMUsage{
			InputTokens:      res.Usage.InputTokens,
			OutputTokens:     res.Usage.OutputTokens,
			CacheReadTokens:  res.Usage.CacheReadTokens,
			CacheWriteTokens: res.Usage.CacheWriteTokens,
			ReasoningTokens:  res.Usage.ReasoningTokens,
			TotalTokens:      res.Usage.TotalTokens,
		},
	}
}

func defaultRoleForChannel(ch LLMEventChannel) string {
	switch ch {
	case LLMChannelAssistant, LLMChannelThought:
		return "assistant"
	case LLMChannelTool:
		return "tool"
	case LLMChannelStatus, LLMChannelResult, LLMChannelError, LLMChannelMeta:
		return "system"
	default:
		return ""
	}
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if s, ok := payload[key].(string); ok {
		return s
	}
	return ""
}

func firstNonEmptyString(vals ...string) string {
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
	RegisterLLMStreamAdapter(cursorStreamAdapter{})
}
