package autonomy

import "sync"

// World is the observable state of assets and of the events that changed them.
type World struct {
	mu           sync.Mutex
	assets       map[string]Asset
	assetManager *AssetManager
	events       []Event
}

var _world *World // only one world instance is allowed

func buildWorld() *World {
	if _world != nil {
		return _world
	}
	_world = &World{
		assetManager: NewAssetManager(),
		events:       make([]Event, 0),
	}
	return _world
}

// StateWriter is an optional World capability used by UpdateWorld.
type StateWriter interface {
	Set(assetID, state string)
}

func (w *World) getWorld() *World {
	return _world
}

// RecordEvent appends one observation. The event gateway is the usual writer
// (src/event_gateway.go); the slice is what a prompt falls back to when the
// gateway itself is off.
func (w *World) RecordEvent(ev Event) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, ev)
}

// RecentEvents is the last n events in append order. n <= 0 means all of them.
func (w *World) RecentEvents(n int) []Event {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if n <= 0 || n > len(w.events) {
		n = len(w.events)
	}
	out := make([]Event, n)
	copy(out, w.events[len(w.events)-n:])
	return out
}
