package cursorsdk

import (
	"context"
	"strings"

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
}

func (r RunResult) OK() bool { return r.Status == "finished" }

// Run wraps a live Send stream.
type Run struct {
	client  *Client
	agentID string
	stream  *connect.ServerStreamForClient[sdkv1.RunStreamMessage]

	runID        string
	lastOffset   string
	assistant    strings.Builder
	statusMsg    string
	terminal     *RunResult
	drained      bool
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
	if r.terminal != nil {
		return r.terminal, nil
	}
	if err := r.consume(ctx, nil); err != nil {
		// Dropped live stream: fall back to WaitLiveRun when we know run id.
		if r.runID != "" {
			return r.waitLive(ctx)
		}
		return nil, err
	}
	if r.terminal == nil {
		if r.runID != "" {
			return r.waitLive(ctx)
		}
		return nil, sdkErr("run ended without terminal result")
	}
	return r.terminal, nil
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
	resp, err := r.client.agentRPC.WaitLiveRun(ctx, connect.NewRequest(&sdkv1.WaitLiveRunRequest{RunId: r.runID}))
	if err != nil {
		return nil, wrapConnectErr(err)
	}
	rr := runResultFromProto(resp.Msg.GetResult(), "", r.statusMsg)
	r.terminal = &rr
	return r.terminal, nil
}

func (r *Run) consume(ctx context.Context, onEvent func(RunEvent)) error {
	if r.drained {
		return nil
	}
	for r.stream.Receive() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := r.stream.Msg()
		if off := msg.GetOffset(); off != "" {
			r.lastOffset = off
		}
		switch env := msg.GetEnvelope().(type) {
		case nil:
			continue // keepalive / unknown
		case *sdkv1.RunStreamMessage_SdkMessage:
			sm := env.SdkMessage
			payload := structToMap(sm.GetMessage())
			if rid, _ := payload["run_id"].(string); rid != "" && r.runID == "" {
				r.runID = rid
			}
			if rid, _ := payload["runId"].(string); rid != "" && r.runID == "" {
				r.runID = rid
			}
			typ := sm.GetType()
			if typ == "assistant" {
				r.assistant.WriteString(textFromPayload(payload))
			}
			if typ == "status" {
				if m, ok := payload["message"].(string); ok && m != "" {
					r.statusMsg = m
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
			if onEvent != nil {
				onEvent(RunEvent{Type: "result", Payload: map[string]any{"status": rr.Status, "text": rr.Text}, Offset: msg.GetOffset()})
			}
		case *sdkv1.RunStreamMessage_Done:
			r.drained = true
			return r.stream.Err()
		default:
			continue
		}
	}
	r.drained = true
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
