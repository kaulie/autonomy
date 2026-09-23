package codex

import (
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/codexsdk"
	"github.com/kaulie/autonomy/src/llmbackend"
)

// CodexStreamAdapter maps the Codex SDK's run stream onto neutral LLMEvents. It is the only
// place that knows Codex's payload shapes, so the trace and store layers stay
// backend-agnostic.
//
// The bridge forwards the SDK's events verbatim, so this reads the whole event from
// RunEvent.Native (Codex has no `payload` field; its items ride in `item`). Each event is
// kept for fidelity, and what consumers read is annotated with the neutral keys
// (call_id/args/result/status/usage).
type CodexStreamAdapter struct{}

func (CodexStreamAdapter) Provider() llmbackend.Provider { return llmbackend.ProviderCodex }

// Codex's own thread/turn/item discriminators, kept verbatim in EventType.
const (
	codexThreadStarted = "thread.started"
	codexTurnStarted   = "turn.started"
	codexTurnCompleted = "turn.completed"
	codexTurnFailed    = "turn.failed"
	codexThreadError   = "thread.error"
	codexItemStarted   = "item.started"
	codexItemUpdated   = "item.updated"
	codexItemCompleted = "item.completed"
)

// Codex's item types.
const (
	codexItemAgentMessage = "agent_message"
	codexItemReasoning    = "reasoning"
	codexItemCommand      = "command_execution"
	codexItemFileChange   = "file_change"
	codexItemMCPTool      = "mcp_tool_call"
	codexItemWebSearch    = "web_search"
	codexItemTodoList     = "todo_list"
	codexItemError        = "error"
)

// MapEvent converts one native bridge event.
func (CodexStreamAdapter) MapEvent(native any, _ time.Time) (llmbackend.Event, bool) {
	ev, ok := native.(codexsdk.RunEvent)
	if !ok {
		return llmbackend.Event{}, false
	}
	switch ev.Type {
	case codexThreadStarted:
		return codexLifecycle(ev, llmbackend.ChannelMeta, llmbackend.KindMeta, codexThreadStarted), true
	case codexTurnStarted:
		return codexLifecycle(ev, llmbackend.ChannelStatus, llmbackend.KindStatus, codexTurnStarted), true
	case codexTurnCompleted:
		return codexTurnUsage(ev), true
	case codexTurnFailed, codexThreadError:
		return codexFailure(ev), true
	case codexItemStarted, codexItemUpdated, codexItemCompleted:
		return codexItemEvent(ev)
	default:
		if strings.TrimSpace(ev.Type) == "" {
			return llmbackend.Event{}, false
		}
		return codexLifecycle(ev, llmbackend.ChannelMeta, llmbackend.KindMeta, ev.Type), true
	}
}

// codexItemEvent handles one item's lifecycle: the model's message, its reasoning, and the
// tools it ran (a command, a file change, an MCP call, a search, its todo list).
func codexItemEvent(ev codexsdk.RunEvent) (llmbackend.Event, bool) {
	item := llmbackend.PayloadMap(ev.Native, "item")
	if len(item) == 0 {
		return codexLifecycle(ev, llmbackend.ChannelMeta, llmbackend.KindMeta, ev.Type), true
	}
	itemType := llmbackend.PayloadString(item, "type")
	completed := ev.Type == codexItemCompleted
	switch itemType {
	case codexItemAgentMessage:
		// Codex reports the message's text as it accumulates, so an update carries the
		// text so far (the completed item is the authoritative one).
		kind := llmbackend.KindAssistant
		if ev.Type == codexItemUpdated {
			kind = llmbackend.KindAssistantDelta
		}
		return llmbackend.Event{
			Channel: llmbackend.ChannelAssistant, Kind: kind, EventType: ev.Type + ":" + itemType,
			Role: "assistant", TextDelta: llmbackend.PayloadString(item, "text"), Payload: item,
		}, true
	case codexItemReasoning:
		kind := llmbackend.KindThoughtDelta
		if completed {
			kind = llmbackend.KindThoughtEnd
		}
		return llmbackend.Event{
			Channel: llmbackend.ChannelThought, Kind: kind, EventType: ev.Type + ":" + itemType,
			Role: "assistant", TextDelta: llmbackend.PayloadString(item, "text"), Payload: item,
		}, true
	case codexItemCommand, codexItemFileChange, codexItemMCPTool, codexItemWebSearch, codexItemTodoList:
		return codexToolEvent(ev, item, itemType, completed), true
	case codexItemError:
		return llmbackend.Event{
			Channel: llmbackend.ChannelError, Kind: llmbackend.KindError, EventType: ev.Type + ":" + itemType,
			Role:    "system",
			Payload: llmbackend.WithNeutralKV(item, llmbackend.KeyMessage, llmbackend.PayloadString(item, "message")),
		}, true
	default:
		return codexLifecycle(ev, llmbackend.ChannelMeta, llmbackend.KindMeta, ev.Type+":"+itemType), true
	}
}

