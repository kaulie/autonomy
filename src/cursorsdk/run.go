package cursorsdk

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
)

// RunEvent is one event from a run stream.
type RunEvent struct {
	Type    string
	Payload map[string]any
	Offset  string
}

// RunUsage is token usage for one run, as reported by the bridge.
type RunUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	TotalTokens      int64
}

// RunResult is the terminal outcome of a run.
type RunResult struct {
	RunID        string
	AgentID      string
	Status       string
	Text         string
	Model        string
	DurationMS   int64
	ErrorCode    string
	ErrorMessage string
	Usage        RunUsage
}

func (r RunResult) OK() bool { return r.Status == "finished" }

// Run wraps a live Send stream.
type Run struct {
	client  *Client
	agentID string
	stream  *connect.ServerStreamForClient[sdkv1.RunStreamMessage]

	runID      string
	lastOffset string
	assistant  strings.Builder
	statusMsg  string
	terminal   *RunResult
	drained    bool
}

func newRun(client *Client, agentID string, stream *connect.ServerStreamForClient[sdkv1.RunStreamMessage]) *Run {
	return &Run{client: client, agentID: agentID, stream: stream}
}

// Events yields stream events, skipping keepalives and unknown envelopes.
func (r *Run) Events(ctx context.Context) <-chan RunEvent {
	ch := make(chan RunEvent)
	go func() {
		defer close(ch)
		_ = r.consume(ctx, func(ev RunEvent) {
			select {
			case ch <- ev:
			case <-ctx.Done():
			}
		})
	}()
	return ch
}

// Wait drains the stream to a terminal result.
func (r *Run) Wait(ctx context.Context) (*RunResult, error) {
	return r.WaitStream(ctx, nil)
}

// WaitStream is Wait plus a live event callback: it drains the stream to a
// terminal result while invoking onEvent for every event in arrival order.
// onEvent may be nil. When the live stream drops and the run is recovered via
// WaitLiveRun, one synthetic "result" event is emitted so callers still observe
// the terminal state.
func (r *Run) WaitStream(ctx context.Context, onEvent func(RunEvent)) (*RunResult, error) {
	if r.terminal != nil {
		return r.terminal, nil
	}
	started := time.Now()
	Trace("Wait", "begin drain agent_id=%s", r.agentID)
	stopHB := startHeartbeat("Wait", started)
	defer stopHB()

	if err := r.consume(ctx, onEvent); err != nil {
		Trace("Wait", "consume error after %s run_id=%s: %v", time.Since(started).Round(time.Millisecond), r.runID, err)
		// Dropped live stream: fall back to WaitLiveRun when we know run id.
		if r.runID != "" {
			Trace("Wait", "fallback WaitLiveRun run_id=%s", r.runID)
			return r.waitLiveWithEvent(ctx, onEvent)
		}
		return nil, err
	}
	if r.terminal == nil {
		if r.runID != "" {
			Trace("Wait", "no terminal on stream; fallback WaitLiveRun run_id=%s", r.runID)
			return r.waitLiveWithEvent(ctx, onEvent)
		}
		return nil, sdkErr("run ended without terminal result")
	}
	Trace("Wait", "done status=%s run_id=%s text_bytes=%d elapsed=%s",
		r.terminal.Status, r.terminal.RunID, len(r.terminal.Text), time.Since(started).Round(time.Millisecond))
	return r.terminal, nil
}

// waitLiveWithEvent wraps waitLive and emits a synthetic terminal event so
// stream consumers still observe the run closing.
func (r *Run) waitLiveWithEvent(ctx context.Context, onEvent func(RunEvent)) (*RunResult, error) {
	res, err := r.waitLive(ctx)
	if err == nil && onEvent != nil {
		onEvent(RunEvent{Type: "result", Payload: map[string]any{"status": res.Status, "text": res.Text}})
	}
	return res, err
}

