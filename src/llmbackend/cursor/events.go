package cursor

import (
	"github.com/kaulie/autonomy/src/llmbackend"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// cursorStreamAdapter maps the llmbackend.Cursor SDK bridge run stream onto neutral
// LLMEvents. It is the only place that knows llmbackend.Cursor's payload shapes, so other
// backends can be added without touching the trace or store layers.
type cursorStreamAdapter struct{}

func (cursorStreamAdapter) Provider() llmbackend.Provider { return llmbackend.ProviderCursor }

// llmbackend.MapEvent converts one llmbackend.Cursor RunEvent. llmbackend.Cursor events are always kept (ok is
// true) because callers want the full raw stream. The payload is copied with the
// neutral keys added (call_id/args/result/text/duration_ms/usage), so a consumer
// sees the same shape as the llmbackend.Cline backend.
func (cursorStreamAdapter) MapEvent(native any, _ time.Time) (llmbackend.Event, bool) {
	ev, ok := native.(cursorsdk.RunEvent)
	if !ok {
		return llmbackend.Event{}, false
	}
	typ := strings.TrimSpace(ev.Type)
	ch := llmbackend.ClassifyLLMChannel(typ)
	mapped := llmbackend.Event{
		OffsetToken: ev.Offset,
		Channel:     ch,
		Kind:        cursorEventKind(typ, ev.Payload),
		EventType:   typ,
		Payload:     cursorNeutralPayload(typ, ev.Payload),
	}
	mapped.Role = llmbackend.FirstNonEmptyString(
		llmbackend.PayloadString(ev.Payload, "role"),
		defaultRoleForChannel(ch),
	)
	mapped.Name = llmbackend.FirstNonEmptyString(
		llmbackend.PayloadString(ev.Payload, "name"),
		llmbackend.PayloadString(ev.Payload, "tool"),
		llmbackend.PayloadString(ev.Payload, "tool_name"),
	)
	mapped.TextDelta = cursorEventText(ev.Payload)
	return mapped, true
}

// cursorEventKind classifies one native llmbackend.Cursor message. llmbackend.Cursor reports thinking
// and assistant text as whole blocks (no deltas) and tool calls as
// running/completed messages sharing a call_id.
func cursorEventKind(typ string, payload map[string]any) llmbackend.EventKind {
	switch strings.ToLower(typ) {
	case "assistant", "assistant_message":
		return llmbackend.KindAssistant
	case "thinking", "thought", "reasoning":
		return llmbackend.KindThought
	case "tool_call", "tool_use":
		if strings.EqualFold(llmbackend.PayloadString(payload, "status"), "running") {
			return llmbackend.KindToolCallStarted
		}
		return llmbackend.KindToolCallCompleted
	case "status":
		return llmbackend.KindStatus
	case "usage":
		return llmbackend.KindUsage
	case "result", "done", "task":
		return llmbackend.KindRunResult
	case "error", "run_error":
		return llmbackend.KindError
	}
	switch ch := llmbackend.ClassifyLLMChannel(typ); ch {
	case llmbackend.ChannelAssistant:
		return llmbackend.KindAssistantDelta
	case llmbackend.ChannelThought:
		return llmbackend.KindThought
	case llmbackend.ChannelTool:
		return llmbackend.KindToolCallDelta
	case llmbackend.ChannelStatus:
		return llmbackend.KindStatus
	case llmbackend.ChannelResult:
		return llmbackend.KindRunResult
	case llmbackend.ChannelError:
		return llmbackend.KindError
	default:
		return llmbackend.KindMeta
	}
}

// cursorNeutralPayload adds the neutral keys to llmbackend.Cursor's payload.
func cursorNeutralPayload(typ string, payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return payload
	}
	switch strings.ToLower(typ) {
	case "assistant", "assistant_message":
		if text := cursorEventText(payload); text != "" {
			return llmbackend.WithNeutralText(payload, text)
		}
		return payload
	case "thinking", "thought", "reasoning":
		// llmbackend.Cursor reports the thinking duration; expose it under the neutral key.
		out := llmbackend.WithNeutralText(payload, llmbackend.PayloadString(payload, "text"))
		if ms, ok := llmbackend.PayloadNumber(payload, "thinking_duration_ms", "thinkingDurationMs", "duration_ms"); ok {
			out = llmbackend.WithNeutralKV(out, llmbackend.KeyDurationMS, int64(ms))
		}
		return out
	case "tool_call", "tool_use":
		status := normalizeToolStatus(llmbackend.PayloadString(payload, "status"))
		out := llmbackend.WithNeutralKV(payload,
			llmbackend.KeyCallID, llmbackend.FirstNonEmptyString(llmbackend.PayloadString(payload, "call_id"), llmbackend.PayloadString(payload, "callId"), llmbackend.PayloadString(payload, "id")),
			llmbackend.KeyName, llmbackend.PayloadString(payload, "name"),
			llmbackend.KeyStatus, status,
		)
		return llmbackend.WithNeutralKV(out,
			llmbackend.KeyArgs, payload["args"],
			llmbackend.KeyResult, payload["result"],
			llmbackend.KeyDurationMS, payloadInt(payload, "duration_ms", "durationMs"),
		)
	case "usage":
		return llmbackend.WithUsageKeys(payload, llmbackend.PayloadMap(payload, "usage"))
	}
	return payload
}

// payloadInt reads an integer payload value.
func payloadInt(payload map[string]any, keys ...string) any {
	if v, ok := llmbackend.PayloadNumber(payload, keys...); ok {
		return int64(v)
	}
	return nil
}

// cursorRunResultToLLMRun captures run-level metadata (status, usage, timing)
// from a finished llmbackend.Cursor run.
func cursorRunResultToLLMRun(res cursorsdk.RunResult, startedAt time.Time) llmbackend.RunResult {
	status := llmbackend.LLMStatus(strings.ToLower(strings.TrimSpace(res.Status)))
	switch status {
	case llmbackend.StatusRunning, llmbackend.StatusFinished, llmbackend.StatusError, llmbackend.StatusCancelled, llmbackend.StatusExpired:
	default:
		status = llmbackend.StatusError
	}
	return llmbackend.RunResult{
		ProviderRunID: res.RunID,
		LLMAgentID:    res.AgentID,
		Status:        status,
		ErrorCode:     res.ErrorCode,
		ErrorMessage:  res.ErrorMessage,
		DurationMS:    res.DurationMS,
		StartedAt:     startedAt,
		EndedAt:       time.Now(),
		Usage: llmbackend.Usage{
			InputTokens:      res.Usage.InputTokens,
			OutputTokens:     res.Usage.OutputTokens,
			CacheReadTokens:  res.Usage.CacheReadTokens,
			CacheWriteTokens: res.Usage.CacheWriteTokens,
			ReasoningTokens:  res.Usage.ReasoningTokens,
			TotalTokens:      res.Usage.TotalTokens,
		},
	}
}

func defaultRoleForChannel(ch llmbackend.EventChannel) string {
	switch ch {
	case llmbackend.ChannelAssistant, llmbackend.ChannelThought:
		return "assistant"
	case llmbackend.ChannelTool:
		return "tool"
	case llmbackend.ChannelStatus, llmbackend.ChannelResult, llmbackend.ChannelError, llmbackend.ChannelMeta:
		return "system"
	default:
		return ""
	}
}

// cursorEventText extracts incremental assistant text from a llmbackend.Cursor payload.
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
