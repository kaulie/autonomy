package bridgesdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
)

// RunEvent is one native event from a bridge run stream. Payload is the provider SDK's own
// payload, verbatim: this package never interprets provider shapes.
type RunEvent struct {
	// Type is the core event type: agent_event, chunk, status, session_snapshot, ended, ...
	Type string
	// SessionID / AgentID identify the Cline session that produced the event.
	SessionID string
	AgentID   string
	// Payload is the event's payload (for agent_event this holds the agent event).
	Payload map[string]any
	// Native is the whole event object as sent by the bridge, for full fidelity.
	Native map[string]any
}

// transport is one bridge process's stdio NDJSON channel: it correlates
// responses with requests and fans run events out to the run that asked for them.
type transport struct {
	stdin io.WriteCloser
	sc    *bufio.Scanner

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan *bridgeResponse
	streams map[string]*runStream
	closed  bool
	err     error
}

type bridgeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type bridgeResponse struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *bridgeError    `json:"error"`
}

type bridgeMessage struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	RequestID string          `json:"requestId"`
	AgentID   string          `json:"agentId"`
	SessionID string          `json:"sessionId"`
	Event     map[string]any  `json:"event"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result"`
	Error     *bridgeError    `json:"error"`
}

func newTransport(stdin io.WriteCloser, sc *bufio.Scanner) *transport {
	return &transport{
		stdin:   stdin,
		sc:      sc,
		pending: map[string]chan *bridgeResponse{},
		streams: map[string]*runStream{},
	}
}

func (t *transport) start() {
	go t.readLoop()
}

// close drops the transport: closing stdin makes the bridge shut down.
func (t *transport) close() {
	pending, streams := t.shutdown(bridgeErr("bridge closed"))
	for _, ch := range pending {
		close(ch)
	}
	for _, s := range streams {
		s.finish(nil, bridgeErr("bridge closed"))
	}
	_ = t.stdin.Close()
}

// shutdown marks the transport closed and steals its waiting calls/streams.
func (t *transport) shutdown(err error) ([]chan *bridgeResponse, []*runStream) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, nil
	}
	t.closed = true
	t.err = err
	pending := make([]chan *bridgeResponse, 0, len(t.pending))
	for _, ch := range t.pending {
		pending = append(pending, ch)
	}
	streams := make([]*runStream, 0, len(t.streams))
	for _, s := range t.streams {
		streams = append(streams, s)
	}
	t.pending = map[string]chan *bridgeResponse{}
	t.streams = map[string]*runStream{}
	return pending, streams
}

func (t *transport) readLoop() {
	for t.sc.Scan() {
		line := t.sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' {
			// Not protocol traffic (a chatty child, for example a test binary
			// acting as a fake bridge): ignore it.
			continue
		}
		var msg bridgeMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			fmt.Fprintf(os.Stderr, "[llm-bridge] unparsable message: %v\n", err)
			continue
		}
		switch msg.Type {
		case "event":
			t.dispatchEvent(msg)
		case "result":
			t.dispatchResult(msg)
		}
	}
	err := t.sc.Err()
	if err == nil {
		err = bridgeErr("bridge stream ended")
	}
	pending, streams := t.shutdown(err)
	for _, ch := range pending {
		close(ch)
	}
	for _, s := range streams {
		s.finish(nil, err)
	}
}

func (t *transport) dispatchEvent(msg bridgeMessage) {
	t.mu.Lock()
	s := t.streams[msg.RequestID]
	t.mu.Unlock()
	if s == nil {
		return // event for a run nobody waits on (cancelled or unknown)
	}
	payload, _ := msg.Event["payload"].(map[string]any)
	s.push(RunEvent{
		Type:      stringOf(msg.Event["type"]),
		SessionID: msg.SessionID,
		AgentID:   msg.AgentID,
		Payload:   payload,
		Native:    msg.Event,
	})
}

func (t *transport) dispatchResult(msg bridgeMessage) {
	t.mu.Lock()
	call, isCall := t.pending[msg.ID]
	stream, isStream := t.streams[msg.ID]
	if isCall {
		delete(t.pending, msg.ID)
	}
	if isStream {
		delete(t.streams, msg.ID)
	}
	t.mu.Unlock()

	if isCall {
		call <- &bridgeResponse{ID: msg.ID, OK: msg.OK, Result: msg.Result, Error: msg.Error}
		return
	}
	if isStream {
		if msg.OK {
			stream.finish(msg.Result, nil)
			return
		}
		stream.finish(nil, rpcErrFrom(msg.Error))
	}
}

func rpcErrFrom(e *bridgeError) error {
	if e == nil {
		return &RPCError{Code: "error", Message: "unknown bridge error"}
	}
	return &RPCError{Code: e.Code, Message: e.Message}
}

