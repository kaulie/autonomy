package autonomy

import (
	"strings"
	"sync"
)

// StopReason is why an in-flight run is being stopped.
//
// It travels with the *task* (stopReasons) because the caller that cancels a run is not
// the caller that writes the record: StopTask cancels the context, but the run loop is
// what marks the task stopped (markStopped) and the message it leaves behind is written
// by the door that accepted the stop. Without a carrier the runtime's own stops were
// recorded as the user's — a restart cutting an in-flight run said "the user stopped the
// task", and its task row said only "stopped": the one thing that record exists for (who
// ended it, and why) was wrong exactly when it mattered.
//
// Set by whoever initiates the stop, read by the run's own path, cleared when the run
// leaves in-flight (so a later, unrelated cancellation is not blamed on it).
type StopReason struct {
	// By is stoppedByUser (the door: POST /api/tasks/{id}/stop) or stoppedByRuntime
	// (this process is being restarted / shut down). Anything else is recorded as the
	// former, so the conservative answer is the one that does not overclaim.
	By         string `json:"by"`
	RequestID  string `json:"requestId,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	Version    string `json:"version,omitempty"`
}

const (
	// stoppedByUser is the stop a person asked for.
	stoppedByUser = "user"
	// stoppedByRuntime is a stop this process made for itself: a restart cutting what
	// is in flight during the shutdown grace (src/graceful.go).
	stoppedByRuntime = "runtime"
)

// stopReasons maps a task that is being stopped to why. A sync.Map like inFlightTasks,
// and for the same reason: the writers are doors and the reader is a run.
var stopReasons sync.Map // taskID -> StopReason

func setStopReason(taskID string, reason StopReason) {
	if strings.TrimSpace(taskID) == "" {
		return
	}
	stopReasons.Store(taskID, reason)
}

func clearStopReason(taskID string) {
	if strings.TrimSpace(taskID) == "" {
		return
	}
	stopReasons.Delete(taskID)
}

// stopReasonOf reads the reason a task is being stopped for, if anyone said one.
func stopReasonOf(taskID string) (StopReason, bool) {
	v, ok := stopReasons.Load(taskID)
	if !ok {
		return StopReason{}, false
	}
	reason, ok := v.(StopReason)
	return reason, ok
}

// IsRuntimeStop is whether this stop is the runtime's own (a restart / shutdown).
func (r StopReason) IsRuntimeStop() bool { return r.By == stoppedByRuntime }

// message is the record the agent's inbox keeps of the stop (the stop message's
// content). It is what a later run of that task reads, and what a person reads when
// asking "why did this stop?" — so a runtime stop names the restart it belongs to.
func (r StopReason) message() string {
	if !r.IsRuntimeStop() {
		return "the user stopped the task"
	}
	if strings.TrimSpace(r.RequestID) == "" {
		return "the runtime stopped the task while shutting down"
	}
	out := "the runtime stopped the task for a restart (requestId=" + r.RequestID
	if d := strings.TrimSpace(r.Deployment); d != "" {
		out += ", deployment=" + d
	}
	if v := strings.TrimSpace(r.Version); v != "" {
		out += ", version=" + v
	}
	return out + ")"
}

// errorText is what the stopped task row says it was stopped for (tasks.error). A user
// stop keeps saying "stopped" — that is what it has always said on the door's path, and
// the stop message already names the user; a runtime stop says what it was.
func (r StopReason) errorText() string {
	if !r.IsRuntimeStop() {
		return "stopped"
	}
	if strings.TrimSpace(r.RequestID) == "" {
		return "stopped by the runtime while shutting down"
	}
	return "stopped by the runtime for a restart (requestId=" + r.RequestID + ")"
}
