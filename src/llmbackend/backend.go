package llmbackend

import (
	"os"
	"sort"
	"strings"
)

// DefaultBackend is the backend this process runs agents on when an agent row does not say
// (AUTONOMY_LLM_BACKEND; anything unknown or unset means Cursor, the default).
func DefaultBackend() Backend {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUTONOMY_LLM_BACKEND"))) {
	case string(Cline), "cline_sdk":
		return Cline
	case string(Codex), "codex_sdk":
		return Codex
	default:
		return Cursor
	}
}

// CloseClients shuts down the process-wide bridge clients (one per harness), the way
// Autonomy.Close ends a runtime, and reports the first error. A harness that holds no
// client of its own does nothing.
func CloseClients() error {
	harnessesMu.RLock()
	registered := make([]Harness, 0, len(harnesses))
	for _, h := range harnesses {
		registered = append(registered, h)
	}
	harnessesMu.RUnlock()
	sort.Slice(registered, func(i, j int) bool { return registered[i].Backend < registered[j].Backend })
	var first error
	for _, h := range registered {
		if h.CloseClient == nil {
			continue
		}
		if err := h.CloseClient(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
