package cursorsdk

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Trace logs SDK stage timing to stderr. Always on for hang diagnosis.
// Set AUTONOMY_LLM_DEBUG=0 to silence; AUTONOMY_LLM_DEBUG=1 also logs stream event details.
func Trace(stage, format string, args ...any) {
	if strings.TrimSpace(os.Getenv("AUTONOMY_LLM_DEBUG")) == "0" {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "[cursorsdk %s] %-18s %s\n", time.Now().Format("15:04:05.000"), stage, msg)
}

func TraceVerbose() bool {
	v := strings.TrimSpace(os.Getenv("AUTONOMY_LLM_DEBUG"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "verbose")
}
