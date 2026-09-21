package db

import . "github.com/kaulie/autonomy/src"

import "time"

// Column-value encoding for the SQLite engine: how a Go field becomes a column
// value, and back. It lives in the engine rather than next to the Store contract
// (src/store.go) because "a nullable column" and "a text timestamp" are database
// choices, not part of what the upper layer asks for. Another engine encodes its
// own rows its own way and never calls these.

// formatTime renders a time as the RFC3339Nano text the engine stores.
func formatTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// nullTimeArg renders a possibly-zero time as a nullable column value.
func nullTimeArg(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

// nullFloatArg renders a possibly-nil float as a nullable column value.
func nullFloatArg(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

// usageCostArg renders usage cost as a nullable column value, keeping unknown
// cost distinct from a genuine zero.
func usageCostArg(u LLMUsage) any {
	if !u.CostKnown {
		return nil
	}
	return u.CostCents
}

// parseTime parses a stored RFC3339Nano timestamp, returning the zero time for
// empty or malformed input.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
