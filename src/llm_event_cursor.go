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
// true) because callers want the full raw stream.
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
		EventType:   typ,
		Payload:     ev.Payload,
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
