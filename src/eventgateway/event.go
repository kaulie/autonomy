// Package eventgateway is the inbound path for world observations.
//
// Autonomy's agents reason over a World (docs/event.md, docs/principles.md): an
// Event is an observable fact, not a workflow step. This package is how a fact
// that happened *outside* the runtime — a deployment finishing, a pull request
// opening, a control-plane status change — becomes such a fact.
//
// It is independent the way context_builder is: it knows nothing about tasks,
// agents, prompts or the runtime. It takes an Envelope, normalizes it through an
// Adapter, appends it to a Log (deduped), and tells a Sink. What the runtime does
// with that (record it on World, inject it into the next prompt, wake an agent)
// is the runtime's, wired in src/event_gateway.go.
//
//	gw := eventgateway.New()
//	ev, result, err := gw.Ingest(ctx, eventgateway.Envelope{Source: "deployment", Type: "deployment.succeeded"})
package eventgateway

import "time"

// Event is one canonical world observation. The fields follow docs/event.md:
// Type / Source / Subject / Payload / Timestamp. The gateway assigns ID and
// ReceivedAt; OccurredAt is when it happened in the world (the envelope, or now).
type Event struct {
	ID             string         `json:"id"`
	Source         string         `json:"source"`
	Type           string         `json:"type"`
	Subject        Subject        `json:"subject,omitempty"`
	Payload        map[string]any `json:"payload,omitempty"`
	OccurredAt     time.Time      `json:"occurred_at"`
	ReceivedAt     time.Time      `json:"received_at"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

// Subject names what the fact is about. Any field may be empty: an event with no
// subject is still a world fact, it just does not point at a particular task or
// asset. The runtime uses TaskID (when set) to wake that task's agent; the other
// fields are for the prompt and for later routing.
type Subject struct {
	TaskID    string `json:"task_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	AssetID   string `json:"asset_id,omitempty"`
	ActionID  string `json:"action_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

// Empty reports whether the subject names nothing.
func (s Subject) Empty() bool {
	return s.TaskID == "" && s.AgentID == "" && s.AssetID == "" && s.ActionID == "" && s.ProjectID == ""
}
