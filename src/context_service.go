package autonomy

import ctxsvc "github.com/kaulie/autonomy/src/context"

// ContextStore is the optional port a storage engine implements when it can back
// the Context Service's persistence (spec: /Users/gaolei/agent-policies/
// context_service.md). The upper layer asks the Store for the repository — it
// never reaches for a driver, a dialect or a connection of its own
// (store_ports_test.go). A store that cannot serve context simply does not
// implement it, and the Context Service stays unconfigured.
type ContextStore interface {
	// ContextRepository returns the Context Service repository over the store's
	// database, or nil when the engine cannot provide one.
	ContextRepository() ctxsvc.Repository
}

// buildContextService wires the Context Service onto the runtime's existing
// store. It reuses the engine's own connection (through ContextStore) rather
// than opening a parallel database; when the store is not a ContextStore the
// service is left unconfigured and the context.* capabilities report a typed
// error instead of pretending to work.
func buildContextService(store Store) ctxsvc.Service {
	provider, ok := store.(ContextStore)
	if !ok {
		return nil
	}
	repo := provider.ContextRepository()
	if repo == nil {
		return nil
	}
	return ctxsvc.NewService(repo, ctxsvc.FileSourceLoader{})
}
