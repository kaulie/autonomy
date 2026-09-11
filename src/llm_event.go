package autonomy

import (
	"encoding/json"
	"strings"
	"time"
)

// This file defines the provider-neutral LLM run-stream model. Every LLM
// backend (Cursor today; Cline / DeepSeek harness later) maps its native run
// stream into LLMEvent, so persistence, replay, and analytics stay
// backend-agnostic. Adding a backend means adding an LLMStreamAdapter — the
// trace and store layers never change.

// LLMEventChannel is a coarse, provider-neutral classification of a stream
// event. Channels are stable across providers; the provider's own
// discriminator is preserved verbatim in LLMEvent.EventType.
type LLMEventChannel string

const (
	LLMChannelAssistant LLMEventChannel = "assistant"
	LLMChannelTool      LLMEventChannel = "tool"
	LLMChannelThought   LLMEventChannel = "thought"
	LLMChannelStatus    LLMEventChannel = "status"
	LLMChannelResult    LLMEventChannel = "result"
	LLMChannelError     LLMEventChannel = "error"
	LLMChannelMeta      LLMEventChannel = "meta"
)

// LLMStatus is the lifecycle status of one LLM run.
type LLMStatus string

const (
	LLMStatusRunning   LLMStatus = "running"
	LLMStatusFinished  LLMStatus = "finished"
	LLMStatusError     LLMStatus = "error"
	LLMStatusCancelled LLMStatus = "cancelled"
	LLMStatusExpired   LLMStatus = "expired"
)

// LLMUsage is provider-neutral token usage for one run.
type LLMUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	TotalTokens      int64
	// CostCents is only meaningful when CostKnown is true.
	CostCents float64
	CostKnown bool
}

// LLMEvent is one event from an LLM run stream.
type LLMEvent struct {
	// Seq is the 0-based position within the run; the trace assigns it.
	Seq int
	// OffsetToken is the provider's opaque resume cursor, when offered.
	OffsetToken string
	// Channel is the coarse classification used for filtering.
	Channel LLMEventChannel
	// EventType is the provider's native discriminator, verbatim.
	EventType string
	// Role is the message author when known: user | assistant | tool | system.
	Role string
	// Name is a tool or step name when the event refers to one.
	Name string
	// TextDelta is the incremental text carried by the event, when any.
	TextDelta string
	// Payload is the verbatim provider payload for full-fidelity replay.
	Payload map[string]any
	// ElapsedMS is the time since the run started.
	ElapsedMS int64
	// CreatedAt is the wall-clock arrival time.
	CreatedAt time.Time
}

// PayloadJSON renders Payload as compact JSON for storage. It never fails; an
// unmarshalable payload degrades to "{}".
func (e LLMEvent) PayloadJSON() string {
	if len(e.Payload) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// LLMRunResult is the run-level metadata captured when a run finishes.
type LLMRunResult struct {
	ProviderRunID string
	LLMAgentID    string
	Status        LLMStatus
	ErrorCode     string
	ErrorMessage  string
	RawOutput     string
	DurationMS    int64
	EventCount    int
	Usage         LLMUsage
	StartedAt     time.Time
	EndedAt       time.Time
}

// LLMStreamAdapter translates one provider's native run stream into neutral
// LLMEvents. Implement it per backend; the trace/store layer depends only on
// LLMEvent, so adding a backend never touches persistence.
type LLMStreamAdapter interface {
	// Provider is the backend this adapter handles.
	Provider() LLMProvider
	// MapEvent converts one native stream event. ok=false drops the event.
	MapEvent(native any, startedAt time.Time) (LLMEvent, bool)
}

var llmStreamAdapters = map[LLMProvider]LLMStreamAdapter{}

// RegisterLLMStreamAdapter makes a backend's stream adapter available to
// LLMTrace. Backends register once (typically from bootstrap or init).
func RegisterLLMStreamAdapter(adapter LLMStreamAdapter) {
	if adapter == nil {
		return
	}
	llmStreamAdapters[adapter.Provider()] = adapter
}

func llmStreamAdapterFor(provider LLMProvider) (LLMStreamAdapter, bool) {
	a, ok := llmStreamAdapters[provider]
	return a, ok
}

// MapNativeLLMEvent maps any provider's native event for the given provider,
// reporting ok=false when no adapter is registered or the event is dropped.
// Backends' Agent wrappers use it to stay provider-agnostic themselves.
func MapNativeLLMEvent(provider LLMProvider, native any, startedAt time.Time) (LLMEvent, bool) {
	adapter, ok := llmStreamAdapterFor(provider)
	if !ok {
		return LLMEvent{}, false
	}
	return adapter.MapEvent(native, startedAt)
}

// classifyLLMChannel maps a provider event type to a neutral channel. It strips
// an optional "envelope:" prefix (e.g. "interaction_update:text_delta") and
// falls back to LLMChannelMeta for unknown types.
func classifyLLMChannel(eventType string) LLMEventChannel {
	name := strings.ToLower(strings.TrimSpace(eventType))
	if i := strings.Index(name, ":"); i >= 0 {
		name = strings.TrimSpace(name[i+1:])
	}
	switch name {
	case "assistant", "assistant_message", "assistant_delta", "text", "text_delta", "content", "delta":
		return LLMChannelAssistant
	case "tool", "tool_call", "tool_use", "tool_result", "function_call", "function_result":
		return LLMChannelTool
	case "thought", "thinking", "reasoning", "reasoning_delta":
		return LLMChannelThought
	case "status":
		return LLMChannelStatus
	case "result":
		return LLMChannelResult
	case "error", "run_error":
		return LLMChannelError
	default:
		return LLMChannelMeta
	}
}
