// Package eventcenter is autonomy's client for the event-center service
// (https://github.com/kaulie/event-center): the process that accepts GitHub
// webhooks and other producers, persists them, and fans them out over
// cursor pull / push webhook.
//
// Runtime watches the world by reading this service. It does not call GitHub.
package eventcenter

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	// DefaultAPIURL is event-center's contract host (swagger @host / the
	// instance registered with the service registry).
	DefaultAPIURL = "http://127.0.0.1:9099"
	// EnvAPIURL names the event-center base URL.
	EnvAPIURL = "EVENT_CENTER_API_URL"
	// EnvAdminToken is the admin bearer for consume APIs.
	EnvAdminToken = "EVENT_CENTER_ADMIN_TOKEN"
	// EnvAdminTokenAlias is event-center's own token name.
	EnvAdminTokenAlias = "EVENTD_ADMIN_TOKEN"

	StreamGitHub = "github"
	StreamAll    = "all"

	DefaultLookback = 200
	DefaultWait     = 15 * time.Second
)

// Event is one persisted envelope as event-center's pull API returns it.
type Event struct {
	Seq        int64             `json:"seq"`
	StreamSeq  int64             `json:"stream_seq"`
	ID         string            `json:"id"`
	Stream     string            `json:"stream"`
	Provider   string            `json:"provider"`
	Type       string            `json:"type"`
	Subject    string            `json:"subject"`
	SourceTime *time.Time        `json:"source_time"`
	ReceivedAt time.Time         `json:"received_at"`
	DedupeKey  string            `json:"dedupe_key"`
	Headers    map[string]string `json:"headers"`
	DataType   string            `json:"data_type"`
	Data       json.RawMessage   `json:"data"`
}

// ListResult is GET /v1/streams/{stream}/events.
type ListResult struct {
	Events     []Event `json:"events"`
	NextCursor int64   `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// StreamsResult is GET /v1/streams.
type StreamsResult struct {
	Streams   map[string]int64 `json:"streams"`
	GlobalSeq int64            `json:"global_seq"`
	StreamAll string           `json:"stream_all"`
}

// Fact is the world-event shape Runtime ingests: source/type from the
// producer, not from event-center's process identity.
type Fact struct {
	Source         string
	Type           string
	AssetID        string
	Payload        map[string]any
	OccurredAt     time.Time
	IdempotencyKey string
}

// FactFrom maps one event-center row onto a world fact. A merged pull
// request is typed github.pull_request.merged even when GitHub delivered
// pull_request.closed; the idempotency key is stable on (repo, number, state)
// so a watcher's Change and a feed ingest collapse.
func FactFrom(ev Event) Fact {
	payload := map[string]any{}
	if len(ev.Data) > 0 && json.Valid(ev.Data) {
		var obj map[string]any
		if err := json.Unmarshal(ev.Data, &obj); err == nil {
			payload = obj
		} else {
			payload["data"] = string(ev.Data)
		}
	}
	source := firstNonEmpty(ev.Provider, ev.Stream, "event-center")
	typ := strings.TrimSpace(ev.Type)
	asset := strings.TrimSpace(ev.Subject)
	key := firstNonEmpty(ev.DedupeKey, ev.ID)
	if pr, ok := decodeGitHubPR(ev); ok {
		asset = "pull_request:" + strings.ToLower(pr.Repo) + "#" + pr.Number
		if pr.Merged {
			typ = typePullRequestMerged
			key = "github:" + pr.Repo + "#" + pr.Number + ":merged"
		} else if strings.EqualFold(pr.State, "closed") {
			typ = typePullRequestClosed
			key = "github:" + pr.Repo + "#" + pr.Number + ":closed"
		} else {
			typ = typePullRequestUpdated
			key = "github:" + pr.Repo + "#" + pr.Number + ":updated"
		}
	}
	occurred := ev.ReceivedAt
	if ev.SourceTime != nil && !ev.SourceTime.IsZero() {
		occurred = *ev.SourceTime
	}
	return Fact{
		Source:         source,
		Type:           typ,
		AssetID:        asset,
		Payload:        payload,
		OccurredAt:     occurred,
		IdempotencyKey: key,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
