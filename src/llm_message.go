package autonomy

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// This file derives the aggregated conversation messages (thinking, tool calls)
// from a run's raw llm_events stream. The raw stream keeps one row per provider
// event, so assistant/thinking text arrives as tiny token deltas; the message
// layer keeps the same content but aggregated, so a consumer reads whole
// messages instead of fragments. Sharing this as a pure function keeps the
// aggregation database-agnostic: any Store engine feeds it its own events.

// AggregateChatMessages turns a run's raw stream into aggregated intermediate
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
//
// It is a thin wrapper over chatAggregator — the very accumulator a live run
// feeds event by event — so a stream derived at the end and a stream derived
// while it runs can never disagree.
func AggregateChatMessages(events []LLMEvent) []LLMMessage {
	agg := newChatAggregator()
	bySeq := map[int]LLMMessage{}
	for _, ev := range events {
		for _, m := range agg.add(ev) {
			bySeq[m.Seq] = m
		}
	}
	for _, m := range agg.flush() {
		bySeq[m.Seq] = m
	}
	out := make([]LLMMessage, 0, len(bySeq))
	for _, m := range bySeq {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// chatAggregator folds a run's stream into aggregated messages. It is the single
// implementation behind both derivations:
//
//   - AggregateChatMessages folds a whole stream (finish/backfill/replay);
//   - LLMTrace feeds it events as they arrive and persists each message the
//     moment it is complete, so a live run is readable in llm_messages instead
//     of only appearing once the run ends.
//
// A message is *complete* when its content can no longer change: a thinking
// block when the next tool message starts (or the run ends), a tool message when
// its result arrives. Callers must be able to re-write a message (upsert): a
// tool call whose call_id shows up again after it was emitted is re-emitted.
type chatAggregator struct {
	groups   []*chatGroup
	byCallID map[string]*chatGroup
}

func newChatAggregator() *chatAggregator {
	return &chatAggregator{byCallID: map[string]*chatGroup{}}
}

// add folds one event in and returns the messages it completed, in seq order.
func (a *chatAggregator) add(ev LLMEvent) []LLMMessage {
	switch ev.Channel {
	case LLMChannelThought:
		if n := len(a.groups); n > 0 && a.groups[n-1].role == LLMMessageRoleThinking {
			a.groups[n-1].appendText(ev)
			return nil
		}
		g := &chatGroup{role: LLMMessageRoleThinking, seq: len(a.groups) + 1}
		g.appendText(ev)
		a.groups = append(a.groups, g)
		return nil
	case LLMChannelTool:
		callID := payloadString(ev.Payload, "call_id")
		if callID != "" {
			if g, ok := a.byCallID[callID]; ok {
				g.mergeTool(ev)
				return a.emit(g)
			}
		}
		// Starting a tool message also closes the open thinking block: thinking
		// only merges while it stays the last group, exactly as before.
		out := a.closeThinking()
		g := &chatGroup{role: LLMMessageRoleTool, callID: callID, seq: len(a.groups) + 1, createdAt: ev.CreatedAt}
		g.mergeTool(ev)
		a.groups = append(a.groups, g)
		if callID != "" {
			a.byCallID[callID] = g
		}
		return append(out, a.emit(g)...)
	}
	return nil
}

// closeThinking emits the open thinking block, which ends when the next group
// starts (or at flush).
func (a *chatAggregator) closeThinking() []LLMMessage {
	if n := len(a.groups); n > 0 && a.groups[n-1].role == LLMMessageRoleThinking && !a.groups[n-1].emitted {
		return a.emit(a.groups[n-1])
	}
	return nil
}

// emit returns a group's message once it can no longer change: a closed thinking
// block, or a tool call that has its result (a tool call without one may still
// receive the result, so it stays open).
func (a *chatAggregator) emit(g *chatGroup) []LLMMessage {
	if g.role == LLMMessageRoleTool && !g.hasResult {
		return nil
	}
	g.emitted = true
	return []LLMMessage{g.message()}
}

// flush returns the still-open groups at the end of a stream (a trailing
// thinking block, a tool call that never returned) in seq order.
func (a *chatAggregator) flush() []LLMMessage {
	var out []LLMMessage
	for _, g := range a.groups {
		if g.emitted {
			continue
		}
		g.emitted = true
		out = append(out, g.message())
	}
	return out
}

// chatGroup accumulates one aggregated message while walking the stream: text
// deltas for thinking, or the call+result fields for a tool call.
type chatGroup struct {
	// seq is the group's position in the run's message order (1-based, the user
	// input is 0). It is fixed when the group is created, so a message written
	// while the run streams keeps the seq the final derivation gives it.
	seq     int
	emitted bool
	role    LLMMessageRole
	callID  string
	name    string
	args    any
	result  any
	// hasResult distinguishes an absent result from a JSON null.
	hasResult bool
	text      strings.Builder
	createdAt time.Time
	// endedAt tracks the last event of the group, so a thinking block's duration
	// can be derived when the provider does not report one (Cline).
	endedAt time.Time
	// reportedMS is the provider-reported thinking duration (Cursor).
	reportedMS *int64
}

func (g *chatGroup) appendText(ev LLMEvent) {
	g.text.WriteString(ev.TextDelta)
	if g.createdAt.IsZero() {
		g.createdAt = ev.CreatedAt
	}
	if !ev.CreatedAt.IsZero() {
		g.endedAt = ev.CreatedAt
	}
	if g.reportedMS == nil {
		if ms, ok := numberPayload(ev.Payload, "thinking_duration_ms"); ok {
			g.reportedMS = &ms
		}
	}
}

// thinkingDurationMS is the provider-reported thinking duration when available,
// otherwise the span covered by the block's events.
func (g *chatGroup) thinkingDurationMS() (int64, bool) {
	if g.reportedMS != nil {
		return *g.reportedMS, true
	}
	if g.createdAt.IsZero() || g.endedAt.IsZero() {
		return 0, false
	}
	if d := g.endedAt.Sub(g.createdAt).Milliseconds(); d > 0 {
		return d, true
	}
	return 0, false
}

// numberPayload reads a numeric payload field (JSON numbers decode as float64).
func numberPayload(payload map[string]any, key string) (int64, bool) {
	switch v := payload[key].(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
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

func (g *chatGroup) message() LLMMessage {
	m := LLMMessage{Seq: g.seq, Role: g.role, CreatedAt: g.createdAt}
	switch g.role {
	case LLMMessageRoleThinking:
		m.Content = g.text.String()
		// Both backends expose how long the model thought: Cursor reports
		// thinking_duration_ms, Cline only streams reasoning deltas (so the
		// span between them is the duration). Normalizing it here gives the UI
		// one field to render "thought for Xs" on either backend.
		if d, ok := g.thinkingDurationMS(); ok {
			m.NormalizedContent = marshalJSONValue(map[string]any{"duration_ms": d}, true)
		}
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
