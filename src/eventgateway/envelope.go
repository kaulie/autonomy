package eventgateway

import (
	"fmt"
	"strings"
	"time"
)

// Envelope is what an external caller posts: a world fact in the canonical
// shape, before the gateway assigns an id and a received-at. Source-native
// payloads (a GitHub webhook, a deployment-platform notice) are adapted into
// this shape first; V1's only adapter is the identity one, so the HTTP body
// *is* the envelope.
type Envelope struct {
	Source         string         `json:"source"`
	Type           string         `json:"type"`
	Subject        Subject        `json:"subject"`
	Payload        map[string]any `json:"payload"`
	OccurredAt     time.Time      `json:"occurred_at"`
	IdempotencyKey string         `json:"idempotency_key"`
}

// Validate reports why an envelope cannot become an Event. Source and Type are
// the identity of a fact; everything else is optional.
func (e Envelope) Validate() error {
	if strings.TrimSpace(e.Source) == "" {
		return fmt.Errorf("source is required")
	}
	if strings.TrimSpace(e.Type) == "" {
		return fmt.Errorf("type is required")
	}
	return nil
}

// Result is what one Ingest did.
type Result struct {
	// Duplicate is true when an event with the same (source, idempotency_key)
	// was already in the log: the returned Event is the original, and the Sink
	// is not told again.
	Duplicate bool
}
