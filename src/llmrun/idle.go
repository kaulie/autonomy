// Package llmrun holds run-liveness plumbing shared by every LLM backend
// adapter (Cursor SDK bridge, Cline SDK bridge, future providers).
//
// It deliberately knows nothing about any provider: adapters report "activity"
// when their stream yields an event, and this package turns that signal into a
// cancellable context that only fires after a period of silence.
package llmrun

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultIdleTimeout bounds how long one provider run may stay silent before it
// is aborted. It is an inactivity budget, not a cap on total run time.
const DefaultIdleTimeout = 3 * time.Minute

// IdleTimeout returns the configured inactivity budget for one provider run.
// AUTONOMY_LLM_TIMEOUT accepts any positive Go duration ("90s", "10m"); empty,
// unparsable or non-positive values fall back to DefaultIdleTimeout.
func IdleTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("AUTONOMY_LLM_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return DefaultIdleTimeout
}

// idleTimeoutErr is the cancellation cause reported when a run goes silent.
func idleTimeoutErr(idle time.Duration) error {
	return fmt.Errorf("run idle for %s: no provider activity", idle)
}

// IdleWatchdog cancels a context only after idle passes with no Touch. Unlike a
// context deadline it does not cap the total run time: as long as the provider
// keeps producing activity the run may continue, and the clock restarts on
// every Touch.
//
// Publish it with WithIdleWatchdog so stream consumers can report activity
// without threading an extra callback through every call site; Stop it once the
// run is finished.
type IdleWatchdog struct {
	idle   time.Duration
	ctx    context.Context
	cancel context.CancelCauseFunc

	mu       sync.Mutex
	timer    *time.Timer
	deadline time.Time
	fired    bool
}

// NewIdleWatchdog derives a cancellable context from parent. A non-positive
// idle disables the watchdog (the context then only follows parent).
func NewIdleWatchdog(parent context.Context, idle time.Duration) *IdleWatchdog {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	w := &IdleWatchdog{idle: idle, ctx: ctx, cancel: cancel}
	if idle > 0 {
		w.mu.Lock()
		w.deadline = time.Now().Add(idle)
		w.mu.Unlock()
		w.timer = time.AfterFunc(idle, w.onIdle)
	}
	return w
}

// Context is the watched context: done once the run goes idle, the parent is
// cancelled, or Stop is called. context.Cause carries the reason.
func (w *IdleWatchdog) Context() context.Context {
	if w == nil {
		return context.Background()
	}
	return w.ctx
}

// Touch records activity and pushes the idle deadline out by another idle gap.
func (w *IdleWatchdog) Touch() {
	if w == nil || w.idle <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fired {
		return
	}
	w.deadline = time.Now().Add(w.idle)
	w.timer.Reset(w.idle)
}

// Stop releases the watchdog: the timer is disarmed and the context cancelled.
// It is idempotent; call it (defer) once the run is done.
func (w *IdleWatchdog) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.fired = true
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
	w.cancel(nil)
}

// onIdle runs from the timer goroutine. It cancels the context unless a
// concurrent Touch moved the deadline forward, in which case it re-arms for the
// remaining gap.
func (w *IdleWatchdog) onIdle() {
	w.mu.Lock()
	if w.fired {
		w.mu.Unlock()
		return
	}
	if remaining := time.Until(w.deadline); remaining > 0 {
		w.timer.Reset(remaining)
		w.mu.Unlock()
		return
	}
	w.fired = true
	w.mu.Unlock()
	w.cancel(idleTimeoutErr(w.idle))
}

type idleWatchdogKey struct{}

// WithIdleWatchdog publishes w on ctx so stream consumers can report activity.
// A nil watchdog returns ctx unchanged.
func WithIdleWatchdog(ctx context.Context, w *IdleWatchdog) context.Context {
	if w == nil {
		return ctx
	}
	return context.WithValue(ctx, idleWatchdogKey{}, w)
}

// Touch reports provider activity to the watchdog carried by ctx, if any.
func Touch(ctx context.Context) {
	if ctx == nil {
		return
	}
	if w, ok := ctx.Value(idleWatchdogKey{}).(*IdleWatchdog); ok {
		w.Touch()
	}
}

// CtxErr prefers the cancellation cause (e.g. an idle timeout) over the bare
// context error so callers can tell why a run stopped.
func CtxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}