// Text drains and returns final assistant text.
func (r *Run) Text(ctx context.Context) (string, error) {
	res, err := r.Wait(ctx)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

func (r *Run) waitLive(ctx context.Context) (*RunResult, error) {
	if err := r.client.ensure(); err != nil {
		return nil, err
	}
	started := time.Now()
	Trace("WaitLiveRun", "rpc begin run_id=%s", r.runID)
	resp, err := r.client.agentRPC.WaitLiveRun(ctx, connect.NewRequest(&sdkv1.WaitLiveRunRequest{RunId: r.runID}))
	if err != nil {
		Trace("WaitLiveRun", "rpc error after %s: %v", time.Since(started).Round(time.Millisecond), err)
		return nil, wrapConnectErr(err)
	}
	rr := runResultFromProto(resp.Msg.GetResult(), "", r.statusMsg)
	r.terminal = &rr
	Trace("WaitLiveRun", "rpc ok status=%s elapsed=%s", rr.Status, time.Since(started).Round(time.Millisecond))
	return r.terminal, nil
}

func startHeartbeat(stage string, started time.Time) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				Trace(stage, "still waiting… elapsed=%s", time.Since(started).Round(time.Millisecond))
			}
		}
	}()
	return func() { close(done) }
}

func (r *Run) consume(ctx context.Context, onEvent func(RunEvent)) error {
	if r.drained {
		return nil
	}
	n := 0
	for r.stream.Receive() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n++
		msg := r.stream.Msg()
		if off := msg.GetOffset(); off != "" {
			r.lastOffset = off
		}
		switch env := msg.GetEnvelope().(type) {
		case nil:
			if TraceVerbose() {
				Trace("stream", "keepalive/#%d offset=%s", n, msg.GetOffset())
			}
			continue // keepalive / unknown
		case *sdkv1.RunStreamMessage_SdkMessage:
			sm := env.SdkMessage
			payload := structToMap(sm.GetMessage())
			if rid, _ := payload["run_id"].(string); rid != "" && r.runID == "" {
				r.runID = rid
				Trace("stream", "run_id=%s", r.runID)
			}
			if rid, _ := payload["runId"].(string); rid != "" && r.runID == "" {
				r.runID = rid
				Trace("stream", "run_id=%s", r.runID)
			}
			typ := sm.GetType()
			if typ == "assistant" {
				chunk := textFromPayload(payload)
				r.assistant.WriteString(chunk)
				if TraceVerbose() {
					Trace("stream", "assistant +%d bytes (total=%d)", len(chunk), r.assistant.Len())
				}
			} else {
				Trace("stream", "event type=%s run_id=%s", typ, r.runID)
			}
			if typ == "status" {
				if m, ok := payload["message"].(string); ok && m != "" {
					r.statusMsg = m
					Trace("stream", "status message=%q", m)
				}
			}
			if onEvent != nil {
				onEvent(RunEvent{Type: typ, Payload: payload, Offset: msg.GetOffset()})
			}
		case *sdkv1.RunStreamMessage_Result:
			rr := runResultFromProto(env.Result.GetResult(), env.Result.GetErrorCode(), r.statusMsg)
			if rr.Text == "" {
				rr.Text = strings.TrimSpace(r.assistant.String())
			}
			if rr.RunID != "" {
				r.runID = rr.RunID
			}
			r.terminal = &rr
			Trace("stream", "result status=%s err=%s text_bytes=%d", rr.Status, rr.ErrorCode, len(rr.Text))
			if onEvent != nil {
				onEvent(RunEvent{Type: "result", Payload: map[string]any{"status": rr.Status, "text": rr.Text}, Offset: msg.GetOffset()})
			}
		case *sdkv1.RunStreamMessage_InteractionUpdate:
			iu := env.InteractionUpdate
			Trace("stream", "interaction_update type=%s run_id=%s", iu.GetType(), r.runID)
			if onEvent != nil {
				onEvent(RunEvent{
					Type:    "interaction_update:" + iu.GetType(),
					Payload: structToMap(iu.GetUpdate()),
					Offset:  msg.GetOffset(),
				})
			}
		case *sdkv1.RunStreamMessage_Step:
			cs := env.Step
			Trace("stream", "conversation_step type=%s run_id=%s", cs.GetType(), r.runID)
			if onEvent != nil {
				onEvent(RunEvent{
					Type:    "conversation_step:" + cs.GetType(),
					Payload: structToMap(cs.GetStep()),
					Offset:  msg.GetOffset(),
				})
			}
		case *sdkv1.RunStreamMessage_Done:
			r.drained = true
			Trace("stream", "done after %d messages", n)
			return r.stream.Err()
		default:
			continue
		}
	}
	r.drained = true
	Trace("stream", "receive ended after %d messages err=%v", n, r.stream.Err())
	return r.stream.Err()
}

