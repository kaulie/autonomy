package capability

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/capability/spec"
	"github.com/kaulie/autonomy/src/watcher"
)

const (
	WatchName     = "watch"
	WatchDomain   = "world"
	WatchProvider = "autonomy"

	defaultWatchInterval = 15 * time.Second
	minWatchInterval     = 2 * time.Second
	defaultWatchTimeout  = 60 * time.Second
	maxWatchTimeout      = 10 * time.Minute
)

// Watch observes one world object until it reaches `until`, then the runtime
// emits a world event so this task's agent can continue. The object is named
// by kind + target: a pull request, a deployment, later another asset. There
// is one watch capability — a new object is a new Probe on the Watcher, not
// a new `*.watch` construct. It does not merge, deploy, or mutate.
type Watch struct {
	Watches *watcher.Watcher
	// Sleep waits between in-call polls (tests replace it).
	Sleep func(context.Context, time.Duration) error
}

func (Watch) Name() string { return WatchName }

func (Watch) Domain() string { return WatchDomain }

func (Watch) Provider() string { return WatchProvider }

func (Watch) Description() string {
	return `watch one world object until it reaches a state this task can continue from, then the runtime emits a world event. Kind names the object ("pull_request", "deployment", …); target names the instance (a PR URL, a pipeline id). "pr" / "deployment" fill kind+target when omitted. Default is one snapshot plus a background watch (the decision loop is not held). "watch":"true" also polls inside this call until until or timeout. It does NOT merge, deploy, or change the object. Verification stays on the kind's system tool (pr.check, deployment.monitor). output: {"kind","target","until","watching","terminal","exists","state", …probe fields}`
}

func (Watch) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "target", Aliases: []string{"pr", "pr_url", "pull_request", "deployment", "pipeline_id", "asset", "id"}, Required: true, Description: "the object to watch: a pull request URL, a pipeline id, or another probe's identity"},
		{Name: "kind", Aliases: []string{"type"}, Description: `"pull_request" / "deployment" / … — inferred from pr or deployment when omitted`},
		{Name: "task_id", Description: "the task that should be woken when the object reaches until (the runtime fills it)"},
		{Name: "until", Description: "the state the background watch waits for; default depends on kind (pull_request: merged; deployment: succeeded)"},
		{Name: "watch", Description: `"true" to poll inside this call until until or timeout; default false (snapshot + background watch)`},
		{Name: "interval", Description: "seconds between polls (watch=true or the background watch); default 15, minimum 2"},
		{Name: "timeout", Description: "seconds the in-call watch may last (watch=true); default 60, maximum 600"},
	}
}

func (Watch) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "kind", Description: "the object kind that was watched"},
		{Name: "target", Description: "the object identity the probe used"},
		{Name: "exists", Description: `"true" when the object is present`},
		{Name: "state", Description: "the object's current state (probe-defined)"},
		{Name: "watching", Description: `"true" when a background watch will emit a world event at until`},
		{Name: "terminal", Description: `"true" when the object already satisfies until`},
		{Name: "until", Description: "what the watch is waiting for"},
	}
}

func (c Watch) Run(in map[string]string) (map[string]string, error) {
	if c.Watches == nil {
		return nil, fmt.Errorf("%s: watcher is off", WatchName)
	}
	specIn, err := inferWatchSpec(in)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", WatchName, err)
	}
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

	obs, err := c.Watches.Observe(specIn)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", WatchName, err)
	}
	if inCallWatch(in["watch"]) && !c.Watches.Terminal(specIn, obs) && presentWatch(obs) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		for {
			if err := c.sleep(ctx, interval); err != nil {
				break
			}
			next, err := c.Watches.Observe(specIn)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", WatchName, err)
			}
			obs = next
			if c.Watches.Terminal(specIn, obs) || !presentWatch(obs) {
				break
			}
		}
	}

	until := c.Watches.Until(specIn)
	specIn.Until = until
	out := watchOutput(specIn, obs, until)
	out["terminal"] = strconv.FormatBool(c.Watches.Terminal(specIn, obs))
	out["watching"] = "false"
	if c.Watches.Terminal(specIn, obs) || !presentWatch(obs) {
		return out, nil
	}
	specIn.Fields = obs
	if err := c.Watches.Watch(specIn); err != nil {
		return nil, fmt.Errorf("%s: start watch: %w", WatchName, err)
	}
	out["watching"] = "true"
	return out, nil
}

func (c Watch) sleep(ctx context.Context, d time.Duration) error {
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

func inferWatchSpec(in map[string]string) (watcher.Spec, error) {
	kind := strings.TrimSpace(firstNonEmptyWatch(in["kind"], in["type"]))
	pr := firstNonEmptyWatch(in["pr"], in["pr_url"], in["pull_request"])
	deployment := firstNonEmptyWatch(in["deployment"], in["pipeline_id"])
	target := firstNonEmptyWatch(in["target"], pr, deployment, in["asset"], in["id"])
	if kind == "" {
		switch {
		case pr != "":
			kind = watcher.KindPullRequest
		case deployment != "":
			kind = watcher.KindDeployment
		}
	}
	if kind == "" {
		return watcher.Spec{}, fmt.Errorf("kind is required")
	}
	if target == "" {
		return watcher.Spec{}, fmt.Errorf("target is required")
	}
	return watcher.Spec{
		Kind:   kind,
		Target: target,
		Until:  strings.TrimSpace(in["until"]),
		TaskID: strings.TrimSpace(in["task_id"]),
	}, nil
}

func watchOutput(specIn watcher.Spec, obs watcher.Observation, until string) map[string]string {
	out := map[string]string{
		"kind":   specIn.Kind,
		"target": firstNonEmptyWatch(obs["target"], specIn.Target),
		"until":  until,
	}
	for k, v := range obs {
		if _, exists := out[k]; !exists || strings.TrimSpace(out[k]) == "" {
			out[k] = v
		}
	}
	if pr := firstNonEmptyWatch(obs["pr"], specIn.Target); specIn.Kind == watcher.KindPullRequest && pr != "" {
		out["pr"] = pr
	}
	return out
}

func presentWatch(obs watcher.Observation) bool {
	if obs == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(obs["exists"])) {
	case "false", "0", "no":
		return false
	}
	return true
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

func firstNonEmptyWatch(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
