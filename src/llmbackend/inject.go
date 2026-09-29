package llmbackend

// SystemInject is when a harness can place the agent's first prompt — the one that
// locates its role. The words are always the same (the runtime frame). Only the
// timing / channel differs:
//
//   - first_turn: no native system field (Cursor, Claude, Codex) — send as the
//     session's first Prompt;
//   - session: native systemPrompt at session create (Cline) — the first Prompt
//     is already a task turn.
type SystemInject string

const (
	SystemInjectFirstTurn SystemInject = "first_turn"
	SystemInjectSession   SystemInject = "session"
)

// SystemInjectFor is this backend's placement strategy. Unknown / unset harnesses
// send the role prompt as the first turn — the conservative default.
func SystemInjectFor(backend Backend) SystemInject {
	if h, ok := harnessFor(backend); ok && h.SystemInject != "" {
		return h.SystemInject
	}
	return SystemInjectFirstTurn
}

// PlacesSystemAtSession reports that this backend absorbs the role prompt when
// the provider session is created, so the runtime must not also send it as a turn.
func PlacesSystemAtSession(backend Backend) bool {
	return SystemInjectFor(backend) == SystemInjectSession
}
