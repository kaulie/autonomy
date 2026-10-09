package watcher

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	DefaultInterval = 15 * time.Second
	MinInterval     = 2 * time.Second
)

// Watcher is the process's background observer. Probes are registered up
// front; Watch remembers one object; a loop polls every Interval and emits
// a Change when the fingerprint moves. It does not hold a decision cycle.
type Watcher struct {
	mu       sync.Mutex
	probes   map[string]Probe
	items    map[string]*item
	sink     func(Change) error
	interval time.Duration
	stop     chan struct{}
	running  bool
	errf     func(string, ...any)
}

type item struct {
	spec        Spec
	fingerprint string
}

// Option customises a Watcher.
type Option func(*Watcher)

func WithInterval(d time.Duration) Option {
	return func(w *Watcher) {
		if d > 0 {
			if d < MinInterval {
				d = MinInterval
			}
			w.interval = d
		}
	}
}

func WithSink(fn func(Change) error) Option {
	return func(w *Watcher) {
		w.sink = fn
	}
}

func WithErrors(fn func(string, ...any)) Option {
	return func(w *Watcher) {
		if fn != nil {
			w.errf = fn
		}
	}
}

// New builds a Watcher with the given probes. Duplicate kinds: last wins.
func New(probes []Probe, opts ...Option) *Watcher {
	w := &Watcher{
		probes:   map[string]Probe{},
		items:    map[string]*item{},
		interval: DefaultInterval,
		stop:     make(chan struct{}),
		errf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[watcher] "+format+"\n", args...)
		},
	}
	for _, p := range probes {
		if p == nil || strings.TrimSpace(p.Kind()) == "" {
			continue
		}
		w.probes[p.Kind()] = p
	}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}
	return w
}

func (w *Watcher) SetSink(fn func(Change) error) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sink = fn
}

// Kinds are the object types this process can watch.
func (w *Watcher) Kinds() []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.probes))
	for k := range w.probes {
		out = append(out, k)
	}
	return out
}

func (w *Watcher) probe(kind string) (Probe, error) {
	if w == nil {
		return nil, fmt.Errorf("watcher is off")
	}
	kind = strings.TrimSpace(kind)
	w.mu.Lock()
	p := w.probes[kind]
	w.mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("watcher: unknown kind %q", kind)
	}
	return p, nil
}

// Observe is one snapshot of spec's object, through its Probe.
func (w *Watcher) Observe(spec Spec) (Observation, error) {
	p, err := w.probe(spec.Kind)
	if err != nil {
		return nil, err
	}
	spec.Target = strings.TrimSpace(spec.Target)
	if spec.Target == "" {
		return nil, fmt.Errorf("watcher: missing target")
	}
	return p.Observe(spec)
}

// Until is spec.Until, or the Probe's default when the caller omitted it.
func (w *Watcher) Until(spec Spec) string {
	p, err := w.probe(spec.Kind)
	if err != nil {
		return strings.TrimSpace(spec.Until)
	}
	if u := strings.TrimSpace(spec.Until); u != "" {
		return u
	}
	return p.DefaultUntil()
}

// Terminal reports whether obs already satisfies spec.Until for that kind.
func (w *Watcher) Terminal(spec Spec, obs Observation) bool {
	p, err := w.probe(spec.Kind)
	if err != nil {
		return false
	}
	return p.Done(obs, w.Until(spec))
}

// Watch remembers spec and polls until the Probe says it is done. Duplicate
// (kind, identity) watches reuse the slot and pick up a newer task_id.
func (w *Watcher) Watch(spec Spec) error {
	p, err := w.probe(spec.Kind)
	if err != nil {
		return err
	}
	spec.Target = strings.TrimSpace(spec.Target)
	if spec.Target == "" {
		return fmt.Errorf("watcher: missing target")
	}
	if strings.TrimSpace(spec.Until) == "" {
		spec.Until = p.DefaultUntil()
	}
	id := p.ID(spec, spec.Fields)
	if id == "" {
		id = spec.Kind + ":" + strings.ToLower(spec.Target)
	}
	w.mu.Lock()
	w.items[id] = &item{spec: spec, fingerprint: p.Fingerprint(spec.Fields)}
	w.ensureLoopLocked()
	w.mu.Unlock()
	return nil
}

func (w *Watcher) List() []View {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]View, 0, len(w.items))
	for id, it := range w.items {
		out = append(out, View{
			ID:       id,
			Kind:     it.spec.Kind,
			Target:   it.spec.Target,
			Until:    it.spec.Until,
			TaskID:   it.spec.TaskID,
			Watching: true,
			Snapshot: it.spec.Fields,
		})
	}
	return out
}

// PollOnce is one background pass. Tests drive it instead of waiting on the ticker.
func (w *Watcher) PollOnce() {
	if w == nil {
		return
	}
	w.poll()
}

func (w *Watcher) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	close(w.stop)
	w.stop = make(chan struct{})
	w.mu.Unlock()
}

func (w *Watcher) ensureLoopLocked() {
	if w.running {
		return
	}
	w.running = true
	if w.stop == nil {
		w.stop = make(chan struct{})
	}
	go w.loop()
}

func (w *Watcher) loop() {
	interval := w.interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

func (w *Watcher) poll() {
	w.mu.Lock()
	type live struct {
		id    string
		item  item
		probe Probe
	}
	batch := make([]live, 0, len(w.items))
	for id, it := range w.items {
		p := w.probes[it.spec.Kind]
		batch = append(batch, live{id: id, item: *it, probe: p})
	}
	sink := w.sink
	errf := w.errf
	w.mu.Unlock()

	for _, row := range batch {
		if row.probe == nil {
			continue
		}
		obs, err := row.probe.Observe(row.item.spec)
		if err != nil {
			if errf != nil {
				errf("%s %s: %v", row.item.spec.Kind, row.item.spec.Target, err)
			}
			continue
		}
		fp := row.probe.Fingerprint(obs)
		if fp == row.item.fingerprint {
			continue
		}
		w.mu.Lock()
		if cur, ok := w.items[row.id]; ok {
			cur.fingerprint = fp
			cur.spec.Fields = obs
			if t := strings.TrimSpace(obs["target"]); t != "" {
				cur.spec.Target = t
			}
		}
		w.mu.Unlock()
		if sink != nil {
			ch := row.probe.Change(row.item.spec, obs)
			if ch.TaskID == "" {
				ch.TaskID = row.item.spec.TaskID
			}
			if err := sink(ch); err != nil && errf != nil {
				errf("ingest %s %s: %v", row.item.spec.Kind, row.item.spec.Target, err)
			}
		}
		if row.probe.Done(obs, row.item.spec.Until) {
			w.mu.Lock()
			delete(w.items, row.id)
			w.mu.Unlock()
		}
	}
}
