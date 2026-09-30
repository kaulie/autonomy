// Package contextcap is the agent-facing adapter for the Context Service: it
// exposes context.search / context.get / context.list (spec 13) as capabilities,
// so an agent discovers project context through the runtime rather than SQL.
package contextcap

import (
	"strings"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

const (
	// Domain groups the context discovery capabilities.
	Domain = "context"
	// Provider is the runtime's own attribution for these capabilities.
	Provider = "autonomy"

	// SearchName / GetName / ListName are the capability names agents call.
	SearchName = "context.search"
	GetName    = "context.get"
	ListName   = "context.list"
)

// notConfigured is returned when a capability is registered without a service.
func notConfigured(name string) error {
	return ctxsvc.Errorf(ctxsvc.ErrIndexFailed, "%s: context service is not configured", name)
}

// splitList parses a comma-separated input into trimmed, non-empty values.
func splitList(value string) []string {
	fields := strings.Split(value, ",")
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
