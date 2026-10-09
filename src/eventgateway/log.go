package eventgateway

import "sync"

// Log is the gateway's durable-looking memory of events. V1's MemoryLog lives
// in process (a restart forgets); the interface is the seam a Store-backed
// implementation can sit behind later, the way Context Service's Repository
// does, without the gateway knowing about sqlite or postgres.
type Log interface {
	// Append writes ev. duplicate is true when an event with the same
	// (source, idempotency_key) is already there — the returned Event is that
	// original. An empty idempotency key never collides.
	Append(ev Event) (stored Event, duplicate bool, err error)
	Get(id string) (Event, bool, error)
	// List returns matching events in append order (oldest first). After is an
	// exclusive id cursor in that order. Limit <= 0 means the default.
	List(f Filter) ([]Event, error)
	// Recent is the last n events in append order (oldest of those first).
	Recent(n int) ([]Event, error)
}

// Filter selects a window of the log. Empty fields are not constraints.
type Filter struct {
	Source string
	Type   string
	TaskID string
	After  string
	Limit  int
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
	// memoryCap drops the oldest events when the in-process log grows past it,
	// so a noisy source cannot hold the runtime's heap. A Store-backed log
	// would not need this.
	memoryCap = 10000
)

// MemoryLog is the V1 Log: a ring of events in this process.
type MemoryLog struct {
	mu     sync.Mutex
	events []Event
	byID   map[string]int
	byKey  map[string]int
}

// NewMemoryLog builds an empty in-process log.
func NewMemoryLog() *MemoryLog {
	return &MemoryLog{
		byID:  map[string]int{},
		byKey: map[string]int{},
	}
}

func (l *MemoryLog) Append(ev Event) (Event, bool, error) {
	if l == nil {
		return Event{}, false, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev.IdempotencyKey != "" {
		if i, ok := l.byKey[dedupeKey(ev.Source, ev.IdempotencyKey)]; ok {
			return l.events[i], true, nil
		}
	}
	if ev.ID != "" {
		if i, ok := l.byID[ev.ID]; ok {
			return l.events[i], true, nil
		}
	}
	l.events = append(l.events, ev)
	idx := len(l.events) - 1
	l.byID[ev.ID] = idx
	if ev.IdempotencyKey != "" {
		l.byKey[dedupeKey(ev.Source, ev.IdempotencyKey)] = idx
	}
	l.dropOldestLocked()
	return ev, false, nil
}

func (l *MemoryLog) Get(id string) (Event, bool, error) {
	if l == nil || id == "" {
		return Event{}, false, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	i, ok := l.byID[id]
	if !ok {
		return Event{}, false, nil
	}
	return l.events[i], true, nil
}

func (l *MemoryLog) List(f Filter) ([]Event, error) {
	if l == nil {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	matched := make([]Event, 0, len(l.events))
	seenAfter := f.After == ""
	for _, ev := range l.events {
		if !seenAfter {
			if ev.ID == f.After {
				seenAfter = true
			}
			continue
		}
		if !matchFilter(ev, f) {
			continue
		}
		matched = append(matched, ev)
	}
	if f.After == "" && len(matched) > limit {
		matched = matched[len(matched)-limit:]
	} else if len(matched) > limit {
		matched = matched[:limit]
	}
	out := make([]Event, len(matched))
	copy(out, matched)
	return out, nil
}

func (l *MemoryLog) Recent(n int) ([]Event, error) {
	if l == nil {
		return nil, nil
	}
	if n <= 0 {
		n = defaultListLimit
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > len(l.events) {
		n = len(l.events)
	}
	out := make([]Event, n)
	copy(out, l.events[len(l.events)-n:])
	return out, nil
}

func (l *MemoryLog) dropOldestLocked() {
	if len(l.events) <= memoryCap {
		return
	}
	drop := len(l.events) - memoryCap
	for _, ev := range l.events[:drop] {
		delete(l.byID, ev.ID)
		if ev.IdempotencyKey != "" {
			delete(l.byKey, dedupeKey(ev.Source, ev.IdempotencyKey))
		}
	}
	l.events = append([]Event(nil), l.events[drop:]...)
	for i, ev := range l.events {
		l.byID[ev.ID] = i
		if ev.IdempotencyKey != "" {
			l.byKey[dedupeKey(ev.Source, ev.IdempotencyKey)] = i
		}
	}
}

func matchFilter(ev Event, f Filter) bool {
	if f.Source != "" && ev.Source != f.Source {
		return false
	}
	if f.Type != "" && ev.Type != f.Type {
		return false
	}
	if f.TaskID != "" && ev.Subject.TaskID != f.TaskID {
		return false
	}
	return true
}

func dedupeKey(source, key string) string {
	return source + "\x00" + key
}
