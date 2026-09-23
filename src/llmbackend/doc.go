// Package llmbackend is everything that differs between the LLM backends an autonomy
// agent can run on — Cursor and Cline — in one place.
//
// It owes the rest of the runtime nothing: it does not import the autonomy package (it
// cannot — the runtime imports *it* for these types). What it owns:
//
//   - the vocabulary: which backend an agent runs on (Backend), which provider is behind
//     it (Provider), which reasoning mode a turn is (Mode);
//   - the selection: which backend this process's agents use by default, and the model /
//     provider defaults each one resolves (backend.go);
//   - one provider session per agent: attach (re-attach the session the agent was
//     recorded with, or open a fresh one when the provider no longer has it), one prompt
//     stream, and how it is put down (Close — durable state kept — or Delete) (session.go,
//     cursor.go, cline.go);
//   - the bridges those sessions talk to, one shared client per process
//     (cursor_client.go, cline_client.go);
//   - the neutral event vocabulary a run's stream is recorded as, the per-backend
//     adapters that produce it, and the registry that finds one by provider
//     (events.go, events_kind.go, events_cursor.go, events_cline.go).
//
// What it deliberately does not own: an agent's identity and lifecycle, which task it
// belongs to, what it has already done, when a run starts and what the runtime records —
// that is the autonomy package (docs/session.md, docs/llm-backend.md).
package llmbackend

import (
	"fmt"
	"os"
	"strings"
)

// Backend identifies how an autonomy agent is backed: the provider session a run talks
// through. Local is the offline one — no provider behind it at all.
type Backend string

const (
	Local  Backend = "local"
	Cursor Backend = "cursor"
	Cline  Backend = "cline"
)

// Provider identifies which LLM provider backs an agent.
type Provider string

const (
	ProviderCursor          Provider = "cursor"
	ProviderCline           Provider = "cline"
	ProviderDeepseekHarness Provider = "deepseek_harness"
)

// ProjectRoot returns PROJECT_ROOT; empty or unset is an error for callers that need it.
//
// It lives here because the backend is what looks for its own assets under it (a bridge
// script, a workspace) — the runtime's prompt files are a different reader of the same
// variable.
func ProjectRoot() (string, error) {
	root := strings.TrimSpace(os.Getenv("PROJECT_ROOT"))
	if root == "" {
		return "", fmt.Errorf("PROJECT_ROOT is required")
	}
	return root, nil
}