// codexToolEvent maps a tool item onto the tool kinds, annotating the neutral keys the
// aggregation layer reads: call_id (the item id), name, args and result.
func codexToolEvent(ev codexsdk.RunEvent, item map[string]any, itemType string, completed bool) llmbackend.Event {
	kind := llmbackend.KindToolCallStarted
	switch ev.Type {
	case codexItemUpdated:
		kind = llmbackend.KindToolCallDelta
	case codexItemCompleted:
		kind = llmbackend.KindToolCallCompleted
	}
	status := llmbackend.PayloadString(item, "status")
	if status == "" && completed {
		status = "completed"
	}
	payload := llmbackend.WithNeutralKV(item, llmbackend.KeyStatus, status)
	if callID := llmbackend.PayloadString(item, "id"); callID != "" {
		payload = llmbackend.WithNeutralKV(payload, llmbackend.KeyCallID, callID)
	}
	// args is what the tool was asked to do: a command, an MCP call, a file change, a query.
	for _, key := range []string{"command", "arguments", "changes", "query", "items"} {
		if args, ok := item[key]; ok {
			payload = llmbackend.WithNeutralKV(payload, llmbackend.KeyArgs, args)
			break
		}
	}
	// result is what it produced: a command's output, an MCP result.
	output := llmbackend.PayloadString(item, "aggregated_output")
	if output != "" {
		payload = llmbackend.WithNeutralKV(payload, llmbackend.KeyResult, output)
		payload = llmbackend.WithNeutralKV(payload, llmbackend.KeyChunk, output)
	} else if out, ok := item["result"]; ok {
		payload = llmbackend.WithNeutralKV(payload, llmbackend.KeyResult, out)
	}
	// A command's exit code is kept verbatim; the neutral status says whether the tool call
	// itself finished.
	if code, ok := llmbackend.PayloadNumber(item, "exit_code", "exitCode"); ok {
		payload = llmbackend.WithNeutralKV(payload, "exit_code", int64(code))
	}
	name := itemType
	if server := llmbackend.PayloadString(item, "server"); server != "" {
		name = server + ":" + llmbackend.FirstNonEmptyString(llmbackend.PayloadString(item, "tool"), itemType)
	}
	return llmbackend.Event{
		Channel: llmbackend.ChannelTool, Kind: kind, EventType: ev.Type + ":" + itemType,
		Role: "tool", Name: name, Payload: payload, TextDelta: output,
	}
}

// codexTurnUsage carries a finished turn's usage, in the neutral token keys.
func codexTurnUsage(ev codexsdk.RunEvent) llmbackend.Event {
	usage := llmbackend.PayloadMap(ev.Native, "usage")
	return llmbackend.Event{
		Channel: llmbackend.ChannelStatus, Kind: llmbackend.KindUsage, EventType: codexTurnCompleted,
		Role: "system", Payload: llmbackend.WithUsageKeys(ev.Native, usage),
	}
}

// codexFailure carries a failed turn (or a thread error) as an error event.
func codexFailure(ev codexsdk.RunEvent) llmbackend.Event {
	message := llmbackend.PayloadString(ev.Native, "message")
	if message == "" {
		message = llmbackend.PayloadString(llmbackend.PayloadMap(ev.Native, "error"), "message")
	}
	return llmbackend.Event{
		Channel: llmbackend.ChannelError, Kind: llmbackend.KindError, EventType: ev.Type,
		Role: "system", Payload: llmbackend.WithNeutralKV(ev.Native, llmbackend.KeyMessage, message),
	}
}

func codexLifecycle(ev codexsdk.RunEvent, channel llmbackend.EventChannel, kind llmbackend.EventKind, eventType string) llmbackend.Event {
	return llmbackend.Event{
		Channel: channel, Kind: kind, EventType: eventType, Role: "system", Payload: ev.Native,
	}
}

// CodexRunResultToLLMRun captures run-level metadata (status, usage, timing) from a
// finished Codex run. Codex reports tokens only — no cost — so nothing is invented.
func CodexRunResultToLLMRun(res codexsdk.RunResult, startedAt time.Time) llmbackend.RunResult {
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
