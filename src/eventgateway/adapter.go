package eventgateway

import "strings"

// Adapter turns an Envelope into a canonical Event. V1 ships the identity
// adapter: the HTTP body already is the envelope. A GitHub webhook or a
// deployment-platform notice becomes another Adapter that Match-es that shape
// and fills Source/Type/Subject/Payload from it — the Gateway, Log and Sink
// do not change.
type Adapter interface {
	Name() string
	Match(Envelope) bool
	Normalize(Envelope) (Event, error)
}

// CanonicalAdapter is the identity adapter: the envelope is already canonical.
type CanonicalAdapter struct{}

func (CanonicalAdapter) Name() string { return "canonical" }

func (CanonicalAdapter) Match(Envelope) bool { return true }

func (CanonicalAdapter) Normalize(env Envelope) (Event, error) {
	if err := env.Validate(); err != nil {
		return Event{}, err
	}
	return Event{
		Source: strings.TrimSpace(env.Source),
		Type:   strings.TrimSpace(env.Type),
		Subject: Subject{
			TaskID:    strings.TrimSpace(env.Subject.TaskID),
			AgentID:   strings.TrimSpace(env.Subject.AgentID),
			AssetID:   strings.TrimSpace(env.Subject.AssetID),
			ActionID:  strings.TrimSpace(env.Subject.ActionID),
			ProjectID: strings.TrimSpace(env.Subject.ProjectID),
		},
		Payload:        env.Payload,
		OccurredAt:     env.OccurredAt,
		IdempotencyKey: strings.TrimSpace(env.IdempotencyKey),
	}, nil
}
