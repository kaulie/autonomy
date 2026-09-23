package bridgesdk

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Progress logging. The Cursor client traces every stage unconditionally, which is how a
// long-but-alive run is told apart from a hung one; a bridge-backed client needs the same,
// because the bridge itself only logs session lifecycle.
//
// AUTONOMY_LLM_DEBUG=1 (shared with the Cursor SDK traces) adds per-event noise.

var traceStart atomic.Int64

// Trace writes one progress line to stderr.
func Trace(stage, format string, args ...any) {
	start := traceStart.Load()
	if start == 0 {
		now := time.Now().UnixNano()
		if traceStart.CompareAndSwap(0, now) {
			start = now
		} else {
			start = traceStart.Load()
		}
	}
	elapsed := time.Since(time.Unix(0, start)).Round(time.Millisecond)
	fmt.Fprintf(os.Stderr, "[llm %s +%s] %s\n", stage, elapsed, fmt.Sprintf(format, args...))
}

// TraceVerbose reports whether verbose (per-event) tracing is on. It mirrors the
// Cursor client: AUTONOMY_LLM_DEBUG=1 (or true/verbose) enables it, everything
// else keeps the log to heartbeats and stage lines.
func TraceVerbose() bool {
	v := strings.TrimSpace(os.Getenv("AUTONOMY_LLM_DEBUG"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "verbose")
}
