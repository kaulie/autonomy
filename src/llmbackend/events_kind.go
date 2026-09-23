package llmbackend

// EventKind is the provider-neutral, fine-grained classification of one
// event: consumers (UI, analytics) switch on Kind and read the neutral payload
// keys below, so cursor / cline / future providers look the same. Channel stays
// the coarse bucket, and the provider's own discriminator is still kept verbatim
// in EventType for full fidelity.
type EventKind string

const (
	// Assistant text: deltas while streaming, a whole block when the provider
	// returns one message (Cursor).
	KindAssistantDelta EventKind = "assistant_delta"
	KindAssistant      EventKind = "assistant"
	// Thinking: deltas while streaming (Cline), a whole block when reported as one
	// message (Cursor), and a terminator where a streamed block ends.
	KindThoughtDelta EventKind = "thought_delta"
	KindThought      EventKind = "thought"
	KindThoughtEnd   EventKind = "thought_end"
	// Tool calls: started -> optional output deltas -> completed.
	KindToolCallStarted   EventKind = "tool_call_started"
	KindToolCallDelta     EventKind = "tool_call_delta"
	KindToolCallCompleted EventKind = "tool_call_completed"
	// Run-level events.
	KindStatus    EventKind = "status"
	KindUsage     EventKind = "usage"
	KindRunResult EventKind = "run_result"
	KindError     EventKind = "error"
	KindMeta      EventKind = "meta"
)

// Neutral payload keys. Adapters copy the provider's own payload and add these
// next to it (provider keys are never renamed or dropped), so a consumer never
// has to know that Cursor says call_id/args/thinking_duration_ms while Cline
// says toolCallId/input/durationMs.
const (
	// KeyText is the event's own text: an increment for *_delta kinds, the
	// whole block for assistant/thought.
	KeyText = "text"
	// KeyDurationMS is how long the event took (a thinking block, a tool call).
	KeyDurationMS = "duration_ms"
	// Tool call identity and payload.
	KeyCallID = "call_id"
	KeyName   = "name"
	KeyArgs   = "args"
	KeyResult = "result"
	// KeyStream / KeyChunk describe one chunk of streamed tool output
	// (stdout/stderr).
	KeyStream = "stream"
	KeyChunk  = "chunk"
	// Run status; for tool events the same key holds running | completed | failed.
	KeyStatus  = "status"
	KeyMessage = "message"
	// Token usage and cost (USD), so usage reads the same from every provider.
	KeyInputTokens      = "input_tokens"
	KeyOutputTokens     = "output_tokens"
	KeyCacheReadTokens  = "cache_read_tokens"
	KeyCacheWriteTokens = "cache_write_tokens"
	KeyTotalTokens      = "total_tokens"
	KeyCostUSD          = "cost_usd"
)

// Family groups the granularity variants of one semantic, so a consumer that
// does not care about streaming granularity can switch on Family alone:
//
//	assistant  <- assistant, assistant_delta
//	thought    <- thought, thought_delta, thought_end
//	tool_call  <- tool_call_started, tool_call_delta, tool_call_completed
//
// The granularity difference is a provider capability, not a semantic one:
// Cursor reports thinking/assistant as whole blocks, Cline streams them as
// deltas (with a block-end marker).
func (k EventKind) Family() string {
	switch k {
	case KindAssistant, KindAssistantDelta:
		return "assistant"
	case KindThought, KindThoughtDelta, KindThoughtEnd:
		return "thought"
	case KindToolCallStarted, KindToolCallDelta, KindToolCallCompleted:
		return "tool_call"
	default:
		return string(k)
	}
}

// neutralPayload copies a provider payload so neutral keys can be added without
// touching the provider's own fields.
func neutralPayload(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload)+8)
	for k, v := range payload {
		out[k] = v
	}
	return out
}

// withNeutralText adds the neutral text key and, when non-empty, reports it.
func withNeutralText(payload map[string]any, text string) map[string]any {
	if text == "" {
		return payload
	}
	out := neutralPayload(payload)
	out[KeyText] = text
	return out
}

// withNeutralKV copies payload and adds the given neutral keys (skipping empties
// and nils), which keeps adapters free of bookkeeping.
func withNeutralKV(payload map[string]any, kv ...any) map[string]any {
	out := neutralPayload(payload)
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		if key == "" {
			continue
		}
		switch v := kv[i+1].(type) {
		case nil:
			continue
		case string:
			if v == "" {
				continue
			}
		}
		out[key] = kv[i+1]
	}
	return out
}

// payloadNumber reads the first present numeric payload value among keys
// (JSON numbers decode as float64; ints are accepted for hand-built payloads).
func payloadNumber(payload map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch v := payload[key].(type) {
		case float64:
			return v, true
		case int64:
			return float64(v), true
		case int:
			return float64(v), true
		}
	}
	return 0, false
}

// PayloadMap returns a nested payload object when present.
func PayloadMap(payload map[string]any, key string) map[string]any {
	m, _ := payload[key].(map[string]any)
	return m
}
