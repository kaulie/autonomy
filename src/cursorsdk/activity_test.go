package cursorsdk

import (
	"context"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/llmrun"

	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
)

// Bare keepalives must not count as progress: a stream that only receives
// keepalives is idle, and the watchdog has to fire.
func TestNoteActivitySkipsKeepalives(t *testing.T) {
	t.Parallel()
	wd := llmrun.NewIdleWatchdog(context.Background(), 70*time.Millisecond)
	defer wd.Stop()
	ctx := llmrun.WithIdleWatchdog(wd.Context(), wd)

	for i := 0; i < 8; i++ {
		time.Sleep(15 * time.Millisecond)
		noteActivity(ctx, &sdkv1.RunStreamMessage{})
	}
	select {
	case <-wd.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("keepalive-only traffic kept an idle run alive")
	}
}

func TestNoteActivityCountsProviderEvents(t *testing.T) {
	t.Parallel()
	wd := llmrun.NewIdleWatchdog(context.Background(), 70*time.Millisecond)
	defer wd.Stop()
	ctx := llmrun.WithIdleWatchdog(wd.Context(), wd)

	event := &sdkv1.RunStreamMessage{Envelope: &sdkv1.RunStreamMessage_Done{}}
	for i := 0; i < 12; i++ {
		time.Sleep(15 * time.Millisecond)
		noteActivity(ctx, event)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("provider events did not keep the run alive: %v", context.Cause(ctx))
	}
}
