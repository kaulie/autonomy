package llmbackend

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// SessionImpl is one harness's session, as the core drives it. It is exported because a
// harness lives in its own package (src/llmbackend/cursor, src/llmbackend/cline) and
// implements it there — the core never imports a harness.
type SessionImpl interface {
	// Attach opens (or re-attaches) the session and says whether it was an existing one.
	Attach(ctx context.Context, mode Mode) (bool, error)
	// Prompt runs one turn and streams neutral events (onEvent may be nil).
	Prompt(ctx context.Context, text string, mode Mode, onEvent func(Event)) (string, RunResult, error)
	// Dispose puts the session down: Close (durable state kept) or Delete (one-shot).
	Dispose(ctx context.Context, ephemeral bool)
	// SessionID is the provider session attached now ("" when none); before a run has
	// started, the bridge's own handle stands in.
	SessionID() string
	// Mode is the provider session's own mode ("" for a harness without them).
	Mode() string
	// Resumed is whether the last Attach re-attached a session that already existed.
	Resumed() bool
	// ResumedFrom is the session the last Attach was asked to continue ("" for a fresh one).
	ResumedFrom() string
}

// Harness is what a backend package registers: everything the core and the runtime need to
// run an agent on it.
//
// Adding a harness is a package that registers itself here — the database/sql driver idiom
// — plus one blank import so it is linked in (src/llmbackend/all). Nothing in the core, the
// runtime or the prompt changes for it.
type Harness struct {
	// Backend is the name this harness serves (what AUTONOMY_LLM_BACKEND selects).
	Backend Backend
	// Provider is the LLM provider its sessions talk to.
	Provider Provider
	// New builds the session for one agent. It is called once per agent, lazily, on the
	// first turn that needs it.
	New func(host Host) SessionImpl
	// Adapter maps this provider's native run stream into neutral Events.
	Adapter StreamAdapter
	// DefaultModel is the model this harness runs on when nothing else says (empty when
	// the harness itself resolves it at session time — Cline asks the bridge).
	DefaultModel func() string
	// CloseClient shuts down this harness's process-wide bridge client (optional: a
	// harness without one leaves it nil).
	CloseClient func() error
	// Probe checks one set of credentials without an agent behind them: the bridge handshake
	// every time, plus — when live is asked for — one short turn the account itself answers.
	// It is what lets a UI say "this pool entry is usable" before a task is handed to it
	// (src/accounts_service.go). nil = this harness has no cheap probe.
	Probe func(ctx context.Context, creds Creds, live bool) (ProbeResult, error)
}

// Creds is what a probe (and a session) may need to reach a provider: the credential a pool
// account carries, not an environment variable.
type Creds struct {
	Harness string
	Vendor  string
	APIKey  string
	BaseURL string
	Model   string
}

// ProbeResult says what a probe found: what it did (Load: bridge handshaken; Live: a turn was
// actually run), and — for a live probe — the model's answer.
type ProbeResult struct {
	Load   bool
	Live   bool
	Model  string
	Text   string
	Detail string
}

// ProbeHarness probes a set of credentials on one backend, reporting a missing harness by
// name rather than failing silently.
func ProbeHarness(ctx context.Context, backend Backend, creds Creds, live bool) (ProbeResult, error) {
	harness, ok := harnessFor(backend)
	if !ok {
		return ProbeResult{}, harnessMissingErr(backend)
	}
	if harness.Probe == nil {
		return ProbeResult{}, fmt.Errorf("the %s harness cannot be probed: it has no cheap check without an agent", backend)
	}
	return harness.Probe(ctx, creds, live)
}

var (
	harnessesMu sync.RWMutex
	harnesses   = map[Backend]Harness{}
)

// Register makes a harness available to this process. It is called from a harness
// package's init; registering the same backend twice replaces it, so a test can stand in
// for one.
func Register(h Harness) {
	if h.Backend == "" || h.New == nil {
		return
	}
	harnessesMu.Lock()
	defer harnessesMu.Unlock()
	harnesses[h.Backend] = h
	if h.Adapter != nil {
		registerAdapter(h.Adapter)
	}
}

// harnessFor is the registered harness for a backend.
func harnessFor(backend Backend) (Harness, bool) {
	harnessesMu.RLock()
	defer harnessesMu.RUnlock()
	h, ok := harnesses[backend]
	return h, ok
}

// Harnesses names the backends this process can run, in a stable order: what /health and a
// startup log can say without knowing any harness by name.
func Harnesses() []Backend {
	harnessesMu.RLock()
	defer harnessesMu.RUnlock()
	out := make([]Backend, 0, len(harnesses))
	for b := range harnesses {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ModelDefault is the model a backend runs on when nothing else said: its harness's
// default, or "" when the harness has none (the provider then resolves it).
func ModelDefault(backend Backend) string {
	if h, ok := harnessFor(backend); ok && h.DefaultModel != nil {
		return h.DefaultModel()
	}
	return ""
}

// harnessMissingErr says a backend has no harness linked into this process, which is a
// build mistake (a missing blank import), not a runtime condition.
func harnessMissingErr(backend Backend) error {
	if len(Harnesses()) == 0 {
		return fmt.Errorf("no LLM harness is linked into this process: import src/llmbackend/all (or the harness package) for the backend you run")
	}
	return fmt.Errorf("no LLM harness registered for backend %q (linked: %v)", backend, Harnesses())
}
