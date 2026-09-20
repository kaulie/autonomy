package autonomy

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Column-value encoding for the postgres engine: how a Go field becomes a
// parameter of a statement, and how a column value comes back. It is the postgres
// side of what sqlite_encoding.go does for sqlite, and it is deliberately its own
// file: the types here are PostgreSQL's (timestamptz, NULL, double precision),
// which is exactly what should *not* leak into src/store.go.

// pgPlaceholders renders the numbered placeholders of one statement: $1, $2, ….
// PostgreSQL numbers its parameters; a query built for a variable number of
// columns therefore has to name them rather than repeat a positional mark.
func pgPlaceholders(n int) string {
	parts := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		parts = append(parts, fmt.Sprintf("$%d", i))
	}
	return strings.Join(parts, ", ")
}

// pgTime is a timestamp for a NOT NULL column: a value nobody set is "now", which
// is what the writer meant when it left the field zero.
func pgTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// pgNullTime is a timestamp for a nullable column: a value nobody set is NULL,
// which stays distinct from any instant.
func pgNullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// pgNullID and the JSON column helpers (orJSONObject / orJSONArray /
// orJSONObjectText) are in src/store_row_text.go: they are the same value rule in
// either engine, so the postgres engine writes its rows through them rather than
// keeping a second copy of the rule.

// pgFloat is a possibly-nil float as a nullable column value.
func pgFloat(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

// pgUsageCost renders usage cost as a nullable column value, keeping unknown cost
// distinct from a genuine zero.
func pgUsageCost(u LLMUsage) any {
	if !u.CostKnown {
		return nil
	}
	return u.CostCents
}

// pgScanTime reads a nullable timestamp: NULL is the zero time, because "the log has
// no time here" is what every reader of it already means for a zero time.
//
// It returns the instant in UTC, and so does every NOT NULL timestamp this engine
// reads. A PostgreSQL session hands a timestamptz back in its own time zone (whatever
// the server or the client's TimeZone setting says), while the sqlite engine's text
// timestamps are always UTC — same instant, different rendering, and a caller that
// prints one (JSON, a log line, a comparison of strings) would see the database it is
// reading. Normalizing here is what makes the read contract say one thing instead of
// two (see store_engines_test.go, which compares the two engines line by line).
func pgScanTime(v sql.NullTime) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return v.Time.UTC()
}

// pgTimeText renders a stored timestamp as the RFC3339Nano UTC text the read
// contract hands out (TurnRecord.started_at / ended_at / created_at in src/store.go,
// and the data API's JSON). A NULL column is the empty string, not a zero instant
// pretending to be one, and the formatting matches the sqlite engine's on purpose:
// the log reads the same whichever engine holds it.
func pgTimeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
