// Package watcher is how autonomy keeps looking at an external object until
// the world reaches a state an agent can continue from.
//
// It is independent the way context_builder and eventgateway are: it does not
// know tasks, inbox, prompts or the runtime. A Probe knows one kind of object
// (a pull request, a deployment, later another asset). The Watcher polls
// registered probes and, when a snapshot's fingerprint moves, emits a Change.
// The runtime maps that Change onto the event gateway (src/watch.go).
//
// Adding a new object is adding a Probe. It is not a new capability.
package watcher

import "strings"

// Object kinds a Probe may watch. These are the same strings Constructs use
// for typed evidence (spec.KindPullRequest / spec.KindDeployment).
const (
	KindPullRequest = "pull_request"
	KindDeployment  = "deployment"
)

// Observation is one snapshot of an object. Keys are probe-defined
// (exists, state, merged, healthy, …). The engine treats the bag as opaque
// except for exists: "false" means the object is not there, so do not watch.
type Observation map[string]string

// Spec is one watch: which kind of object, which instance, what "done" means,
// and which task should hear about it.
type Spec struct {
	Kind   string
	Target string
	Until  string
	TaskID string
	// Fields is the last known snapshot (optional). Watch uses it as the
	// starting fingerprint so the first poll does not re-emit the same state.
	Fields Observation
}

// Change is a fact the Watcher emits when an object's fingerprint moves.
// The runtime turns it into an event-gateway Envelope. The module does not
// ingest, wake agents, or decide the next capability.
type Change struct {
	Source         string
	Type           string
	TaskID         string
	AssetID        string
	Payload        map[string]any
	IdempotencyKey string
}

// View is one active watch, as HTTP lists it.
type View struct {
	ID       string
	Kind     string
	Target   string
	Until    string
	TaskID   string
	Watching bool
	Snapshot Observation
}

// Probe reads one kind of world object and says when that object is "done".
// Implementations live beside this package's engine (pull_request.go,
// deployment.go); they know the object's vocabulary, not event-center's
// HTTP or the deployment control plane's client — those are injected as
// Snapshot functions by the runtime.
type Probe interface {
	Kind() string
	DefaultUntil() string
	Observe(spec Spec) (Observation, error)
	ID(spec Spec, obs Observation) string
	Fingerprint(obs Observation) string
	Done(obs Observation, until string) bool
	Change(spec Spec, obs Observation) Change
}

func present(obs Observation) bool {
	if obs == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(obs["exists"])) {
	case "false", "0", "no":
		return false
	}
	return true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func observationPayload(obs Observation, until string) map[string]any {
	payload := map[string]any{}
	for k, v := range obs {
		if strings.TrimSpace(k) == "" {
			continue
		}
		payload[k] = v
	}
	if until != "" {
		payload["until"] = until
	}
	return payload
}
