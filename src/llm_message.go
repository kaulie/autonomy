package autonomy

import (
	"encoding/json"
	"strings"
	"time"
)

// This file derives the aggregated conversation messages (thinking, tool calls)
// from a run's raw llm_events stream. The raw stream keeps one row per provider
// event, so assistant/thinking text arrives as tiny token deltas; the message
// layer keeps the same content but aggregated, so a consumer reads whole
// messages instead of fragments. Sharing this as a pure function keeps the
// aggregation database-agnostic: any Store engine feeds it its own events.

// aggregateChatMessages turns a run's raw stream into aggregated intermediate
// messages, in the order the events occurred:
//
//   - each maximal run of consecutive thinking events becomes one thinking
//     message (the text deltas concatenated into the whole reasoning text);
//   - each tool call becomes one message per call_id, merging the call and its
//     result (content = the tool result, normalized_content = name/args/call_id).
//
// Assistant deltas are skipped: they are the run's final return, recorded
// separately as the linked assistant message. status/meta/result events carry no
// conversation text and are skipped too.
//
// Seq starts at 1 (the user input is 0). The caller places the assistant row one
// past the last row returned here so it stays the final message of the run.
func aggregateChatMessages(events []LLMEvent) []LLMMessage {
	var groups []*chatGroup
	byCallID := map[string]*chatGroup{}
	for _, ev := range events {
		switch ev.Channel {
		case LLMChannelThought:
			if n := len(groups); n > 0 && groups[n-1].role == LLMMessageRoleThinking {
				groups[n-1].appendText(ev)
				continue
			}
			g := &chatGroup{role: LLMMessageRoleThinking, createdAt: ev.CreatedAt}
			g.appendText(ev)
			groups = append(groups, g)
		case LLMChannelTool:
			callID := payloadString(ev.Payload, "call_id")
			if callID != "" {
				if g, ok := byCallID[callID]; ok {
					g.mergeTool(ev)
					continue
				}
			}
			g := &chatGroup{role: LLMMessageRoleTool, callID: callID, createdAt: ev.CreatedAt}
			g.mergeTool(ev)
			groups = append(groups, g)
			if callID != "" {
				byCallID[callID] = g
			}
		}
	}
	out := make([]LLMMessage, 0, len(groups))
	for i, g := range groups {
		out = append(out, g.message(i+1))
	}
	return out
}

// chatGroup accumulates one aggregated message while walking the stream: text
// deltas for thinking, or the call+result fields for a tool call.
type chatGroup struct {
	role   LLMMessageRole
	callID string
	name   string
	args   any
	result any
	// hasResult distinguishes an absent result from a JSON null.
	hasResult bool
	text      strings.Builder
	createdAt time.Time
}

func (g *chatGroup) appendText(ev LLMEvent) {
	g.text.WriteString(ev.TextDelta)
	if g.createdAt.IsZero() {
		g.createdAt = ev.CreatedAt
	}
}

func (g *chatGroup) mergeTool(ev LLMEvent) {
	if g.createdAt.IsZero() {
		g.createdAt = ev.CreatedAt
	}
	if ev.Name != "" {
		g.name = ev.Name
	}
	if g.args == nil {
		if a, ok := ev.Payload["args"]; ok {
			g.args = a
		}
	}
	if r, ok := ev.Payload["result"]; ok {
		g.result = r
		g.hasResult = true
	}
}

func (g *chatGroup) message(seq int) LLMMessage {
	m := LLMMessage{Seq: seq, Role: g.role, CreatedAt: g.createdAt}
	switch g.role {
	case LLMMessageRoleThinking:
		m.Content = g.text.String()
	case LLMMessageRoleTool:
		m.Content = marshalJSONValue(g.result, g.hasResult)
		m.NormalizedContent = marshalJSONValue(map[string]any{
			"name":    g.name,
			"call_id": g.callID,
			"args":    g.args,
		}, true)
	}
	return m
}

// marshalJSONValue renders a provider payload value as compact JSON. When
// present is false it returns "" so an absent field stays distinct from null.
func marshalJSONValue(v any, present bool) string {
	if !present {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
