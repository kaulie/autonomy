package autonomy

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

func TestCursorStreamAdapterMapsRunEvents(t *testing.T) {
	cases := []struct {
		name        string
		event       cursorsdk.RunEvent
		wantChannel LLMEventChannel
		wantText    string
		wantRole    string
	}{
		{
			name:        "assistant chunk",
			event:       cursorsdk.RunEvent{Type: "assistant", Payload: map[string]any{"text": "hello"}, Offset: "o1"},
			wantChannel: LLMChannelAssistant,
			wantText:    "hello",
			wantRole:    "assistant",
		},
		{
			name:        "tool call",
			event:       cursorsdk.RunEvent{Type: "tool_call", Payload: map[string]any{"name": "read_file"}},
			wantChannel: LLMChannelTool,
			wantRole:    "tool",
		},
		{
			name:        "status",
			event:       cursorsdk.RunEvent{Type: "status", Payload: map[string]any{"message": "thinking"}},
			wantChannel: LLMChannelStatus,
			wantRole:    "system",
		},
		{
			name:        "interaction update delta",
			event:       cursorsdk.RunEvent{Type: "interaction_update:text_delta", Payload: map[string]any{"text": "hi"}},
			wantChannel: LLMChannelAssistant,
			wantText:    "hi",
			wantRole:    "assistant",
		},
		{
			name:        "unknown type falls back to meta",
			event:       cursorsdk.RunEvent{Type: "mystery"},
			wantChannel: LLMChannelMeta,
			wantRole:    "system",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := MapNativeLLMEvent(LLMProviderCursor, tc.event, time.Now())
			if !ok {
				t.Fatalf("expected event to map")
			}
			if ev.Channel != tc.wantChannel {
				t.Fatalf("channel=%q, want %q", ev.Channel, tc.wantChannel)
			}
			if ev.TextDelta != tc.wantText {
				t.Fatalf("text_delta=%q, want %q", ev.TextDelta, tc.wantText)
			}
			if ev.Role != tc.wantRole {
				t.Fatalf("role=%q, want %q", ev.Role, tc.wantRole)
			}
			if ev.OffsetToken != tc.event.Offset {
				t.Fatalf("offset=%q, want %q", ev.OffsetToken, tc.event.Offset)
			}
			if ev.EventType != tc.event.Type {
				t.Fatalf("event_type=%q, want %q", ev.EventType, tc.event.Type)
			}
		})
	}
}

func TestMapNativeLLMEventUnknownProviderIsDropped(t *testing.T) {
	if _, ok := MapNativeLLMEvent(LLMProvider("nope"), cursorsdk.RunEvent{}, time.Now()); ok {
		t.Fatal("expected unknown provider to be dropped")
	}
}