// call performs a request/response round trip.
func (t *transport) call(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
	req, err := t.write(ctx, cmd, params)
	if err != nil {
		return nil, err
	}
	select {
	case msg, ok := <-req.ch:
		if !ok {
			return nil, t.closedErr()
		}
		if msg.OK {
			return msg.Result, nil
		}
		return nil, rpcErrFrom(msg.Error)
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, req.id)
		t.mu.Unlock()
		return nil, ctxCause(ctx, ctx.Err())
	}
}

// beginRun issues a streaming request (send) and registers the sink its events
// will be routed to until the run's result arrives.
func (t *transport) beginRun(ctx context.Context, cmd string, params any) (*runStream, error) {
	s := newRunStream()
	if _, err := t.writeStream(ctx, cmd, params, s); err != nil {
		return nil, err
	}
	return s, nil
}

type writtenReq struct {
	id string
	ch chan *bridgeResponse
}

func (t *transport) write(ctx context.Context, cmd string, params any) (*writtenReq, error) {
	t.mu.Lock()
	if t.closed {
		err := t.err
		t.mu.Unlock()
		if err == nil {
			err = bridgeErr("bridge closed")
		}
		return nil, err
	}
	t.nextID++
	req := &writtenReq{id: "req-" + strconv.FormatInt(t.nextID, 10), ch: make(chan *bridgeResponse, 1)}
	t.pending[req.id] = req.ch
	t.mu.Unlock()

	if err := t.writeRequest(req.id, cmd, params); err != nil {
		t.mu.Lock()
		delete(t.pending, req.id)
		t.mu.Unlock()
		return nil, err
	}
	return req, nil
}

func (t *transport) writeStream(ctx context.Context, cmd string, params any, s *runStream) (*writtenReq, error) {
	t.mu.Lock()
	if t.closed {
		err := t.err
		t.mu.Unlock()
		if err == nil {
			err = bridgeErr("bridge closed")
		}
		return nil, err
	}
	t.nextID++
	req := &writtenReq{id: "req-" + strconv.FormatInt(t.nextID, 10)}
	t.streams[req.id] = s
	s.id = req.id
	t.mu.Unlock()

	if err := t.writeRequest(req.id, cmd, params); err != nil {
		t.forgetStream(req.id)
		return nil, err
	}
	return req, nil
}

func (t *transport) writeRequest(id, cmd string, params any) error {
	body, err := json.Marshal(map[string]any{"id": id, "cmd": cmd, "params": params})
	if err != nil {
		return err
	}
	if _, err := t.stdin.Write(append(body, '\n')); err != nil {
		return bridgeErr("write %s: %v", cmd, err)
	}
	return nil
}

func (t *transport) forgetStream(id string) {
	t.mu.Lock()
	delete(t.streams, id)
	t.mu.Unlock()
}

func (t *transport) closedErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	return bridgeErr("bridge closed")
}

// runStream buffers a run's events until the caller drains them, so a slow
// consumer (DB writes, tracing) never blocks the bridge reader.
type runStream struct {
	id string

	mu     sync.Mutex
	events []RunEvent
	result json.RawMessage
	err    error
	done   bool
	// notify is a one-slot wakeup channel: producers signal, consumers re-check
	// state and wait again, which is race-free without a mailbox per event.
	notify chan struct{}
}

func newRunStream() *runStream {
	return &runStream{notify: make(chan struct{}, 1)}
}

func (s *runStream) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *runStream) push(ev RunEvent) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	s.signal()
}

func (s *runStream) finish(result json.RawMessage, err error) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.result = result
	s.err = err
	s.done = true
	s.mu.Unlock()
	s.signal()
}

// next returns the next buffered event; ok is false once the run finished.
func (s *runStream) next(ctx context.Context) (RunEvent, bool, error) {
	for {
		s.mu.Lock()
		if len(s.events) > 0 {
			ev := s.events[0]
			s.events = s.events[1:]
			s.mu.Unlock()
			return ev, true, nil
		}
		if s.done {
			err := s.err
			s.mu.Unlock()
			return RunEvent{}, false, err
		}
		s.mu.Unlock()
		if err := s.wait(ctx); err != nil {
			return RunEvent{}, false, err
		}
	}
}

// waited blocks until the run reaches its terminal result.
func (s *runStream) waited(ctx context.Context) (json.RawMessage, error) {
	for {
		s.mu.Lock()
		if s.done {
			result, err := s.result, s.err
			s.mu.Unlock()
			return result, err
		}
		s.mu.Unlock()
		if err := s.wait(ctx); err != nil {
			return nil, err
		}
	}
}

// wait blocks until new events/results exist or the context ends.
func (s *runStream) wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.notify:
		return nil
	case <-ctx.Done():
		return ctxCause(ctx, ctx.Err())
	}
}

// ctxCause prefers the cancellation cause (an idle timeout, for example).
func ctxCause(ctx context.Context, fallback error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return fallback
}

// stringOf reads a string out of a decoded JSON value.
func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
