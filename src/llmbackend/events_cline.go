package llmbackend

import (
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// ClineStreamAdapter maps the Cline SDK bridge run stream onto neutral LLMEvents.
// It is the only place that knows Cline's payload shapes, so the trace and store
// layers stay backend-agnostic (see docs/cline-reasoner.md).
type ClineStreamAdapter struct{}

func (ClineStreamAdapter) Provider() Provider { return ProviderCline }

func init() { RegisterAdapter(ClineStreamAdapter{}) }

// Cline core event types forwarded by the bridge (kept verbatim in EventType).
const (
	clineEventAgentEvent      = "agent_event"
	clineEventChunk           = "chunk"
	clineEventStatus          = "status"
	clineEventSessionSnapshot = "session_snapshot"
	clineEventEnded           = "ended"
)

// MapEvent converts one native bridge event. Agent events carry the model's
// stream (text deltas, reasoning, tool calls and their output); the other core
// events carry lifecycle/status metadata. Everything is kept for fidelity, and
// tool payloads are annotated with the neutral keys the aggregation layer reads
// (call_id/args/result).
func (ClineStreamAdapter) MapEvent(native any, _ time.Time) (Event, bool) {
	ev, ok := native.(clinesdk.RunEvent)
	if !ok {
		return Event{}, false
	}
	switch ev.Type {
	case clineEventAgentEvent:
		return mapClineAgentEvent(ev)
	case clineEventChunk:
		return mapClineChunk(ev)
	case clineEventStatus:
		return mapClineLifecycle(ev, ChannelStatus, KindStatus, clineEventStatus)
	case clineEventSessionSnapshot:
		return mapClineLifecycle(ev, ChannelMeta, KindMeta, clineEventSessionSnapshot)
	case clineEventEnded:
		return mapClineLifecycle(ev, ChannelResult, KindRunResult, clineEventEnded)
	default:
		if strings.TrimSpace(ev.Type) == "" {
			return Event{}, false
		}
		return mapClineLifecycle(ev, ChannelMeta, KindMeta, ev.Type)
	}
}

// mapClineAgentEvent handles the inner agent event (payload.event).
func mapClineAgentEvent(ev clinesdk.RunEvent) (Event, bool) {
	inner, ok := ev.Payload["event"].(map[string]any)
	if !ok {
		return mapClineLifecycle(ev, ChannelMeta, KindMeta, clineEventAgentEvent)
	}
	innerType := PayloadString(inner, "type")
	contentType := PayloadString(inner, "contentType")
	mapped := Event{
		Channel:   clineAgentChannel(innerType, contentType),
		Kind:      clineAgentKind(innerType, contentType),
		EventType: clineAgentEventType(innerType, contentType),
		Role:      "assistant",
		Payload:   inner,
	}
	if innerType == "usage" {
		mapped.Role = "system"
		mapped.Payload = withUsageKeys(inner, PayloadMap(inner, "usage"))
		if cost, ok := payloadNumber(inner, "totalCost", "cost", "total_cost"); ok {
			mapped.Payload = withNeutralKV(mapped.Payload, KeyCostUSD, cost) // Cline reports USD
		}
	}
	if innerType == "notice" {
		mapped.Payload = withNeutralKV(inner, KeyStatus, PayloadString(inner, "noticeType"), KeyMessage, PayloadString(inner, "message"))
	}
	if callID := PayloadString(inner, "toolCallId"); callID != "" {
		// The aggregation layer groups tool events by call_id and reads
		// args/result, so expose them under the neutral keys as well.
		mapped.Payload = annotateClineTool(inner, callID)
		mapped.Role = "tool"
		mapped.Name = PayloadString(inner, "toolName")
	}
	switch contentType {
	case "text":
		text := FirstNonEmptyString(PayloadString(inner, "text"), PayloadString(inner, "accumulated"))
		mapped.TextDelta = text
		mapped.Payload = withNeutralText(mapped.Payload, text)
	case "reasoning":
		// Thinking text arrives as content_start deltas. content_end repeats the
		// whole block (verified against a real stream: joining the deltas equals
		// the block text), so emitting it as a delta again would duplicate the
		// aggregated thinking message. The payload still carries the full text.
		// The neutral text key always carries the event's own text (the whole
		// block on content_end), while TextDelta stays empty there so the
		// aggregation does not see the block twice.
		reasoning := PayloadString(inner, "reasoning")
		mapped.Payload = withNeutralText(mapped.Payload, reasoning)
		if innerType != "content_end" {
			mapped.TextDelta = reasoning
		}
	}
	if mapped.Name == "" {
		mapped.Name = FirstNonEmptyString(PayloadString(inner, "name"), PayloadString(inner, "tool"))
	}
	return mapped, true
}

// clineAgentKind classifies one inner agent event. Cline streams text and
// thinking as deltas (with a block-end marker) and tool calls as
// start / output-update / end events sharing a toolCallId.
func clineAgentKind(innerType, contentType string) EventKind {
	switch contentType {
	case "text":
		if innerType == "content_end" {
			return KindAssistant
		}
		return KindAssistantDelta
	case "reasoning", "thinking":
		if innerType == "content_end" {
			return KindThoughtEnd
		}
		return KindThoughtDelta
	case "tool":
		switch innerType {
		case "content_start":
			return KindToolCallStarted
		case "content_update":
			return KindToolCallDelta
		case "content_end":
			return KindToolCallCompleted
		default:
			return KindToolCallDelta
		}
	}
	switch innerType {
	case "usage":
		return KindUsage
	case "done":
		return KindRunResult
	case "error":
		return KindError
	case "notice":
		return KindStatus
	case "content_start", "content_update", "content_end":
		return KindAssistantDelta
	default:
		return KindMeta
	}
}

// clineAgentChannel classifies one inner agent event.
func clineAgentChannel(innerType, contentType string) EventChannel {
	switch contentType {
	case "text":
		return ChannelAssistant
	case "reasoning", "thinking":
		return ChannelThought
	case "tool":
		return ChannelTool
	}
	switch innerType {
	case "usage":
		return ChannelMeta
	case "done":
		return ChannelResult
	case "error":
		return ChannelError
	case "notice":
		return ChannelStatus
	case "content_start", "content_update", "content_end":
		return ChannelAssistant
	default:
		return ChannelMeta
	}
}

// clineAgentEventType keeps the provider's discriminators verbatim, including
// the content type when present (e.g. "agent_event:content_end:tool").
func clineAgentEventType(innerType, contentType string) string {
	if contentType == "" {
		return clineEventAgentEvent + ":" + innerType
	}
	return clineEventAgentEvent + ":" + innerType + ":" + contentType
}

// annotateClineTool copies the native payload and adds the neutral tool keys.
func annotateClineTool(inner map[string]any, callID string) map[string]any {
	out := make(map[string]any, len(inner)+6)
	for k, v := range inner {
		out[k] = v
	}
	out[KeyCallID] = callID
	if name := PayloadString(inner, "toolName"); name != "" {
		out[KeyName] = name
	}
	// Cline signals the tool lifecycle with the event type, so normalize it into
	// the same status key Cursor uses (running while it runs, completed at end).
	if PayloadString(inner, "type") == "content_end" {
		out[KeyStatus] = "completed"
	} else {
		out[KeyStatus] = "running"
	}
	if ms, ok := payloadNumber(inner, "durationMs", "duration_ms"); ok {
		out[KeyDurationMS] = int64(ms)
	}
	if args, ok := inner["input"]; ok {
		out["args"] = args
	}
	if update, ok := inner["update"].(map[string]any); ok {
		// Streaming tool output (stdout/stderr chunks).
		if chunk, ok := update["chunk"]; ok {
			out["chunk"] = chunk
		}
		if stream, ok := update["stream"]; ok {
			out["stream"] = stream
		}
	}
	if output, ok := inner["output"]; ok {
		out[KeyResult] = output
	}
	return out
}

// mapClineChunk handles the raw chunk stream. Streams other than "agent" carry
// process output (stdout/stderr) and are useful; the "agent" stream is a verbatim
// JSON echo of events already recorded as agent_event, so it is dropped to keep
// llm_events free of duplicates.
func mapClineChunk(ev clinesdk.RunEvent) (Event, bool) {
	if stream := PayloadString(ev.Payload, "stream"); stream == "" || stream == "agent" {
		return Event{}, false
	}
	return Event{
		Channel:   ChannelTool,
		Kind:      KindToolCallDelta,
		EventType: clineEventChunk + ":" + PayloadString(ev.Payload, "stream"),
		Role:      "tool",
		Payload:   ev.Payload,
		TextDelta: PayloadString(ev.Payload, "chunk"),
	}, true
}

func mapClineLifecycle(ev clinesdk.RunEvent, channel EventChannel, kind EventKind, eventType string) (Event, bool) {
	return Event{
		Channel:   channel,
		Kind:      kind,
		EventType: eventType,
		Role:      "system",
		Payload:   ev.Payload,
	}, true
}

// clineRunResultToLLMRun captures run-level metadata (status, usage, timing)
// from a finished Cline run. Cline reports cost in USD and folds cache reads into
// the input tokens; both are preserved as reported.
func ClineRunResultToLLMRun(res clinesdk.RunResult, startedAt time.Time) RunResult {
	status := LLMStatus(strings.ToLower(strings.TrimSpace(res.Status)))
	switch status {
	case StatusRunning, StatusFinished, StatusError, StatusCancelled, StatusExpired:
	default:
		status = StatusError
	}
	usage := Usage{
		InputTokens:      res.Usage.InputTokens,
		OutputTokens:     res.Usage.OutputTokens,
		CacheReadTokens:  res.Usage.CacheReadTokens,
		CacheWriteTokens: res.Usage.CacheWriteTokens,
		TotalTokens:      res.Usage.TotalTokens,
	}
	if res.Usage.ReasoningTokens != nil {
		usage.ReasoningTokens = *res.Usage.ReasoningTokens
	}
	if res.Usage.HasCost {
		usage.CostCents = res.Usage.CostUSD * 100
		usage.CostKnown = true
	}
	ended := res.EndedAt
	if ended.IsZero() {
		ended = time.Now()
	}
	start := startedAt
	if !res.StartedAt.IsZero() {
		start = res.StartedAt
	}
	return RunResult{
		ProviderRunID: FirstNonEmptyString(res.SessionID, res.AgentID),
		LLMAgentID:    res.AgentID,
		Status:        status,
		ErrorMessage:  res.ErrorMessage,
		RawOutput:     res.Text,
		DurationMS:    res.DurationMS,
		Usage:         usage,
		StartedAt:     start,
		EndedAt:       ended,
	}
}
