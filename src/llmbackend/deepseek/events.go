package deepseek

import (
	"encoding/json"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// Native events are what this harness sees of a run, one map each with a "type": the
// stream's own deltas (client.stream) and the tool loop's calls and results (session).
//
//	reasoning_delta  {text}                     DeepSeek's reasoning_content, streamed
//	reasoning_end    {text}                     the whole reasoning block of one completion
//	content_delta    {text}                     assistant text, streamed
//	tool_call_delta  {index,id,name,arguments}  a fragment of a streamed tool call
//	tool_call        {id,name,arguments}        a complete call, about to run
//	tool_result      {id,name,content,is_error} what the call returned
//	usage            {usage}                    the completion's token usage (OpenAI shape)
//	finish           {finish_reason}            one completion ended
//	error            {message}                  the run failed
const (
	nativeReasoningDelta = "reasoning_delta"
	nativeReasoningEnd   = "reasoning_end"
	nativeContentDelta   = "content_delta"
	nativeToolCallDelta  = "tool_call_delta"
	nativeToolCall       = "tool_call"
	nativeToolResult     = "tool_result"
	nativeUsage          = "usage"
	nativeFinish         = "finish"
	nativeError          = "error"
)

type streamAdapter struct{}

func (streamAdapter) Provider() llmbackend.Provider { return llmbackend.ProviderDeepseekHarness }

func (streamAdapter) MapEvent(native any, startedAt time.Time) (llmbackend.Event, bool) {
	n, ok := native.(map[string]any)
	if !ok || llmbackend.PayloadString(n, "type") == "" {
		return llmbackend.Event{}, false
	}
	now := time.Now()
	ev := llmbackend.Event{Channel: llmbackend.ChannelMeta, Kind: llmbackend.KindMeta, Role: "system",
		EventType: llmbackend.PayloadString(n, "type"), Payload: n, CreatedAt: now}
	if !startedAt.IsZero() {
		ev.ElapsedMS = now.Sub(startedAt).Milliseconds()
	}
	text := llmbackend.PayloadString(n, "text")
	switch ev.EventType {
	case nativeReasoningDelta:
		ev.Channel, ev.Kind, ev.Role, ev.TextDelta = llmbackend.ChannelThought, llmbackend.KindThoughtDelta, "assistant", text
		ev.Payload = llmbackend.WithNeutralText(n, text)
	case nativeReasoningEnd:
		ev.Channel, ev.Kind, ev.Role = llmbackend.ChannelThought, llmbackend.KindThoughtEnd, "assistant"
		ev.Payload = llmbackend.WithNeutralText(n, text)
	case nativeContentDelta:
		ev.Channel, ev.Kind, ev.Role, ev.TextDelta = llmbackend.ChannelAssistant, llmbackend.KindAssistantDelta, "assistant", text
		ev.Payload = llmbackend.WithNeutralText(n, text)
	case nativeToolCallDelta:
		// Argument fragments are kept for replay; the call itself is reported once whole.
		ev.Name = llmbackend.PayloadString(n, "name")
	case nativeToolCall:
		ev.Channel, ev.Kind, ev.Role = llmbackend.ChannelTool, llmbackend.KindToolCallStarted, "tool"
		ev.Name = llmbackend.PayloadString(n, "name")
		ev.Payload = llmbackend.WithNeutralKV(n,
			llmbackend.KeyCallID, n["id"], llmbackend.KeyName, ev.Name,
			llmbackend.KeyArgs, decodeArgs(llmbackend.PayloadString(n, "arguments")),
			llmbackend.KeyStatus, "running")
	case nativeToolResult:
		ev.Channel, ev.Kind, ev.Role = llmbackend.ChannelTool, llmbackend.KindToolCallCompleted, "tool"
		ev.Name = llmbackend.PayloadString(n, "name")
		status := "completed"
		if n["is_error"] == true {
			status = "failed"
		}
		ev.Payload = llmbackend.WithNeutralKV(n,
			llmbackend.KeyCallID, n["id"], llmbackend.KeyName, ev.Name,
			llmbackend.KeyResult, n["content"], llmbackend.KeyStatus, status)
		if d, ok := llmbackend.PayloadNumber(n, "duration_ms"); ok {
			ev.Payload = llmbackend.WithNeutralKV(ev.Payload, llmbackend.KeyDurationMS, int64(d))
		}
	case nativeUsage:
		ev.Channel, ev.Kind = llmbackend.ChannelStatus, llmbackend.KindUsage
		u := usageOf(llmbackend.PayloadMap(n, "usage"))
		ev.Payload = llmbackend.WithNeutralKV(n,
			llmbackend.KeyInputTokens, u.InputTokens, llmbackend.KeyOutputTokens, u.OutputTokens,
			llmbackend.KeyCacheReadTokens, u.CacheReadTokens, llmbackend.KeyTotalTokens, u.TotalTokens)
	case nativeFinish:
		ev.Channel, ev.Kind = llmbackend.ChannelStatus, llmbackend.KindStatus
		ev.Payload = llmbackend.WithNeutralKV(n, llmbackend.KeyStatus, n["finish_reason"])
	case nativeError:
		ev.Channel, ev.Kind = llmbackend.ChannelError, llmbackend.KindError
		ev.Payload = llmbackend.WithNeutralKV(n, llmbackend.KeyMessage, n["message"])
	}
	return ev, true
}

// decodeArgs is a call's arguments as the JSON object they are, or the raw string when the
// model sent something that is not one.
func decodeArgs(raw string) any {
	var v map[string]any
	if json.Unmarshal([]byte(raw), &v) == nil {
		return v
	}
	return raw
}

// usageOf reads DeepSeek's usage object (OpenAI's keys plus its cache hit/miss split).
func usageOf(u map[string]any) llmbackend.Usage {
	n := func(m map[string]any, key string) int64 { v, _ := llmbackend.PayloadNumber(m, key); return int64(v) }
	out := llmbackend.Usage{
		InputTokens:     n(u, "prompt_tokens"),
		OutputTokens:    n(u, "completion_tokens"),
		CacheReadTokens: n(u, "prompt_cache_hit_tokens"),
		TotalTokens:     n(u, "total_tokens"),
		ReasoningTokens: n(llmbackend.PayloadMap(u, "completion_tokens_details"), "reasoning_tokens"),
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = out.InputTokens + out.OutputTokens
	}
	return out
}

// addUsage sums the completions of one turn (a tool loop is several requests).
func addUsage(a, b llmbackend.Usage) llmbackend.Usage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.ReasoningTokens += b.ReasoningTokens
	a.TotalTokens += b.TotalTokens
	return a
}
