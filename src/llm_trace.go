package autonomy

import (
	"fmt"
	"os"
	"time"
)

// llmEventFlushSize bounds the number of buffered events before a best-effort
// flush, so long streams do not insert one row at a time.
const llmEventFlushSize = 64

// LLMTrace records one LLM interaction. It opens a reason_turns row as the run
// header, appends the provider's stream events to llm_events, then finalizes
// the header with status, usage, and duration.
//
// Persistence is best-effort: failures are logged and never fail the model
// call, so observability degrades instead of the run breaking. A trace with no
// active store is a safe no-op.
type LLMTrace struct {
	store  Store
	turnID int64
	runID  string
	seq    int
	start  time.Time
	buf    []LLMEvent
	active bool
}

// BeginLLMTrace opens a run header for one LLM interaction. The returned trace
// is always non-nil; call Emit for each stream event and Finish exactly once.
func BeginLLMTrace(agent *Agent, taskID string, step int, mode ReasonMode, input string) *LLMTrace {
	t := &LLMTrace{store: activeStore(), start: time.Now()}
	if t.store == nil {
		return t
	}
	turn := ReasonTurn{
		TaskID:    taskID,
		Step:      step,
		Mode:      mode,
		Input:     input,
		Status:    string(LLMStatusRunning),
		StartedAt: t.start,
	}
	if agent != nil {
		turn.AgentID = agent.ID
		turn.LLMProvider = agent.LLMProvider
		turn.Model = agent.Model
		turn.LLMAgentID = agent.LLMAgentID
	}
	id, err := t.store.BeginReasonTurn(turn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] begin llm trace: %v\n", err)
		return t
	}
	t.turnID = id
	t.active = true
	return t
}

// Emit appends one stream event. Seq, CreatedAt and ElapsedMS are assigned here
// so adapters only report what the provider actually sent.
func (t *LLMTrace) Emit(ev LLMEvent) {
	if t == nil || !t.active {
		return
	}
	ev.Seq = t.seq
	t.seq++
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	if ev.ElapsedMS == 0 {
		ev.ElapsedMS = time.Since(t.start).Milliseconds()
	}
	t.buf = append(t.buf, ev)
	if len(t.buf) >= llmEventFlushSize {
		t.flush()
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
	if err := t.store.FinishReasonTurn(t.turnID, res); err != nil {
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
	if err := t.store.AppendLLMEvents(t.turnID, t.runID, batch); err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] append llm events: %v\n", err)
	}
}
