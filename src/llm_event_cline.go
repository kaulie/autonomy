package autonomy

import (
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// clineStreamAdapter maps the Cline SDK bridge run stream onto neutral LLMEvents.
// It is the only place that knows Cline's payload shapes, so the trace and store
// layers stay backend-agnostic (see docs/cline-reasoner.md).
type clineStreamAdapter struct{}

func (clineStreamAdapter) Provider() LLMProvider { return LLMProviderCline }

func init() { RegisterLLMStreamAdapter(clineStreamAdapter{}) }

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
func (clineStreamAdapter) MapEvent(native any, _ time.Time) (LLMEvent, bool) {
	ev, ok := native.(clinesdk.RunEvent)
	if !ok {
		return LLMEvent{}, false
	}
	switch ev.Type {
	case clineEventAgentEvent:
		return mapClineAgentEvent(ev)
	case clineEventChunk:
		return mapClineChunk(ev)
	case clineEventStatus:
		return mapClineLifecycle(ev, LLMChannelStatus, clineEventStatus)
	case clineEventSessionSnapshot:
		return mapClineLifecycle(ev, LLMChannelMeta, clineEventSessionSnapshot)
	case clineEventEnded:
		return mapClineLifecycle(ev, LLMChannelResult, clineEventEnded)
	default:
		if strings.TrimSpace(ev.Type) == "" {
			return LLMEvent{}, false
		}
		return mapClineLifecycle(ev, LLMChannelMeta, ev.Type)
	}
}

// mapClineAgentEvent handles the inner agent event (payload.event).
func mapClineAgentEvent(ev clinesdk.RunEvent) (LLMEvent, bool) {
	inner, ok := ev.Payload["event"].(map[string]any)
	if !ok {
		return mapClineLifecycle(ev, LLMChannelMeta, clineEventAgentEvent)
	}
	innerType := payloadString(inner, "type")
	contentType := payloadString(inner, "contentType")
	mapped := LLMEvent{
		Channel:   clineAgentChannel(innerType, contentType),
		EventType: clineAgentEventType(innerType, contentType),
		Role:      "assistant",
		Payload:   inner,
	}
	if innerType == "usage" {
		mapped.Role = "system"
	}
	if callID := payloadString(inner, "toolCallId"); callID != "" {
		// The aggregation layer groups tool events by call_id and reads
		// args/result, so expose them under the neutral keys as well.
		mapped.Payload = annotateClineTool(inner, callID)
		mapped.Role = "tool"
		mapped.Name = payloadString(inner, "toolName")
	}
	switch contentType {
	case "text":
		mapped.TextDelta = firstNonEmptyString(payloadString(inner, "text"), payloadString(inner, "accumulated"))
	case "reasoning":
		// Thinking text arrives as content_start deltas. content_end repeats the
		// whole block (verified against a real stream: joining the deltas equals
		// the block text), so emitting it as a delta again would duplicate the
		// aggregated thinking message. The payload still carries the full text.
		if innerType != "content_end" {
			mapped.TextDelta = payloadString(inner, "reasoning")
		}
	}
	if mapped.Name == "" {
		mapped.Name = firstNonEmptyString(payloadString(inner, "name"), payloadString(inner, "tool"))
	}
	return mapped, true
}

// clineAgentChannel classifies one inner agent event.
func clineAgentChannel(innerType, contentType string) LLMEventChannel {
	switch contentType {
	case "text":
		return LLMChannelAssistant
	case "reasoning", "thinking":
		return LLMChannelThought
	case "tool":
		return LLMChannelTool
	}
	switch innerType {
	case "usage":
		return LLMChannelMeta
	case "done":
		return LLMChannelResult
	case "error":
		return LLMChannelError
	case "notice":
		return LLMChannelStatus
	case "content_start", "content_update", "content_end":
		return LLMChannelAssistant
	default:
		return LLMChannelMeta
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
	out := make(map[string]any, len(inner)+4)
	for k, v := range inner {
		out[k] = v
	}
	out["call_id"] = callID
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
		out["result"] = output
	}
	return out
}

// mapClineChunk handles the raw chunk stream. Streams other than "agent" carry
// process output (stdout/stderr) and are useful; the "agent" stream is a verbatim
// JSON echo of events already recorded as agent_event, so it is dropped to keep
// llm_events free of duplicates.
func mapClineChunk(ev clinesdk.RunEvent) (LLMEvent, bool) {
	if stream := payloadString(ev.Payload, "stream"); stream == "" || stream == "agent" {
		return LLMEvent{}, false
	}
	return LLMEvent{
		Channel:   LLMChannelTool,
		EventType: clineEventChunk + ":" + payloadString(ev.Payload, "stream"),
		Role:      "tool",
		Payload:   ev.Payload,
		TextDelta: payloadString(ev.Payload, "chunk"),
	}, true
}

func mapClineLifecycle(ev clinesdk.RunEvent, channel LLMEventChannel, eventType string) (LLMEvent, bool) {
	return LLMEvent{
		Channel:   channel,
		EventType: eventType,
		Role:      "system",
		Payload:   ev.Payload,
	}, true
}

// clineRunResultToLLMRun captures run-level metadata (status, usage, timing)
// from a finished Cline run. Cline reports cost in USD and folds cache reads into
// the input tokens; both are preserved as reported.
func clineRunResultToLLMRun(res clinesdk.RunResult, startedAt time.Time) LLMRunResult {
	status := LLMStatus(strings.ToLower(strings.TrimSpace(res.Status)))
	switch status {
	case LLMStatusRunning, LLMStatusFinished, LLMStatusError, LLMStatusCancelled, LLMStatusExpired:
	default:
		status = LLMStatusError
	}
	usage := LLMUsage{
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
	return LLMRunResult{
		ProviderRunID: firstNonEmptyString(res.SessionID, res.AgentID),
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
