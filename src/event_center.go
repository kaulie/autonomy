package autonomy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kaulie/autonomy/src/eventcenter"
	"github.com/kaulie/autonomy/src/eventgateway"
	"github.com/kaulie/autonomy/src/watcher"
)

const (
	// EnvEventCenter switches the event-center client off ("0", "off", "false", "no").
	EnvEventCenter = "AUTONOMY_EVENT_CENTER"
	// EnvEventCenterStreams is a comma-separated list of streams to tail (default github).
	EnvEventCenterStreams = "AUTONOMY_EVENT_CENTER_STREAMS"
)

// eventCenterFeed tails event-center and turns its events into world facts.
// Cursor is in-process; a restart resumes from a short lookback.
type eventCenterFeed struct {
	client  *eventcenter.Client
	mu      sync.Mutex
	cursor  map[string]int64
	stop    chan struct{}
	running bool
	errf    func(string, ...any)
}

func eventCenterEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvEventCenter))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

func newEventCenterClient() *eventcenter.Client {
	if !eventCenterEnabled() {
		return nil
	}
	return eventcenter.New()
}

func eventCenterStreams() []string {
	raw := strings.TrimSpace(os.Getenv(EnvEventCenterStreams))
	if raw == "" {
		return []string{eventcenter.StreamGitHub}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{eventcenter.StreamGitHub}
	}
	return out
}

func newEventCenterFeed(c *eventcenter.Client) *eventCenterFeed {
	if c == nil {
		return nil
	}
	return &eventCenterFeed{
		client: c,
		cursor: map[string]int64{},
		stop:   make(chan struct{}),
		errf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[autonomy] event-center: "+format+"\n", args...)
		},
	}
}

func (f *eventCenterFeed) Close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running {
		return
	}
	f.running = false
	close(f.stop)
}

func (r *Autonomy) startEventCenterFeed() {
	if r == nil || r.eventCenter == nil {
		return
	}
	f := r.eventCenter
	f.mu.Lock()
	if f.running {
		f.mu.Unlock()
		return
	}
	f.running = true
	f.mu.Unlock()
	go f.loop(r)
}

func (f *eventCenterFeed) loop(r *Autonomy) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-f.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		for _, stream := range eventCenterStreams() {
			if _, err := f.consume(r, ctx, stream, eventcenter.DefaultWait); err != nil {
				if ctx.Err() != nil {
					return
				}
				if f.errf != nil {
					f.errf("%s: %v", stream, err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}
	}
}

// consume pulls one page (or long-polls until wait) and ingests each event.
func (f *eventCenterFeed) consume(r *Autonomy, ctx context.Context, stream string, wait time.Duration) (int, error) {
	if f == nil || f.client == nil {
		return 0, fmt.Errorf("event-center is off")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	after, err := f.after(ctx, stream)
	if err != nil {
		return 0, err
	}
	res, err := f.client.Pull(ctx, stream, after, int64(eventcenter.DefaultLookback), wait)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ev := range res.Events {
		if err := r.ingestEventCenter(ev); err != nil {
			if f.errf != nil {
				f.errf("ingest %s: %v", ev.ID, err)
			}
			continue
		}
		n++
	}
	f.mu.Lock()
	f.cursor[stream] = res.NextCursor
	f.mu.Unlock()
	return n, nil
}

func (f *eventCenterFeed) after(ctx context.Context, stream string) (int64, error) {
	f.mu.Lock()
	cur, ok := f.cursor[stream]
	f.mu.Unlock()
	if ok {
		return cur, nil
	}
	heads, err := f.client.Streams(ctx)
	if err != nil {
		return 0, err
	}
	head := heads.Streams[stream]
	if stream == eventcenter.StreamAll {
		head = heads.GlobalSeq
	}
	after := head - int64(eventcenter.DefaultLookback)
	if after < 0 {
		after = 0
	}
	return after, nil
}

func (r *Autonomy) ingestEventCenter(ev eventcenter.Event) error {
	if r == nil {
		return fmt.Errorf("event gateway is off")
	}
	fact := eventcenter.FactFrom(ev)
	subject := eventgateway.Subject{AssetID: fact.AssetID}
	if id := r.taskIDForEventCenter(ev); id != "" {
		subject.TaskID = id
	}
	_, err := r.IngestWorldEvent(context.Background(), IngestEventRequest{
		Source:         fact.Source,
		Type:           fact.Type,
		Subject:        subject,
		Payload:        fact.Payload,
		OccurredAt:     fact.OccurredAt,
		IdempotencyKey: fact.IdempotencyKey,
	})
	return err
}

func (r *Autonomy) taskIDForEventCenter(ev eventcenter.Event) string {
	if r == nil || r.Watcher == nil {
		return ""
	}
	for _, v := range r.Watcher.List() {
		if strings.TrimSpace(v.TaskID) == "" {
			continue
		}
		if v.Kind == watcher.KindPullRequest && eventcenter.EventMatchesPullRequest(ev, v.Target) {
			return v.TaskID
		}
	}
	return ""
}
