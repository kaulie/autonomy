package autonomy

import _ "embed"

// agentMonitorHTML is the single-page monitoring dashboard, embedded at build
// time so the running service serves it with nothing to deploy beside the
// binary (src/http_server.go's GET /monitor). Keeping it in the same directory
// is what //go:embed requires.
//
//go:embed agent_monitor_ui.html
var agentMonitorHTML string
