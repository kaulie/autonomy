package cursor

import "github.com/kaulie/autonomy/src/cursorsdk"

// TraceVerbose reports whether the Cursor bridge is logging its protocol traffic
// (AUTONOMY_CURSOR_TRACE / the SDK's own switch). The runtime asks the harness, so it does
// not have to know the SDK to report what the bridge is doing.
func TraceVerbose() bool { return cursorsdk.TraceVerbose() }
