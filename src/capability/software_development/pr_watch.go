package software_development

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/capability/spec"
)

const (
	// WatchName is the pull-request watcher: observe one PR until the world
	// reaches a state the planner can continue from (merged, by default).
	// It does not merge. pr.check is still the system verification tool.
	WatchName     = "pr.watch"
	WatchDomain   = ReviewDomain
	WatchProvider = ReviewProvider

	// WatchUntilMerged is the default: keep watching until the host has merged.
	WatchUntilMerged = "merged"
	// WatchUntilClosed stops when the pull request is closed, merged or not.
	WatchUntilClosed = "closed"
	// WatchUntilTerminal stops on merged or closed.
	WatchUntilTerminal = "terminal"

	defaultWatchInterval = 15 * time.Second
	minWatchInterval     = 2 * time.Second
	defaultWatchTimeout  = 60 * time.Second
	maxWatchTimeout      = 10 * time.Minute
)

// WatchRegistrar starts a background watch of a pull request. The runtime
// implements it (src/pr_watcher.go): it polls the git host and, when the PR
// reaches the wanted state, ingests a world event so the task's agent can
// continue without waiting for the next user instruction.
type WatchRegistrar interface {
	WatchPullRequest(PRWatchSpec) error
}

// PRWatchSpec is one background watch the capability (or HTTP) asked for.
type PRWatchSpec struct {
	PR       string
	Repo     string
	Number   int
	TaskID   string
	Until    string
	Interval time.Duration
	State    string
	Merged   string
}

// PRWatch observes one pull request over time. A single call always returns
// the current snapshot. When the PR is not yet at `until` (default merged),
// it also registers a background watch: later state changes arrive as world
// events (github.pull_request.merged) on the event gateway.
//
// watch=true additionally polls inside this call, bounded by timeout — the
// same shape as deployment.monitor — so a plan can wait a short window and
// still continue in the same cycle when the merge lands quickly.
type PRWatch struct {
	APIURL      string
	Token       string
	GhAuthToken GhAuthTokenFn
	Repo        string
	HTTPClient  *http.Client

	Watches WatchRegistrar
	// Sleep waits between in-call polls (tests replace it).
	Sleep func(context.Context, time.Duration) error
}

func (PRWatch) Name() string { return WatchName }

func (PRWatch) Domain() string { return WatchDomain }

func (PRWatch) Provider() string { return WatchProvider }

func (PRWatch) Description() string {
	return `watch one pull request until the world changes — typically until a human merges it — then the runtime emits a world event so this task's agent can continue. It does NOT merge. Name it by URL: "pr":"https://<host>/owner/name/pull/9". Default is one snapshot plus a background watch (the decision loop is not held). "watch":"true" also polls inside this call until merged/closed or timeout. "until" is "merged" (default) / "closed" / "terminal". output: {"exists","state","merged","watching","terminal","pr","number","repo","until"}`
}

func (PRWatch) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "pr", Aliases: []string{"pr_url", "pull_request"}, Required: true, Kind: spec.KindPullRequest, Description: "the pull request to watch: https://<host>/owner/name/pull/<number>"},
		{Name: "repo", Aliases: []string{"repository"}, Description: "owner/name, when pr does not name the repository"},
		{Name: "task_id", Description: "the task that should be woken when the pull request reaches until (the runtime fills it)"},
		{Name: "until", Description: `"merged" (default) / "closed" / "terminal" — the state the background watch waits for`},
		{Name: "watch", Description: `"true" to poll inside this call until until or timeout; default false (snapshot + background watch)`},
		{Name: "interval", Description: "seconds between polls (watch=true or the background watch); default 15, minimum 2"},
		{Name: "timeout", Description: "seconds the in-call watch may last (watch=true); default 60, maximum 600"},
	}
}

func (PRWatch) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "exists", Description: `"true" when the git host has this pull request`},
		{Name: "state", Description: `the pull request's state: "open" / "closed"`},
		{Name: "merged", Description: `"true" when the host has merged it`},
		{Name: "watching", Description: `"true" when a background watch will emit a world event at until`},
		{Name: "terminal", Description: `"true" when the pull request already satisfies until`},
		{Name: "until", Description: "what the watch is waiting for"},
		{Name: "number", Description: "the pull request number"},
		{Name: "pr", Description: "the pull request's url"},
		{Name: "title", Description: "the pull request's title"},
		{Name: "repo", Description: "owner/name the pull request lives in"},
	}
}

func (c PRWatch) check() PRCheck {
	return PRCheck{
		APIURL:      c.APIURL,
		Token:       c.Token,
		GhAuthToken: c.GhAuthToken,
		Repo:        c.Repo,
		HTTPClient:  c.HTTPClient,
	}
}

func (c PRWatch) Run(in map[string]string) (map[string]string, error) {
	until := NormalizeUntil(in["until"])
	interval, err := watchSeconds(in, "interval", defaultWatchInterval, minWatchInterval, 0)
	if err != nil {
		return nil, err
	}
	timeout, err := watchSeconds(in, "timeout", defaultWatchTimeout, 0, maxWatchTimeout)
	if err != nil {
		return nil, err
	}
	if interval > timeout {
		interval = timeout
	}

	out, err := c.check().Run(in)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", WatchName, err)
	}
	if inCallWatch(in["watch"]) && !watchSatisfied(out, until) && out["exists"] == "true" {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		for {
			if err := c.sleep(ctx, interval); err != nil {
				break
			}
			next, err := c.check().Run(in)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", WatchName, err)
			}
			out = next
			if watchSatisfied(out, until) || out["exists"] != "true" {
				break
			}
		}
	}

	out["until"] = until
	out["terminal"] = strconv.FormatBool(watchSatisfied(out, until))
	out["watching"] = "false"
	if watchSatisfied(out, until) || out["exists"] != "true" {
		return out, nil
	}
	if c.Watches == nil {
		return out, nil
	}
	number, _ := strconv.Atoi(out["number"])
	if err := c.Watches.WatchPullRequest(PRWatchSpec{
		PR:       firstNonEmpty(out["pr"], in[inputPull], in[inputPullURL], in[inputPullAlt]),
		Repo:     firstNonEmpty(out["repo"], in["repo"], in["repository"]),
		Number:   number,
		TaskID:   strings.TrimSpace(in["task_id"]),
		Until:    until,
		Interval: interval,
		State:    out["state"],
		Merged:   out["merged"],
	}); err != nil {
		return nil, fmt.Errorf("%s: start watch: %w", WatchName, err)
	}
	out["watching"] = "true"
	return out, nil
}

func (c PRWatch) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// NormalizeUntil maps a caller-supplied until to merged / closed / terminal.
func NormalizeUntil(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case WatchUntilClosed:
		return WatchUntilClosed
	case WatchUntilTerminal:
		return WatchUntilTerminal
	default:
		return WatchUntilMerged
	}
}

func watchSatisfied(out map[string]string, until string) bool {
	merged := out["merged"] == "true"
	closed := strings.EqualFold(out["state"], "closed")
	switch until {
	case WatchUntilClosed:
		return closed
	case WatchUntilTerminal:
		return merged || closed
	default:
		return merged
	}
}

func inCallWatch(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func watchSeconds(in map[string]string, key string, fallback, min, max time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(in[key])
	if v == "" {
		return fallback, nil
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0, fmt.Errorf("%s: invalid %s %q", WatchName, key, v)
	}
	d := time.Duration(secs) * time.Second
	if min > 0 && d < min {
		d = min
	}
	if max > 0 && d > max {
		d = max
	}
	return d, nil
}