func runResultFromProto(p *sdkv1.RunResult, errorCode, statusMsg string) RunResult {
	if p == nil {
		return RunResult{Status: "error", ErrorMessage: statusMsg}
	}
	status := strings.TrimPrefix(p.GetStatus().String(), "RUN_LIFECYCLE_STATUS_")
	status = strings.ToLower(status)
	model := ""
	if p.GetModel() != nil {
		model = p.GetModel().GetId()
	}
	return RunResult{
		RunID:        p.GetRunId(),
		AgentID:      p.GetAgentId(),
		Status:       status,
		Text:         p.GetResult(),
		Model:        model,
		DurationMS:   int64(p.GetDurationMs()),
		ErrorCode:    errorCode,
		ErrorMessage: statusMsg,
		Usage:        usageFromProto(p.GetUsage()),
	}
}

// usageFromProto maps the bridge's TokenUsage onto RunUsage.
func usageFromProto(u *sdkv1.TokenUsage) RunUsage {
	if u == nil {
		return RunUsage{}
	}
	return RunUsage{
		InputTokens:      u.GetInputTokens(),
		OutputTokens:     u.GetOutputTokens(),
		CacheReadTokens:  u.GetCacheReadTokens(),
		CacheWriteTokens: u.GetCacheWriteTokens(),
		ReasoningTokens:  u.GetReasoningTokens(),
		TotalTokens:      u.GetTotalTokens(),
	}
}

func structToMap(s *structpb.Struct) map[string]any {
	if s == nil {
		return map[string]any{}
	}
	return s.AsMap()
}

func textFromPayload(payload map[string]any) string {
	if t, ok := payload["text"].(string); ok {
		return t
	}
	if msg, ok := payload["message"].(map[string]any); ok {
		if t, ok := msg["text"].(string); ok {
			return t
		}
		if content, ok := msg["content"].([]any); ok {
			var b strings.Builder
			for _, c := range content {
				m, _ := c.(map[string]any)
				if m == nil {
					continue
				}
				if typ, _ := m["type"].(string); typ == "text" || typ == "" {
					if t, ok := m["text"].(string); ok {
						b.WriteString(t)
					}
				}
			}
			return b.String()
		}
	}
	if content, ok := payload["content"].([]any); ok {
		var b strings.Builder
		for _, c := range content {
			m, _ := c.(map[string]any)
			if m == nil {
				continue
			}
			if t, ok := m["text"].(string); ok {
				b.WriteString(t)
			}
		}
		return b.String()
	}
	return ""
}

// Cancel requests cancellation of the in-flight run.
func (r *Run) Cancel(ctx context.Context) error {
	if r.runID == "" {
		return sdkErr("run id unknown; cannot cancel yet")
	}
	if err := r.client.ensure(); err != nil {
		return err
	}
	_, err := r.client.agentRPC.CancelRun(ctx, connect.NewRequest(&sdkv1.CancelRunRequest{RunId: r.runID}))
	return wrapConnectErr(err)
}
