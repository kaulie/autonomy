package autonomy

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// llmEventFlushSize bounds the number of buffered events before a best-effort
// flush, so long streams do not insert one row at a time.
const llmEventFlushSize = 64

// llmEventStreamDisabledValues lists the AUTONOMY_LLM_EVENTS values that turn
// off raw stream persistence. Anything else (including unset) keeps it on.
var llmEventStreamDisabledValues = map[string]bool{
	"0": true, "false": true, "off": true, "no": true, "disable": true, "disabled": true,
}

// llmEventStreamEnabled reports whether raw llm_events rows should be persisted.
// Default is enabled; set AUTONOMY_LLM_EVENTS=0 (or false/off/no) to store only
// the reason_turns run header and skip the stream.
func llmEventStreamEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AUTONOMY_LLM_EVENTS")))
	return !llmEventStreamDisabledValues[v]
}

// LLMTrace records one LLM interaction. It opens a reason_turns row as the run
// header and records the user-input llm_message, appends the provider's stream
// events to llm_events, aggregates the stream into conversation messages
// (thinking blocks, tool calls with their results) and writes each one as soon as
// it completes — logging that row to stderr — then finalizes the header with
// status, usage, and duration and records the assistant llm_message linked to
// that input.
//
// Persistence is best-effort: failures are logged and never fail the model
// call, so observability degrades instead of the run breaking. A trace with no
// active store is a safe no-op.
type LLMTrace struct {
	store  Store
	handle ReasonTurnHandle
	runID  string
	seq    int
	start  time.Time
	buf    []LLMEvent
	active bool
	// events is false when AUTONOMY_LLM_EVENTS disables raw stream persistence;
	// the run header is still recorded, only llm_events writes are skipped.
	events bool
	// aggregator folds the same live events into conversation messages, so the
	// thinking/tool rows are written (and logged) while the run streams instead of
	// only when it ends. nil when the trace is inert.
	aggregator *chatAggregator
}

// BeginLLMTrace opens a run header for one LLM interaction whose input is the
// user's (the runtime's own prompts). The returned trace is always non-nil; call
// Emit for each stream event and Finish exactly once.
func BeginLLMTrace(agent *Agent, taskID string, step int, mode ReasonMode, input string) *LLMTrace {
	return BeginLLMTraceFrom(agent, LLMMessageRoleUser, taskID, step, mode, input)
}

// BeginLLMTraceFrom opens a run header and records who authored the input:
// LLMMessageRoleUser for the runtime's own prompts, LLMMessageRoleAgent when
// another agent delegated this run (a capability handing a sub-task to this
// agent). The input row is the run's first llm_messages row, so this is where a
// delegation becomes visible instead of looking like the user speaking again.
func BeginLLMTraceFrom(agent *Agent, inputRole LLMMessageRole, taskID string, step int, mode ReasonMode, input string) *LLMTrace {
	if inputRole == "" {
		inputRole = LLMMessageRoleUser
	}
	t := &LLMTrace{store: activeStore(), start: time.Now(), events: llmEventStreamEnabled()}
	if t.store == nil {
		return t
	}
	turn := ReasonTurn{
		TaskID:    taskID,
		Step:      step,
		Mode:      mode,
		Input:     input,
		InputRole: inputRole,
		Status:    string(LLMStatusRunning),
		StartedAt: t.start,
	}
	if agent != nil {
		turn.AgentID = agent.ID
		turn.LLMProvider = agent.LLMProvider
		turn.Model = agent.Model
		turn.LLMAgentID = agent.LLMAgentID
	}
	handle, err := t.store.BeginReasonTurn(turn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] begin llm trace: %v\n", err)
		return t
	}
	t.handle = handle
	t.aggregator = newChatAggregator()
	t.active = true
	// The input is an llm_messages row too, so the run's log starts with it.
	logLLMMessage(LLMMessage{Seq: llmMessageSeqUser, Role: inputRole, Content: input})
	return t
}

// Emit appends one stream event. Seq, CreatedAt and ElapsedMS are assigned here
// so adapters only report what the provider actually sent. It is a no-op when
// raw stream persistence is disabled (AUTONOMY_LLM_EVENTS) — but only for the
// raw rows: the aggregated messages are still persisted and logged as they
// complete, because they are the run's conversation rather than its replay.
func (t *LLMTrace) Emit(ev LLMEvent) {
	if t == nil || !t.active {
		return
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	if t.events {
		ev.Seq = t.seq
		t.seq++
		if ev.ElapsedMS == 0 {
			ev.ElapsedMS = time.Since(t.start).Milliseconds()
		}
		t.buf = append(t.buf, ev)
		if len(t.buf) >= llmEventFlushSize {
			t.flush()
		}
	}
	t.emitMessages(t.aggregator.add(ev))
}

// emitMessages persists the aggregated messages that just became complete and
// mirrors each one to stderr. That log line *is* the readable run log: one line
// per llm_messages row (user input, thinking block, tool call + result, assistant
// return) instead of one per token. Both writes are best-effort, like the raw
// stream: a store failure is logged and never fails the model call.
func (t *LLMTrace) emitMessages(messages []LLMMessage) {
	if len(messages) == 0 {
		return
	}
	rows := make([]LLMMessage, 0, len(messages))
	for _, m := range messages {
		m.TurnID = t.handle.TurnID
		m.ParentID = t.handle.InputMessageID
		rows = append(rows, m)
	}
	if err := t.store.AppendLLMMessages(t.handle.TurnID, rows); err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] append llm messages: %v\n", err)
	}
	for _, m := range rows {
		logLLMMessage(m)
	}
}

// Finish flushes remaining events and finalizes the run header. EventCount is
// filled from the number of events observed. It is a no-op when already closed.
func (t *LLMTrace) Finish(res LLMRunResult) {
	if t == nil || !t.active {
		return
	}
	if res.ProviderRunID != "" {
		t.runID = res.ProviderRunID
	}
	if res.StartedAt.IsZero() {
		res.StartedAt = t.start
	}
	if res.EndedAt.IsZero() {
		res.EndedAt = time.Now()
	}
	if res.EventCount == 0 {
		res.EventCount = t.seq
	}
	t.flush()
	// Close whatever the stream left open (a trailing thinking block, a tool call
	// that never returned) before the assistant row is appended, so the return
	// stays the run's last message.
	t.emitMessages(t.aggregator.flush())
	if err := t.store.FinishReasonTurn(t.handle, res); err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] finish llm trace: %v\n", err)
	}
	t.active = false
}

func (t *LLMTrace) flush() {
	if t == nil || !t.active || len(t.buf) == 0 {
		return
	}
	batch := t.buf
	t.buf = nil
	if err := t.store.AppendLLMEvents(t.handle.TurnID, t.runID, batch); err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] append llm events: %v\n", err)
	}
}
