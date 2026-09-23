package llmbackend

import "github.com/kaulie/autonomy/src/cursorsdk"

// TraceVerbose reports whether the Cursor bridge is logging its protocol traffic
// (AUTONOMY_CURSOR_TRACE / the SDK's own switch). The runtime asks, so it does not have to
// import the SDK to report what the bridge is doing.
func TraceVerbose() bool { return cursorsdk.TraceVerbose() }
