package llmrun

import (
	"context"
	"strings"
	"testing"
	"time"
)

func waitDone(t *testing.T, ctx context.Context, d time.Duration) bool {
	t.Helper()
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func TestIdleWatchdogAbortsAfterSilence(t *testing.T) {
	t.Parallel()
	wd := NewIdleWatchdog(context.Background(), 60*time.Millisecond)
	defer wd.Stop()

	if !waitDone(t, wd.Context(), 2*time.Second) {
		t.Fatal("watchdog did not cancel an idle run")
	}
	cause := context.Cause(wd.Context())
	if cause == nil || !strings.Contains(cause.Error(), "idle") {
		t.Fatalf("cause=%v, want an idle-timeout reason", cause)
	}
}

func TestIdleWatchdogSurvivesActivity(t *testing.T) {
	t.Parallel()
	wd := NewIdleWatchdog(context.Background(), 80*time.Millisecond)
	defer wd.Stop()

	// Keep touching for well past the idle gap: a busy run must not be cut.
	for i := 0; i < 15; i++ {
		time.Sleep(20 * time.Millisecond)
		if wd.Context().Err() != nil {
			t.Fatalf("cancelled while active after %d touches: %v", i, context.Cause(wd.Context()))
		}
		wd.Touch()
	}
	// Silence now aborts.
	if !waitDone(t, wd.Context(), 2*time.Second) {
		t.Fatal("watchdog did not abort after activity stopped")
	}
}

func TestIdleWatchdogStopCancelsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	wd := NewIdleWatchdog(context.Background(), time.Hour)
	wd.Stop()
	wd.Stop() // must not panic
	if !waitDone(t, wd.Context(), time.Second) {
		t.Fatal("Stop did not cancel the context")
	}
	// A stopped watchdog ignores later touches instead of resurrecting.
	wd.Touch()
	if wd.Context().Err() == nil {
		t.Fatal("touch after Stop resurrected the context")
	}
}

func TestIdleWatchdogDisabledFollowsParent(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(context.Background())
	wd := NewIdleWatchdog(parent, 0)
	defer wd.Stop()

	time.Sleep(30 * time.Millisecond)
	if err := wd.Context().Err(); err != nil {
		t.Fatalf("disabled watchdog cancelled on its own: %v", err)
	}
	cancel()
	if !waitDone(t, wd.Context(), time.Second) {
		t.Fatal("watchdog context did not follow parent cancellation")
	}
}

func TestTouchThroughContext(t *testing.T) {
	t.Parallel()
	wd := NewIdleWatchdog(context.Background(), 70*time.Millisecond)
	defer wd.Stop()
	ctx := WithIdleWatchdog(wd.Context(), wd)

	for i := 0; i < 12; i++ {
		time.Sleep(15 * time.Millisecond)
		Touch(ctx)
	}
	if ctx.Err() != nil {
		t.Fatalf("touching through ctx did not keep the run alive: %v", context.Cause(ctx))
	}
	// Contexts without a watchdog are a no-op.
	Touch(context.Background())
	Touch(nil)
}

func TestCtxErrPrefersCause(t *testing.T) {
	t.Parallel()
	wd := NewIdleWatchdog(context.Background(), 40*time.Millisecond)
	defer wd.Stop()
	if !waitDone(t, wd.Context(), 2*time.Second) {
		t.Fatal("watchdog did not fire")
	}
	got := CtxErr(wd.Context())
	if got == nil || !strings.Contains(got.Error(), "idle") {
		t.Fatalf("CtxErr=%v, want the idle-timeout cause", got)
	}
	if CtxErr(context.Background()) != nil {
		t.Fatal("CtxErr on a live context should be nil")
	}
}

func TestIdleTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", DefaultIdleTimeout},
		{"90s", 90 * time.Second},
		{"10m", 10 * time.Minute},
		{"nonsense", DefaultIdleTimeout},
		{"0", DefaultIdleTimeout},
		{"-1s", DefaultIdleTimeout},
	}
	for _, tc := range cases {
		t.Setenv("AUTONOMY_LLM_TIMEOUT", tc.env)
		if got := IdleTimeout(); got != tc.want {
			t.Fatalf("IdleTimeout() with %q = %s, want %s", tc.env, got, tc.want)
		}
	}
}
