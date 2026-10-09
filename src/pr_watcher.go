package autonomy

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
	"github.com/kaulie/autonomy/src/eventgateway"
)

const (
	// EnvPRWatchInterval is the background poll interval for pr.watch (seconds).
	EnvPRWatchInterval = "AUTONOMY_PR_WATCH_INTERVAL"

	defaultPRWatchInterval = 15 * time.Second
	minPRWatchInterval     = 2 * time.Second

	sourceGitHub           = "github"
	typePullRequestMerged  = "github.pull_request.merged"
	typePullRequestClosed  = "github.pull_request.closed"
	typePullRequestUpdated = "github.pull_request.updated"
)

// PRWatchView is one active background watch, as HTTP lists it.
type PRWatchView struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	PR       string `json:"pr"`
	Repo     string `json:"repo,omitempty"`
	Number   int    `json:"number,omitempty"`
	TaskID   string `json:"task_id,omitempty"`
	Until    string `json:"until"`
	State    string `json:"state,omitempty"`
	Merged   string `json:"merged,omitempty"`
	Watching bool   `json:"watching"`
}

// WatchPullRequestRequest is POST /api/watches: start watching a pull request
// for a task. kind defaults to pull_request.
type WatchPullRequestRequest struct {
	Kind   string `json:"kind"`
	PR     string `json:"pr"`
	Until  string `json:"until"`
	TaskID string `json:"task_id"`
}

// ListWatchesResponse is GET /api/watches.
type ListWatchesResponse struct {
	Watches []PRWatchView `json:"watches"`
	Count   int           `json:"count"`
}

// PRWatcher is the runtime's background pull-request observer. pr.watch
// registers here; each poll that sees a new state (especially merged) is
// ingested through the event gateway so the named task's agent can continue.
type PRWatcher struct {
	mu       sync.Mutex
	items    map[string]*prWatchItem
	ingest   func(context.Context, IngestEventRequest) (*IngestEventResponse, error)
	snapshot func(pr string) (map[string]string, error)
	interval time.Duration
	stop     chan struct{}
	running  bool
}

type prWatchItem struct {
	spec        sd.PRWatchSpec
	fingerprint string
}

func newPRWatcher() *PRWatcher {
	return &PRWatcher{
		items:    map[string]*prWatchItem{},
		snapshot: defaultPRSnapshot,
		interval: prWatchInterval(),
		stop:     make(chan struct{}),
	}
}

func prWatchInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(EnvPRWatchInterval))
	if raw == "" {
		return defaultPRWatchInterval
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return defaultPRWatchInterval
	}
	d := time.Duration(secs) * time.Second
	if d < minPRWatchInterval {
		return minPRWatchInterval
	}
	return d
}

func defaultPRSnapshot(pr string) (map[string]string, error) {
	return sd.PRCheck{}.Run(map[string]string{"pr": pr})
}

func (w *PRWatcher) SetIngest(fn func(context.Context, IngestEventRequest) (*IngestEventResponse, error)) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ingest = fn
}

func (w *PRWatcher) SetSnapshot(fn func(pr string) (map[string]string, error)) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if fn != nil {
		w.snapshot = fn
	}
}

// WatchPullRequest implements sd.WatchRegistrar: remember this PR and poll
// until it satisfies until. Duplicate (repo, number) watches reuse the slot
// and pick up a newer task_id.
func (w *PRWatcher) WatchPullRequest(spec sd.PRWatchSpec) error {
	if w == nil {
		return fmt.Errorf("pr watcher is off")
	}
	spec.PR = strings.TrimSpace(spec.PR)
	if spec.PR == "" && (spec.Repo == "" || spec.Number == 0) {
		return fmt.Errorf("pr watcher: missing pull request")
	}
	if spec.Until == "" {
		spec.Until = sd.WatchUntilMerged
	}
	id := prWatchID(spec)
	w.mu.Lock()
	w.items[id] = &prWatchItem{spec: spec, fingerprint: prFingerprint(spec.State, spec.Merged)}
	w.ensureLoopLocked()
	w.mu.Unlock()
	return nil
}

func (w *PRWatcher) List() []PRWatchView {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]PRWatchView, 0, len(w.items))
	for id, item := range w.items {
		out = append(out, PRWatchView{
			ID:       id,
			Kind:     "pull_request",
			PR:       item.spec.PR,
			Repo:     item.spec.Repo,
			Number:   item.spec.Number,
			TaskID:   item.spec.TaskID,
			Until:    item.spec.Until,
			State:    item.spec.State,
			Merged:   item.spec.Merged,
			Watching: true,
		})
	}
	return out
}

// PollOnce is one background pass: tests drive it instead of waiting on the
// ticker. Production uses the loop started by WatchPullRequest.
func (w *PRWatcher) PollOnce() {
	if w == nil {
		return
	}
	w.poll()
}

func (w *PRWatcher) Observe(pr string) (map[string]string, error) {
	if w == nil {
		return nil, fmt.Errorf("pr watcher is off")
	}
	w.mu.Lock()
	snap := w.snapshot
	w.mu.Unlock()
	if snap == nil {
		snap = defaultPRSnapshot
	}
	return snap(pr)
}