func TestLLMTraceRecordsRunStreamAndHeader(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	agent := &Agent{ID: 77, LLMProvider: LLMProviderCursor, Model: "composer-2", LLMAgentID: "cursor-agent-1"}
	trace := BeginLLMTrace(agent, "task-9", 3, ReasonModeAgent, "do the thing")
	if trace == nil || !trace.active {
		t.Fatal("expected an active trace")
	}
	trace.Emit(LLMEvent{Channel: LLMChannelAssistant, EventType: "assistant", TextDelta: "hello",
		Payload: map[string]any{"text": "hello"}, OffsetToken: "o1"})
	trace.Emit(LLMEvent{Channel: LLMChannelTool, EventType: "tool_call", Name: "read_file",
		Payload: map[string]any{"name": "read_file"}})
	trace.Emit(LLMEvent{Channel: LLMChannelStatus, EventType: "status", Payload: map[string]any{"message": "working"}})
	fenced := "```json\n{\"type\":\"done\"}\n```"
	trace.Finish(LLMRunResult{
		ProviderRunID: "run-1",
		Status:        LLMStatusFinished,
		RawOutput:     fenced,
		DurationMS:    1234,
		Usage:         LLMUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	})
	if trace.active {
		t.Fatal("trace should be closed after Finish")
	}

	var (
		turnID                                              int64
		runID, status, rawOut, normalized, mode, llmAgentID string
		eventCount, inputTokens, totalTokens                int
	)
	err = store.db.QueryRow(`SELECT id, run_id, status, raw_output, normalized_output, mode, llm_agent_id, event_count, input_tokens, total_tokens
FROM reason_turns WHERE agent_id = ?`, 77).
		Scan(&turnID, &runID, &status, &rawOut, &normalized, &mode, &llmAgentID, &eventCount, &inputTokens, &totalTokens)
	if err != nil {
		t.Fatal(err)
	}
	if runID != "run-1" || status != string(LLMStatusFinished) {
		t.Fatalf("run_id=%q status=%q", runID, status)
	}
	if llmAgentID != "cursor-agent-1" || mode != string(ReasonModeAgent) {
		t.Fatalf("llm_agent_id=%q mode=%q", llmAgentID, mode)
	}
	if eventCount != 3 || inputTokens != 10 || totalTokens != 30 {
		t.Fatalf("event_count=%d input=%d total=%d", eventCount, inputTokens, totalTokens)
	}
	if rawOut != fenced {
		t.Fatalf("raw_output=%q, want %q", rawOut, fenced)
	}
	if normalized != `{"type":"done"}` {
		t.Fatalf("normalized_output=%q", normalized)
	}

	events, err := store.ListLLMEvents(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events=%d, want 3", len(events))
	}
	for i, ev := range events {
		if ev.Seq != i {
			t.Fatalf("event %d has seq=%d", i, ev.Seq)
		}
		if ev.CreatedAt.IsZero() {
			t.Fatalf("event %d missing created_at", i)
		}
	}
	if events[0].Channel != LLMChannelAssistant || events[0].TextDelta != "hello" || events[0].Payload["text"] != "hello" {
		t.Fatalf("event0=%+v", events[0])
	}
	if events[1].Name != "read_file" || events[1].Channel != LLMChannelTool {
		t.Fatalf("event1=%+v", events[1])
	}

	// Run id is backfilled onto events written before it was known.
	var unbackfilled int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM llm_events WHERE turn_id = ? AND run_id = ''`, turnID).Scan(&unbackfilled); err != nil {
		t.Fatal(err)
	}
	if unbackfilled != 0 {
		t.Fatalf("unbackfilled events=%d, want 0", unbackfilled)
	}
}

func TestAppendLLMEventsIsIdempotentOnSeq(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	turnID, err := store.BeginReasonTurn(ReasonTurn{AgentID: 5, Status: string(LLMStatusRunning)})
	if err != nil {
		t.Fatal(err)
	}
	if turnID == 0 {
		t.Fatal("expected a turn id")
	}
	batch := []LLMEvent{
		{Seq: 0, EventType: "assistant", Channel: LLMChannelAssistant, TextDelta: "a"},
		{Seq: 1, EventType: "assistant", Channel: LLMChannelAssistant, TextDelta: "b"},
	}
	if err := store.AppendLLMEvents(turnID, "", batch); err != nil {
		t.Fatal(err)
	}
	// A replayed stream must not double-write the same seq.
	if err := store.AppendLLMEvents(turnID, "", batch); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM llm_events WHERE turn_id = ?`, turnID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("event rows=%d, want 2", count)
	}
}

func TestLLMTraceWithoutStoreIsNoop(t *testing.T) {
	prevStore, prevAutonomy := _store, _autonomy
	_store, _autonomy = nil, nil
	t.Cleanup(func() { _store, _autonomy = prevStore, prevAutonomy })

	trace := BeginLLMTrace(&Agent{ID: 1}, "task", 1, ReasonModePlan, "in")
	if trace == nil {
		t.Fatal("trace must never be nil")
	}
	// These must not panic or write anything without a store.
	trace.Emit(LLMEvent{EventType: "assistant", Channel: LLMChannelAssistant})
	trace.Finish(LLMRunResult{Status: LLMStatusFinished})
}

func TestLLMEventStreamEnabledParsing(t *testing.T) {
	cases := map[string]bool{
		"": true, "1": true, "true": true, "on": true, "yes": true,
		"0": false, "false": false, "off": false, "no": false, "OFF": false, "disabled": false,
	}
	for value, want := range cases {
		t.Setenv("AUTONOMY_LLM_EVENTS", value)
		if got := llmEventStreamEnabled(); got != want {
			t.Fatalf("AUTONOMY_LLM_EVENTS=%q enabled=%v, want %v", value, got, want)
		}
	}
}

func TestLLMTraceSkipsStreamWhenDisabled(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })
	t.Setenv("AUTONOMY_LLM_EVENTS", "0")

	agent := &Agent{ID: 88, LLMProvider: LLMProviderCursor, Model: "composer-2"}
	trace := BeginLLMTrace(agent, "task-d", 1, ReasonModePlan, "in")
	trace.Emit(LLMEvent{EventType: "assistant", Channel: LLMChannelAssistant, TextDelta: "x"})
	trace.Emit(LLMEvent{EventType: "assistant", Channel: LLMChannelAssistant, TextDelta: "y"})
	trace.Finish(LLMRunResult{ProviderRunID: "run-x", Status: LLMStatusFinished, RawOutput: "done"})

	var (
		turnID int64
		status string
	)
	if err := store.db.QueryRow(`SELECT id, status FROM reason_turns WHERE agent_id = ?`, 88).Scan(&turnID, &status); err != nil {
		t.Fatal(err)
	}
	if status != string(LLMStatusFinished) {
		t.Fatalf("status=%q, want %q (run header must still be written)", status, LLMStatusFinished)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM llm_events WHERE turn_id = ?`, turnID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("llm_events rows=%d, want 0 when AUTONOMY_LLM_EVENTS=0", count)
	}
}
