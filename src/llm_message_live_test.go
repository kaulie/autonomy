package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"path/filepath"
	"testing"
)

// TestLLMTraceWritesMessagesWhileStreaming pins the point of the live
// aggregation: llm_messages is not an end-of-run artifact. Each thinking block
// and tool call is persisted the moment the stream completes it, so a consumer
// can follow a long run instead of waiting for Finish.
func TestLLMTraceWritesMessagesWhileStreaming(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	agent := &Agent{ID: 77, LLMProvider: llmbackend.ProviderCline, Model: "deepseek-v4-pro"}
	trace := BeginLLMTrace(agent, "task-live", 1, ReasonModeAgent, "list the bridge dir")
	turnID := trace.handle.TurnID

	// Thinking streams as deltas (one event per chunk); nothing is complete yet.
	trace.Emit(llmbackend.Event{Channel: llmbackend.ChannelThought, EventType: "agent_event:content_start:reasoning", TextDelta: "Let me"})
	trace.Emit(llmbackend.Event{Channel: llmbackend.ChannelThought, EventType: "agent_event:content_start:reasoning", TextDelta: " check the code"})
	if got := mustListMessages(t, store, turnID); len(got) != 1 {
		t.Fatalf("mid-thought messages=%d, want 1 (just the user input): %+v", len(got), got)
	}

	// The tool call starting is what closes the thinking block.
	trace.Emit(llmbackend.Event{Channel: llmbackend.ChannelTool, EventType: "agent_event:content_start:tool", Name: "execute_command",
		Payload: map[string]any{"call_id": "call-1", "args": map[string]any{"command": "ls"}}})
	msgs := mustListMessages(t, store, turnID)
	if len(msgs) != 2 {
		t.Fatalf("after tool start messages=%d, want 2 (user + thinking): %+v", len(msgs), msgs)
	}
	if msgs[1].Role != LLMMessageRoleThinking || msgs[1].Seq != 1 || msgs[1].Content != "Let me check the code" {
		t.Fatalf("thinking message=%+v, want the aggregated block at seq 1", msgs[1])
	}

	// The tool's return completes the tool message.
	trace.Emit(llmbackend.Event{Channel: llmbackend.ChannelTool, EventType: "agent_event:content_end:tool", Name: "execute_command",
		Payload: map[string]any{"call_id": "call-1", "result": map[string]any{"exit": 0, "stdout": "bridge\n"}}})
	msgs = mustListMessages(t, store, turnID)
	if len(msgs) != 3 {
		t.Fatalf("after tool result messages=%d, want 3 (user + thinking + tool): %+v", len(msgs), msgs)
	}
	if msgs[2].Role != LLMMessageRoleTool || msgs[2].Seq != 2 {
		t.Fatalf("tool message=%+v, want role=tool at seq 2", msgs[2])
	}
	if msgs[2].Content != `{"exit":0,"stdout":"bridge\n"}` {
		t.Fatalf("tool content=%q, want the tool result", msgs[2].Content)
	}

	// Finish adds only the trailing assistant row, one past everything written.
	trace.Finish(llmbackend.RunResult{ProviderRunID: "run-live", Status: llmbackend.StatusFinished, RawOutput: "found it"})
	msgs = mustListMessages(t, store, turnID)
	if len(msgs) != 4 {
		t.Fatalf("after finish messages=%d, want 4: %+v", len(msgs), msgs)
	}
	out := msgs[3]
	if out.Role != LLMMessageRoleAssistant || out.Seq != 3 || out.Content != "found it" {
		t.Fatalf("assistant message=%+v, want the return at seq 3", out)
	}
	if out.ParentID != msgs[0].ID {
		t.Fatalf("assistant parent_id=%d, want the user row id %d", out.ParentID, msgs[0].ID)
	}

	// Re-finishing stays idempotent: the same four rows, no second assistant row.
	trace.Finish(llmbackend.RunResult{ProviderRunID: "run-live", Status: llmbackend.StatusFinished, RawOutput: "found it"})
	if got := mustListMessages(t, store, turnID); len(got) != 4 {
		t.Fatalf("messages after re-finish=%d, want 4: %+v", len(got), got)
	}
}