// StartPRWatch is HTTP's door onto pr.watch: snapshot the pull request, and
// if it is not yet at until, register a background watch bound to task_id.
func (r *Autonomy) StartPRWatch(req WatchPullRequestRequest) (*PRWatchView, error) {
	if r == nil || r.PRWatcher == nil {
		return nil, fmt.Errorf("pr watcher is off")
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "pull_request"
	}
	if kind != "pull_request" {
		return nil, fmt.Errorf("unsupported watch kind %q (want pull_request)", kind)
	}
	pr := strings.TrimSpace(req.PR)
	if pr == "" {
		return nil, fmt.Errorf("pr is required")
	}
	out, err := r.PRWatcher.Observe(pr)
	if err != nil {
		return nil, err
	}
	until := sd.NormalizeUntil(req.Until)
	number, _ := strconv.Atoi(out["number"])
	spec := sd.PRWatchSpec{
		PR:     firstNonEmpty(out["pr"], pr),
		Repo:   out["repo"],
		Number: number,
		TaskID: strings.TrimSpace(req.TaskID),
		Until:  until,
		State:  out["state"],
		Merged: out["merged"],
	}
	view := PRWatchView{
		ID:     prWatchID(spec),
		Kind:   "pull_request",
		PR:     spec.PR,
		Repo:   spec.Repo,
		Number: spec.Number,
		TaskID: spec.TaskID,
		Until:  until,
		State:  spec.State,
		Merged: spec.Merged,
	}
	if out["exists"] != "true" {
		return &view, nil
	}
	if watchDone(out, until) {
		return &view, nil
	}
	if err := r.PRWatcher.WatchPullRequest(spec); err != nil {
		return nil, err
	}
	view.Watching = true
	return &view, nil
}

func (r *Autonomy) ListWatches() ListWatchesResponse {
	if r == nil || r.PRWatcher == nil {
		return ListWatchesResponse{Watches: []PRWatchView{}}
	}
	watches := r.PRWatcher.List()
	if watches == nil {
		watches = []PRWatchView{}
	}
	return ListWatchesResponse{Watches: watches, Count: len(watches)}
}

func (w *PRWatcher) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	close(w.stop)
	w.stop = make(chan struct{})
	w.mu.Unlock()
}

func (w *PRWatcher) ensureLoopLocked() {
	if w.running {
		return
	}
	w.running = true
	if w.stop == nil {
		w.stop = make(chan struct{})
	}
	go w.loop()
}

func (w *PRWatcher) loop() {
	interval := w.interval
	if interval <= 0 {
		interval = defaultPRWatchInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

func (w *PRWatcher) poll() {
	w.mu.Lock()
	items := make([]*prWatchItem, 0, len(w.items))
	ids := make([]string, 0, len(w.items))
	for id, item := range w.items {
		ids = append(ids, id)
		cp := *item
		items = append(items, &cp)
	}
	snap := w.snapshot
	ingest := w.ingest
	w.mu.Unlock()
	if snap == nil {
		return
	}
	for i, item := range items {
		out, err := snap(item.spec.PR)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] pr-watch %s: %v\n", item.spec.PR, err)
			continue
		}
		fp := prFingerprint(out["state"], out["merged"])
		if fp == item.fingerprint {
			continue
		}
		w.mu.Lock()
		if live, ok := w.items[ids[i]]; ok {
			live.fingerprint = fp
			live.spec.State = out["state"]
			live.spec.Merged = out["merged"]
			if pr := strings.TrimSpace(out["pr"]); pr != "" {
				live.spec.PR = pr
			}
			if repo := strings.TrimSpace(out["repo"]); repo != "" {
				live.spec.Repo = repo
			}
			if n, err := strconv.Atoi(out["number"]); err == nil && n > 0 {
				live.spec.Number = n
			}
		}
		w.mu.Unlock()
		if ingest != nil {
			if _, err := ingest(context.Background(), prWatchEnvelope(item.spec, out)); err != nil {
				fmt.Fprintf(os.Stderr, "[autonomy] pr-watch ingest %s: %v\n", item.spec.PR, err)
			}
		}
		if watchDone(out, item.spec.Until) {
			w.mu.Lock()
			delete(w.items, ids[i])
			w.mu.Unlock()
		}
	}
}

func prWatchEnvelope(spec sd.PRWatchSpec, out map[string]string) IngestEventRequest {
	pr := firstNonEmpty(out["pr"], spec.PR)
	repo := firstNonEmpty(out["repo"], spec.Repo)
	number := firstNonEmpty(out["number"], strconv.Itoa(spec.Number))
	typ := typePullRequestUpdated
	key := "github:" + repo + "#" + number + ":updated"
	if out["merged"] == "true" {
		typ = typePullRequestMerged
		key = "github:" + repo + "#" + number + ":merged"
	} else if strings.EqualFold(out["state"], "closed") {
		typ = typePullRequestClosed
		key = "github:" + repo + "#" + number + ":closed"
	}
	payload := map[string]any{
		"pr":     pr,
		"repo":   repo,
		"number": number,
		"state":  out["state"],
		"merged": out["merged"],
		"title":  out["title"],
		"until":  spec.Until,
	}
	return IngestEventRequest{
		Source: sourceGitHub,
		Type:   typ,
		Subject: eventgateway.Subject{
			TaskID:  spec.TaskID,
			AssetID: "pull_request:" + repo + "#" + number,
		},
		Payload:        payload,
		IdempotencyKey: key,
	}
}

func watchDone(out map[string]string, until string) bool {
	if until == "" {
		until = sd.WatchUntilMerged
	}
	merged := out["merged"] == "true"
	closed := strings.EqualFold(out["state"], "closed")
	switch until {
	case sd.WatchUntilClosed:
		return closed
	case sd.WatchUntilTerminal:
		return merged || closed
	default:
		// until=merged: a closed-unmerged PR will not merge; stop watching.
		return merged || closed
	}
}

func prWatchID(spec sd.PRWatchSpec) string {
	if spec.Repo != "" && spec.Number > 0 {
		return strings.ToLower(spec.Repo) + "#" + strconv.Itoa(spec.Number)
	}
	return strings.ToLower(strings.TrimSpace(spec.PR))
}

func prFingerprint(state, merged string) string {
	return strings.ToLower(strings.TrimSpace(state)) + "|" + strings.TrimSpace(merged)
}
