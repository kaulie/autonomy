package claude

import (
	"context"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
	"github.com/kaulie/autonomy/src/llmrun"
)

// A run that keeps streaming outlives an idle budget shorter than the run:
// every stream line is activity, so the watchdog bounds silence, not total time.
func TestStreamActivityResetsIdleWatchdog(t *testing.T) {
	dir := fakeCLI(t, `for i in 1 2 3 4 5 6; do
  echo '{"type":"system","subtype":"init","session_id":"session-1"}'
  sleep 0.1
done
echo '`+success+`'`)
	wd := llmrun.NewIdleWatchdog(context.Background(), 300*time.Millisecond)
	defer wd.Stop()
	ctx := llmrun.WithIdleWatchdog(wd.Context(), wd)

	start := time.Now()
	out, res, err := newSession(&probeHost{facts: llmbackend.Facts{Workspace: dir}}).Prompt(ctx, "test", llmbackend.ModePlan, nil)
	if err != nil || out != "OK" {
		t.Fatalf("run cancelled after %s: out=%q res=%+v err=%v", time.Since(start), out, res, err)
	}
}