func mustListMessages(t *testing.T, store rawStore, turnID int64) []LLMMessage {
	t.Helper()
	msgs, err := store.ListLLMMessages(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// TestLLMTraceWritesMessagesWithRawStreamDisabled proves the message layer is
// independent of the optional raw replay: AUTONOMY_LLM_EVENTS=0 skips llm_events,
// not llm_messages.
func TestLLMTraceWritesMessagesWithRawStreamDisabled(t *testing.T) {
	t.Setenv("AUTONOMY_LLM_EVENTS", "0")
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	trace := BeginLLMTrace(&Agent{ID: 78}, "task-nostream", 1, ReasonModeAgent, "do it")
	trace.Emit(llmbackend.Event{Channel: llmbackend.ChannelThought, EventType: "thinking", TextDelta: "because"})
	trace.Emit(llmbackend.Event{Channel: llmbackend.ChannelTool, EventType: "tool_call", Name: "shell",
		Payload: map[string]any{"call_id": "c9", "args": map[string]any{"command": "ls"}, "result": "ok"}})
	trace.Finish(llmbackend.RunResult{Status: llmbackend.StatusFinished, RawOutput: "done"})

	msgs := mustListMessages(t, store, trace.handle.TurnID)
	if len(msgs) != 4 {
		t.Fatalf("messages=%d, want 4 (user + thinking + tool + assistant): %+v", len(msgs), msgs)
	}
	if msgs[1].Role != LLMMessageRoleThinking || msgs[1].Content != "because" {
		t.Fatalf("thinking=%+v", msgs[1])
	}
	if msgs[3].Role != LLMMessageRoleAssistant || msgs[3].Seq != 3 {
		t.Fatalf("assistant=%+v, want seq 3", msgs[3])
	}
	if events, err := store.ListLLMEvents(trace.handle.TurnID); err != nil || len(events) != 0 {
		t.Fatalf("llm_events=%d (err=%v), want 0 with AUTONOMY_LLM_EVENTS=0", len(events), err)
	}
}

// TestChatAggregatorEmitsEveryMessageOnce pins the incremental emissions: a live
// run ends up with exactly the messages (and seqs) the whole-stream derivation
// produces, even when a tool call returns only after other groups were emitted.
func TestChatAggregatorEmitsEveryMessageOnce(t *testing.T) {
	events := []llmbackend.Event{
		{Channel: llmbackend.ChannelThought, TextDelta: "first "},
		{Channel: llmbackend.ChannelThought, TextDelta: "block"},
		{Channel: llmbackend.ChannelTool, Name: "shell", Payload: map[string]any{"call_id": "c1", "args": map[string]any{"command": "ls"}}},
		{Channel: llmbackend.ChannelTool, Name: "read", Payload: map[string]any{"call_id": "c2", "result": "file body"}},
		{Channel: llmbackend.ChannelAssistant, TextDelta: "nope"},
		{Channel: llmbackend.ChannelThought, TextDelta: "second block"},
		// c1 returns only now: its message was never emitted before, and the merge
		// re-emits the (still correct) row it now has.
		{Channel: llmbackend.ChannelTool, Name: "shell", Payload: map[string]any{"call_id": "c1", "result": "listing"}},
	}

	agg := newChatAggregator()
	emitted := map[int]int{}
	final := map[int]LLMMessage{}
	collect := func(msgs []LLMMessage) {
		for _, m := range msgs {
			emitted[m.Seq]++
			final[m.Seq] = m
		}
	}
	for _, ev := range events {
		collect(agg.add(ev))
	}
	collect(agg.flush())

	want := AggregateChatMessages(events)
	if len(want) != 4 {
		t.Fatalf("whole-stream messages=%d, want 4: %+v", len(want), want)
	}
	if len(final) != len(want) {
		t.Fatalf("streamed messages=%d, want %d: %+v", len(final), len(want), final)
	}
	for _, w := range want {
		got, ok := final[w.Seq]
		if !ok {
			t.Fatalf("seq %d missing from the streamed emissions: %+v", w.Seq, final)
		}
		if got.Role != w.Role || got.Content != w.Content || got.NormalizedContent != w.NormalizedContent {
			t.Fatalf("seq %d streamed=%+v, whole-stream=%+v", w.Seq, got, w)
		}
	}
	// The first thinking block is closed by the tool call, so it is emitted once,
	// already complete — not emitted empty and patched up later. The first tool
	// message could not be written before its result arrived (seq 2).
	if emitted[1] != 1 {
		t.Fatalf("seq 1 emitted %d times, want 1", emitted[1])
	}
	if final[1].Content != "first block" {
		t.Fatalf("seq 1 content=%q, want the whole thinking block", final[1].Content)
	}
	if emitted[2] != 1 || final[2].Content != `"listing"` {
		t.Fatalf("seq 2 emitted=%d content=%q, want the tool message once, merged", emitted[2], final[2].Content)
	}
}
