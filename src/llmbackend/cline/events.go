package cline

import (
	"github.com/kaulie/autonomy/src/llmbackend"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// ClineStreamAdapter maps the llmbackend.Cline SDK bridge run stream onto neutral LLMEvents.
// It is the only place that knows llmbackend.Cline's payload shapes, so the trace and store
// layers stay backend-agnostic (see docs/cline-reasoner.md).
type ClineStreamAdapter struct{}

func (ClineStreamAdapter) Provider() llmbackend.Provider { return llmbackend.ProviderCline }

// llmbackend.Cline core event types forwarded by the bridge (kept verbatim in EventType).
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
func (ClineStreamAdapter) MapEvent(native any, _ time.Time) (llmbackend.Event, bool) {
	ev, ok := native.(clinesdk.RunEvent)
	if !ok {
		return llmbackend.Event{}, false
	}
	switch ev.Type {
	case clineEventAgentEvent:
		return mapClineAgentEvent(ev)
	case clineEventChunk:
		return mapClineChunk(ev)
	case clineEventStatus:
		return mapClineLifecycle(ev, llmbackend.ChannelStatus, llmbackend.KindStatus, clineEventStatus)
	case clineEventSessionSnapshot:
		return mapClineLifecycle(ev, llmbackend.ChannelMeta, llmbackend.KindMeta, clineEventSessionSnapshot)
	case clineEventEnded:
		return mapClineLifecycle(ev, llmbackend.ChannelResult, llmbackend.KindRunResult, clineEventEnded)
	default:
		if strings.TrimSpace(ev.Type) == "" {
			return llmbackend.Event{}, false
		}
		return mapClineLifecycle(ev, llmbackend.ChannelMeta, llmbackend.KindMeta, ev.Type)
	}
}

// mapClineAgentEvent handles the inner agent event (payload.event).
func mapClineAgentEvent(ev clinesdk.RunEvent) (llmbackend.Event, bool) {
	inner, ok := ev.Payload["event"].(map[string]any)
	if !ok {
		return mapClineLifecycle(ev, llmbackend.ChannelMeta, llmbackend.KindMeta, clineEventAgentEvent)
	}
	innerType := llmbackend.PayloadString(inner, "type")
	contentType := llmbackend.PayloadString(inner, "contentType")
	mapped := llmbackend.Event{
		Channel:   clineAgentChannel(innerType, contentType),
		Kind:      clineAgentKind(innerType, contentType),
		EventType: clineAgentEventType(innerType, contentType),
		Role:      "assistant",
		Payload:   inner,
	}
	if innerType == "usage" {
		mapped.Role = "system"
		mapped.Payload = llmbackend.WithUsageKeys(inner, llmbackend.PayloadMap(inner, "usage"))
		if cost, ok := llmbackend.PayloadNumber(inner, "totalCost", "cost", "total_cost"); ok {
			mapped.Payload = llmbackend.WithNeutralKV(mapped.Payload, llmbackend.KeyCostUSD, cost) // llmbackend.Cline reports USD
		}
	}
	if innerType == "notice" {
		mapped.Payload = llmbackend.WithNeutralKV(inner, llmbackend.KeyStatus, llmbackend.PayloadString(inner, "noticeType"), llmbackend.KeyMessage, llmbackend.PayloadString(inner, "message"))
	}
	if callID := llmbackend.PayloadString(inner, "toolCallId"); callID != "" {
		// The aggregation layer groups tool events by call_id and reads
		// args/result, so expose them under the neutral keys as well.
		mapped.Payload = annotateClineTool(inner, callID)
		mapped.Role = "tool"
		mapped.Name = llmbackend.PayloadString(inner, "toolName")
	}
	switch contentType {
	case "text":
		text := llmbackend.FirstNonEmptyString(llmbackend.PayloadString(inner, "text"), llmbackend.PayloadString(inner, "accumulated"))
		mapped.TextDelta = text
		mapped.Payload = llmbackend.WithNeutralText(mapped.Payload, text)
	case "reasoning":
		// Thinking text arrives as content_start deltas. content_end repeats the
		// whole block (verified against a real stream: joining the deltas equals
		// the block text), so emitting it as a delta again would duplicate the
		// aggregated thinking message. The payload still carries the full text.
		// The neutral text key always carries the event's own text (the whole
		// block on content_end), while TextDelta stays empty there so the
		// aggregation does not see the block twice.
		reasoning := llmbackend.PayloadString(inner, "reasoning")
		mapped.Payload = llmbackend.WithNeutralText(mapped.Payload, reasoning)
		if innerType != "content_end" {
			mapped.TextDelta = reasoning
		}
	}
	if mapped.Name == "" {
		mapped.Name = llmbackend.FirstNonEmptyString(llmbackend.PayloadString(inner, "name"), llmbackend.PayloadString(inner, "tool"))
	}
	return mapped, true
}

// clineAgentKind classifies one inner agent event. llmbackend.Cline streams text and
// thinking as deltas (with a block-end marker) and tool calls as
// start / output-update / end events sharing a toolCallId.
func clineAgentKind(innerType, contentType string) llmbackend.EventKind {
	switch contentType {
	case "text":
		if innerType == "content_end" {
			return llmbackend.KindAssistant
		}
		return llmbackend.KindAssistantDelta
	case "reasoning", "thinking":
		if innerType == "content_end" {
			return llmbackend.KindThoughtEnd
		}
		return llmbackend.KindThoughtDelta
	case "tool":
		switch innerType {
		case "content_start":
			return llmbackend.KindToolCallStarted
		case "content_update":
			return llmbackend.KindToolCallDelta
		case "content_end":
			return llmbackend.KindToolCallCompleted
		default:
			return llmbackend.KindToolCallDelta
		}
	}
	switch innerType {
	case "usage":
		return llmbackend.KindUsage
	case "done":
		return llmbackend.KindRunResult
	case "error":
		return llmbackend.KindError
	case "notice":
		return llmbackend.KindStatus
	case "content_start", "content_update", "content_end":
		return llmbackend.KindAssistantDelta
	default:
		return llmbackend.KindMeta
	}
}

// clineAgentChannel classifies one inner agent event.
func clineAgentChannel(innerType, contentType string) llmbackend.EventChannel {
	switch contentType {
	case "text":
		return llmbackend.ChannelAssistant
	case "reasoning", "thinking":
		return llmbackend.ChannelThought
	case "tool":
		return llmbackend.ChannelTool
	}
	switch innerType {
	case "usage":
		return llmbackend.ChannelMeta
	case "done":
		return llmbackend.ChannelResult
	case "error":
		return llmbackend.ChannelError
	case "notice":
		return llmbackend.ChannelStatus
	case "content_start", "content_update", "content_end":
		return llmbackend.ChannelAssistant
	default:
		return llmbackend.ChannelMeta
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
	out[llmbackend.KeyCallID] = callID
	if name := llmbackend.PayloadString(inner, "toolName"); name != "" {
		out[llmbackend.KeyName] = name
	}
	// llmbackend.Cline signals the tool lifecycle with the event type, so normalize it into
	// the same status key llmbackend.Cursor uses (running while it runs, completed at end).
	if llmbackend.PayloadString(inner, "type") == "content_end" {
		out[llmbackend.KeyStatus] = "completed"
	} else {
		out[llmbackend.KeyStatus] = "running"
	}
	if ms, ok := llmbackend.PayloadNumber(inner, "durationMs", "duration_ms"); ok {
		out[llmbackend.KeyDurationMS] = int64(ms)
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
		out[llmbackend.KeyResult] = output
	}
	return out
}

// mapClineChunk handles the raw chunk stream. Streams other than "agent" carry
// process output (stdout/stderr) and are useful; the "agent" stream is a verbatim
// JSON echo of events already recorded as agent_event, so it is dropped to keep
// llm_events free of duplicates.
func mapClineChunk(ev clinesdk.RunEvent) (llmbackend.Event, bool) {
	if stream := llmbackend.PayloadString(ev.Payload, "stream"); stream == "" || stream == "agent" {
		return llmbackend.Event{}, false
	}
	return llmbackend.Event{
		Channel:   llmbackend.ChannelTool,
		Kind:      llmbackend.KindToolCallDelta,
		EventType: clineEventChunk + ":" + llmbackend.PayloadString(ev.Payload, "stream"),
		Role:      "tool",
		Payload:   ev.Payload,
		TextDelta: llmbackend.PayloadString(ev.Payload, "chunk"),
	}, true
}

func mapClineLifecycle(ev clinesdk.RunEvent, channel llmbackend.EventChannel, kind llmbackend.EventKind, eventType string) (llmbackend.Event, bool) {
	return llmbackend.Event{
		Channel:   channel,
		Kind:      kind,
		EventType: eventType,
		Role:      "system",
		Payload:   ev.Payload,
	}, true
}

// clineRunResultToLLMRun captures run-level metadata (status, usage, timing)
// from a finished llmbackend.Cline run. llmbackend.Cline reports cost in USD and folds cache reads into
// the input tokens; both are preserved as reported.
func ClineRunResultToLLMRun(res clinesdk.RunResult, startedAt time.Time) llmbackend.RunResult {
	status := llmbackend.LLMStatus(strings.ToLower(strings.TrimSpace(res.Status)))
	switch status {
	case llmbackend.StatusRunning, llmbackend.StatusFinished, llmbackend.StatusError, llmbackend.StatusCancelled, llmbackend.StatusExpired:
	default:
		status = llmbackend.StatusError
	}
	usage := llmbackend.Usage{
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
	return llmbackend.RunResult{
		ProviderRunID: llmbackend.FirstNonEmptyString(res.SessionID, res.AgentID),
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
