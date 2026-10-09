package eventgateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Gateway is the module: ingest, list, recent. Adapters and the log are
// replaceable; the default is CanonicalAdapter + MemoryLog. It does not know
// about World, inbox or prompts — the runtime maps an accepted Event onto
// those (src/event_gateway.go).
type Gateway struct {
	log      Log
	adapters []Adapter
	now      func() time.Time
	newID    func() string
}

// Option customises a Gateway (clock, id, log, extra adapters).
type Option func(*Gateway)

func WithLog(log Log) Option {
	return func(g *Gateway) {
		if log != nil {
			g.log = log
		}
	}
}

func WithClock(now func() time.Time) Option {
	return func(g *Gateway) {
		if now != nil {
			g.now = now
		}
	}
}

func WithIDGenerator(gen func() string) Option {
	return func(g *Gateway) {
		if gen != nil {
			g.newID = gen
		}
	}
}

func WithAdapter(a Adapter) Option {
	return func(g *Gateway) {
		if a != nil {
			g.adapters = append([]Adapter{a}, g.adapters...)
		}
	}
}

// New builds a Gateway. CanonicalAdapter is always last, so a more specific
// adapter registered with WithAdapter is tried first.
func New(opts ...Option) *Gateway {
	g := &Gateway{
		log:      NewMemoryLog(),
		adapters: []Adapter{CanonicalAdapter{}},
		now:      func() time.Time { return time.Now().UTC() },
		newID:    mintID,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Ingest normalizes an envelope and appends it. Duplicate (source, idempotency_key)
// returns the original event and Result.Duplicate. It does not notify anyone —
// a listener is the runtime's job after this returns.
func (g *Gateway) Ingest(ctx context.Context, env Envelope) (Event, Result, error) {
	if g == nil {
		return Event{}, Result{}, fmt.Errorf("event gateway is off")
	}
	_ = ctx
	ev, err := g.normalize(env)
	if err != nil {
		return Event{}, Result{}, err
	}
	now := g.now()
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = now
	}
	ev.ReceivedAt = now
	if ev.ID == "" {
		ev.ID = g.newID()
	}
	if ev.Payload == nil {
		ev.Payload = map[string]any{}
	}
	stored, dup, err := g.log.Append(ev)
	if err != nil {
		return Event{}, Result{}, err
	}
	return stored, Result{Duplicate: dup}, nil
}

func (g *Gateway) List(f Filter) ([]Event, error) {
	if g == nil || g.log == nil {
		return nil, nil
	}
	return g.log.List(f)
}

func (g *Gateway) Recent(n int) ([]Event, error) {
	if g == nil || g.log == nil {
		return nil, nil
	}
	return g.log.Recent(n)
}

func (g *Gateway) Get(id string) (Event, bool, error) {
	if g == nil || g.log == nil {
		return Event{}, false, nil
	}
	return g.log.Get(id)
}

func (g *Gateway) normalize(env Envelope) (Event, error) {
	for _, a := range g.adapters {
		if a == nil || !a.Match(env) {
			continue
		}
		return a.Normalize(env)
	}
	return Event{}, fmt.Errorf("no adapter for source %q type %q", env.Source, env.Type)
}

func mintID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "evt-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return "evt-" + hex.EncodeToString(b[:])
}
