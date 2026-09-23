package llmbackend

import (
	"context"
	"time"
)

// Mode is which kind of turn a run is. Cline has session modes — a planner's cycle
// decides (read-only) while a worker's turn runs with tools — and Cursor has no such
// distinction, so a backend that does not care ignores it.
type Mode string

const (
	// ModePlan is a planner's decision cycle: it reads and plans, it does not act.
	ModePlan Mode = "plan"
	// ModeAgent is anything else the runtime asks of an agent (a capability's delegated
	// prompt, a chat turn): it may run tools.
	ModeAgent Mode = "agent"
)

// Facts is what a session has to know about the agent it serves, read when it is needed:
// they change between turns (the workspace, the model, the recorded session id).
type Facts struct {
	Name      string
	Workspace string // "" means the process cwd
	Model     string
	Backend   Backend
	Provider  Provider
	SessionID string // the provider session the agent row recorded, to re-attach
	FrameSent bool   // the reasoning frame is already in the session
	Ephemeral bool   // a one-shot worker: its session is deleted, not kept
}

// Host is the autonomy agent a Session belongs to, as a backend needs it: what it must
// know, and the facts it writes back. It is defined here, not in the runtime, so the
// dependency points one way — the runtime's *Agent satisfies it, and this package never
// imports the runtime.
type Host interface {
	// Facts reads the agent as it is now.
	Facts() Facts
	// SetBackend records which backend and provider the session ended up on.
	SetBackend(Backend, Provider)
	// SetWorkspace records the workspace a turn asked for (ignored when empty).
	SetWorkspace(string)
	// SetModel records the model a turn asked for (ignored when empty).
	SetModel(string)
	// SetSessionID records the provider session id (the agent row's llm_agent_id), or
	// clears it when a session the provider no longer has is let go.
	SetSessionID(string)
	// SetFrameSent records that the session holds the reasoning frame.
	SetFrameSent(bool)
	// Persist writes the agent row back.
	Persist()
}

// Session is one provider session for one autonomy agent: the whole of what differs
// between Cursor and Cline, behind one door the runtime opens per turn.
//
// Attach is idempotent and re-attaches the session the agent was recorded with — a fresh
// one when the provider no longer has it, because a session that expired while the runtime
// was down is a reason to start talking again, not a reason to fail the turn. Prompt runs
// one turn and streams neutral Events. Dispose puts the session down the way its provider
// wants: Close (durable state kept, resumable) for a resident agent, Delete for a one-shot
// worker.
type Session struct {
	host Host
	impl SessionImpl
}

// New builds the session for an agent, on the backend its facts name.
func New(host Host) *Session {
	if host == nil {
		return nil
	}
	s := &Session{host: host}
	backend := host.Facts().Backend
	harness, ok := harnessFor(backend)
	if !ok {
		// A backend with no harness linked in is a build mistake, not something to guess
		// around: fall back to any registered one so a session still exists, and let the
		// attach report the truth (the missing-harness error is on the session).
		s.impl = missingHarness{backend: backend}
		return s
	}
	s.impl = harness.New(host)
	return s
}

// missingHarness is the session of a backend this process has no harness for: every call
// says so.
type missingHarness struct{ backend Backend }

func (m missingHarness) Attach(context.Context, Mode) (bool, error) {
	return false, harnessMissingErr(m.backend)
}

func (m missingHarness) Prompt(context.Context, string, Mode, func(Event)) (string, RunResult, error) {
	return "", RunResult{Status: StatusError, ErrorMessage: harnessMissingErr(m.backend).Error()}, harnessMissingErr(m.backend)
}

func (m missingHarness) Dispose(context.Context, bool) {}
func (m missingHarness) SessionID() string             { return "" }
func (m missingHarness) Mode() string                  { return "" }
func (m missingHarness) Resumed() bool                 { return false }
func (m missingHarness) ResumedFrom() string           { return "" }

// Attach opens (or re-attaches) the session, idempotently, and reports the provider
// session id it ended up on plus whether that was a session that already existed. A nil
// session attaches nothing.
func (s *Session) Attach(ctx context.Context, mode Mode) (string, bool, error) {
	if s == nil || s.impl == nil {
		return "", false, nil
	}
	resumed, err := s.impl.Attach(ctx, mode)
	if err != nil {
		return "", false, err
	}
	// The label is what a log or a caller can point at: the provider session id once the
	// provider has minted one, the bridge's handle until then.
	return s.impl.SessionID(), resumed, nil
}

// Prompt runs one turn on the attached session and streams neutral events to onEvent
// (nil means "drain the stream, deliver nothing"). mode is the autonomy reasoning mode; a
// backend that distinguishes plan from act maps it onto its own session mode.
func (s *Session) Prompt(ctx context.Context, text string, mode Mode, onEvent func(Event)) (string, RunResult, error) {
	if s == nil || s.impl == nil {
		return "", RunResult{Status: StatusError, ErrorMessage: "no session"}, ErrNoSession
	}
	return s.impl.Prompt(ctx, text, mode, onEvent)
}

// Resumed reports whether the last Attach re-attached a session that already existed, as
// opposed to opening a fresh one. It is false before the first Attach.
func (s *Session) Resumed() bool {
	if s == nil {
		return false
	}
	if s.impl == nil {
		return false
	}
	return s.impl.Resumed()
}

// Mode is the attached session's own mode — Cline's plan/act, which a turn must match —
// and "" for a backend that does not distinguish (Cursor) or before the first attach.
func (s *Session) Mode() string {
	if s == nil || s.impl == nil {
		return ""
	}
	return s.impl.Mode()
}

// ProviderSessionID is the provider session this agent is attached to now — the id the
// agent row records so the next process can continue it. Before a run has started on a
// fresh session the provider has not minted that id yet, so the bridge's own handle stands
// in: it identifies the same session to anyone asking "is this still the one I had?".
func (s *Session) ProviderSessionID() string {
	if s == nil || s.impl == nil {
		return ""
	}
	return s.impl.SessionID()
}

// ResumedFrom is the provider session this attach was asked to continue: the id the agent
// row recorded, handed to the provider so it seeds the conversation. "" when the turn
// opened a fresh session instead.
func (s *Session) ResumedFrom() string {
	if s == nil || s.impl == nil {
		return ""
	}
	return s.impl.ResumedFrom()
}

// PromptText is Prompt without event delivery.
func (s *Session) PromptText(ctx context.Context, text string, mode Mode) (string, error) {
	out, _, err := s.Prompt(ctx, text, mode, nil)
	return out, err
}

// Dispose puts the session down: the provider's own way of ending it, for good. It is safe
// on a session that never attached.
func (s *Session) Dispose(ctx context.Context) {
	if s == nil || s.impl == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.impl.Dispose(cctx, s.host.Facts().Ephemeral)
}
