// Package deployment holds deployment-domain capabilities.
//
// The first one is deployment.monitor: observe an in-flight deployment so an
// agent can follow a pipeline rollout and diagnose a problem while it is still
// happening, instead of finding out about it after the fact.
//
// The monitor only observes. It never changes the deployment; it reports state,
// the signals it found, the log evidence behind them and what to look at next
// (see docs/deployment-monitor.md).
package deployment

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// Name is the capability's stable semantic name.
	Name = "deployment.monitor"
	// Domain is the semantic space this capability belongs to.
	Domain = "deployment"
	// Provider is who implements it.
	Provider = "autonomy"

	// EnvEndpoint is the default deployment service base URL, used when the
	// input does not name an endpoint or a full status URL.
	EnvEndpoint = "AUTONOMY_DEPLOYMENT_ENDPOINT"

	defaultTail     = 40
	maxTail         = 500
	defaultInterval = 5 * time.Second
	minInterval     = time.Second
	defaultTimeout  = 60 * time.Second
	maxTimeout      = 10 * time.Minute
	// stallAfter is how long a running deployment may go without its source
	// reporting an update before the monitor calls it stalled.
	stallAfter = 10 * time.Minute
	// maxEvidenceChars bounds the evidence handed back: the decision loop gets
	// the end of the logs (where the failure is), never an unbounded dump.
	maxEvidenceChars = 4000
)

// State is the observed lifecycle state of a deployment.
type State string

const (
	// StateUnknown means the source did not say (or said something unrecognised).
	StateUnknown State = "unknown"
	// StatePending means accepted but not started yet.
	StatePending State = "pending"
	// StateRunning means in progress.
	StateRunning State = "running"
	// StateSucceeded means finished successfully: terminal.
	StateSucceeded State = "succeeded"
	// StateFailed means finished with a failure: terminal.
	StateFailed State = "failed"
)

// state is the reported state, with an unset one defaulting to unknown.
func (s Snapshot) state() State {
	if s.State == "" {
		return StateUnknown
	}
	return s.State
}

// terminal reports whether no further progress is expected: success or failure.
func (s State) terminal() bool { return s == StateSucceeded || s == StateFailed }

// Request is one monitoring request, parsed from the capability input.
type Request struct {
	// Deployment identifies the deployment/run to follow (required).
	Deployment string
	// Endpoint is the deployment service base URL; StatusURL takes precedence.
	Endpoint string
	// StatusURL is the full status URL; when empty it is derived from Endpoint.
	StatusURL string
	// LogsURL is an optional separate logs URL; when empty it is derived from
	// the status URL.
	LogsURL string
	// Tail is how many recent log lines are kept as evidence.
	Tail int
	// Watch keeps polling until a terminal state or the timeout is reached.
	// The default is one observation, because the decision loop itself provides
	// the repetition: a plan runs deployment.monitor again on the next cycle.
	Watch bool
	// Interval is the delay between polls while watching.
	Interval time.Duration
	// Timeout bounds the whole watch window (ignored when Watch is false).
	Timeout time.Duration
}

// Snapshot is one observation of a deployment: what the source says its state
// is right now, plus the log lines that came with it.
type Snapshot struct {
	ID        string
	State     State
	Phase     string
	Progress  string
	Healthy   *bool
	Error     string
	UpdatedAt time.Time
	Logs      []string
}

// Observer reads deployment state. HTTPObserver is the default; a host may
// inject another source (a CI API, an orchestrator, a local run record) through
// capability.Deps.Deployments.
type Observer interface {
	Observe(ctx context.Context, req Request) (Snapshot, error)
}

// Monitor follows one deployment and reports whether it is healthy, what went
// wrong when it failed, and which evidence to look at next.
type Monitor struct {
	// Observer is optional: when nil, the HTTP observer built from the request
	// (or $AUTONOMY_DEPLOYMENT_ENDPOINT) is used.
	Observer Observer
	// Sleep is optional: the wait between polls, replaced in tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (Monitor) Name() string { return Name }

func (Monitor) Domain() string { return Domain }

func (Monitor) Provider() string { return Provider }

func (Monitor) Description() string {
	return `observe an in-flight deployment and report whether it is progressing, what failed, and which logs explain it; never changes the deployment. input: {"deployment":"<id>"} (required), optional "status_url" or "endpoint" (default $AUTONOMY_DEPLOYMENT_ENDPOINT), "logs_url", "watch":"true", "interval" (s), "timeout" (s), "tail". output: state, phase, progress, healthy, terminal, problem, signals, diagnosis, evidence, suggestions`
}

// Run observes the deployment and turns that observation into a report: the
// state, whether it is a problem, the signals behind that, log evidence and
// next steps. It returns an error only when the deployment cannot be observed
// at all — a failed deployment is a successful observation.
func (m Monitor) Run(in map[string]string) (map[string]string, error) {
	req, err := ParseRequest(in)
	if err != nil {
		return nil, err
	}
	obs := m.Observer
	if obs == nil {
		obs = NewHTTPObserver()
	}
	snap, polls, err := observe(obs, req, m.Sleep)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Name, err)
	}
	return report(snap, req, polls, time.Now()), nil
}

// observe takes one snapshot, or — with Watch — polls until the deployment
// reaches a terminal state or the window closes. The window is bounded twice
// (poll count and wall clock) so a capability call can never block the decision
// loop indefinitely.
func observe(obs Observer, req Request, sleep func(context.Context, time.Duration) error) (Snapshot, int, error) {
	if sleep == nil {
		sleep = defaultSleep
	}
	// A caller may build a Request directly, so the bounds are enforced here too
	// (and a zero interval can never become a division by zero).
	interval := req.Interval
	if interval <= 0 {
		interval = minInterval
	}
	timeout := req.Timeout
	if timeout < 0 {
		timeout = 0
	}
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	maxPolls := 1 + int(timeout/interval)
	var last Snapshot
	var lastErr error
	polls := 0
	for {
		polls++
		snap, err := obs.Observe(ctx, req)
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			last = snap
			if !req.Watch || snap.state().terminal() {
				return snap, polls, nil
			}
		}
		if !req.Watch || polls >= maxPolls || !time.Now().Before(deadline) {
			break
		}
		if err := sleep(ctx, interval); err != nil {
			break
		}
	}
	if lastErr != nil {
		return Snapshot{}, polls, lastErr
	}
	return last, polls, nil
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
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

// ParseRequest reads a monitoring request from capability input. Only the
// deployment is required; everything else has a documented default or bound.
func ParseRequest(in map[string]string) (Request, error) {
	deployment := firstNonEmpty(in["deployment"], in["target"], in["run"], in["id"])
	if deployment == "" {
		return Request{}, fmt.Errorf("%s: missing deployment", Name)
	}
	req := Request{
		Deployment: deployment,
		Endpoint:   strings.TrimSpace(in["endpoint"]),
		StatusURL:  strings.TrimSpace(in["status_url"]),
		LogsURL:    strings.TrimSpace(in["logs_url"]),
		Tail:       defaultTail,
		Watch:      boolInput(in["watch"]),
		Interval:   defaultInterval,
		Timeout:    defaultTimeout,
	}
	if req.Endpoint == "" && req.StatusURL == "" {
		req.Endpoint = strings.TrimSpace(os.Getenv(EnvEndpoint))
	}
	if req.Endpoint == "" && req.StatusURL == "" {
		return Request{}, fmt.Errorf("%s: missing deployment endpoint (set %s, or input.status_url/endpoint)", Name, EnvEndpoint)
	}
	if v := strings.TrimSpace(in["tail"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Request{}, fmt.Errorf("%s: invalid tail %q", Name, v)
		}
		if n > maxTail {
			n = maxTail
		}
		req.Tail = n
	}
	interval, err := secondsInput(in, "interval", defaultInterval, minInterval, 0)
	if err != nil {
		return Request{}, err
	}
	req.Interval = interval
	timeout, err := secondsInput(in, "timeout", defaultTimeout, 0, maxTimeout)
	if err != nil {
		return Request{}, err
	}
	req.Timeout = timeout
	if req.Interval > req.Timeout {
		req.Interval = req.Timeout
	}
	return req, nil
}

// secondsInput parses a positive whole-second input, clamped to [min, max]
// (a zero bound means "no bound").
func secondsInput(in map[string]string, key string, fallback, min, max time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(in[key])
	if v == "" {
		return fallback, nil
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0, fmt.Errorf("%s: invalid %s %q", Name, key, v)
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

func boolInput(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// report renders the observation for the agent: what is happening, whether it
// is a problem, the evidence, and the next steps.
func report(snap Snapshot, req Request, polls int, now time.Time) map[string]string {
	d := Diagnose(snap, now, req.Tail)
	out := map[string]string{
		"deployment":  firstNonEmpty(snap.ID, req.Deployment),
		"state":       string(snap.state()),
		"terminal":    strconv.FormatBool(snap.state().terminal()),
		"problem":     strconv.FormatBool(d.Problem),
		"diagnosis":   d.Summary,
		"evidence":    d.Evidence,
		"observed_at": now.Format(time.RFC3339),
		"polls":       strconv.Itoa(polls),
		"provider":    Provider,
	}
	if snap.Phase != "" {
		out["phase"] = snap.Phase
	}
	if snap.Progress != "" {
		out["progress"] = snap.Progress
	}
	if snap.Healthy != nil {
		out["healthy"] = strconv.FormatBool(*snap.Healthy)
	}
	if snap.Error != "" {
		out["error"] = snap.Error
	}
	if len(d.Signals) > 0 {
		out["signals"] = strings.Join(d.Signals, ",")
	}
	if len(d.Suggestions) > 0 {
		out["suggestions"] = strings.Join(d.Suggestions, "; ")
	}
	return out
}

// Diagnosis is the troubleshooting half of a report: whether the observation is
// a problem, the signals that say so, the log evidence, and what to look at.
type Diagnosis struct {
	Problem     bool
	Signals     []string
	Summary     string
	Evidence    string
	Suggestions []string
}

// logRule matches a known failure fingerprint in a line of deployment output
// and carries the advice that fingerprint implies.
type logRule struct {
	name       string
	re         *regexp.Regexp
	suggestion string
}

// logRules map a line of deployment output to a signal and a next step. They are
// ordered: the first match decides where the evidence window starts, and the
// report lists signals in this order.
var logRules = []logRule{
	{"oom", regexp.MustCompile(`(?i)out of memory|oomkilled|oom kill`), "the workload exceeded its memory limit and was killed: raise the limit or fix the leak, then redeploy"},
	{"image_pull", regexp.MustCompile(`(?i)imagepullbackoff|errimagepull|failed to pull image|manifest unknown|pull access denied`), "the image could not be pulled: check that the tag exists and that the pipeline's registry credentials can read it"},
	{"crash_loop", regexp.MustCompile(`(?i)crashloopbackoff|back-off restarting|exited with (code|status) [1-9]`), "the workload keeps crashing: read the process output above and fix the entrypoint, config or dependency it needs"},
	{"timeout", regexp.MustCompile(`(?i)timed out|timeout|deadline exceeded`), "a step timed out: check the health-check path, readiness probe and step timeout"},
	{"connection", regexp.MustCompile(`(?i)connection refused|no such host|dial tcp|i/o timeout|network is unreachable`), "a dependency was unreachable: verify the service endpoint, DNS and network policy between the pipeline and the target environment"},
	{"permission", regexp.MustCompile(`(?i)permission denied|forbidden|unauthorized|\b401\b|\b403\b`), "access was denied: check the credentials, tokens and roles the pipeline uses for this environment"},
	{"config", regexp.MustCompile(`(?i)missing required|invalid (configuration|value)|validation failed|no such file or directory`), "the configuration or a required file is wrong: fix the manifest/env and redeploy"},
	{"crash", regexp.MustCompile(`(?i)\bpanic\b|\bfatal\b|segmentation fault`), "the process died with a fatal error: hand the stack trace above to the owning service"},
}

// Diagnose turns one snapshot into a diagnosis. Structural facts (a failed
// state, an unhealthy target, a stalled rollout) and log fingerprints both
// contribute signals; every signal contributes evidence and a next step. tail
// is the evidence window size (<=0 means the default).
func Diagnose(snap Snapshot, now time.Time, tail int) Diagnosis {
	d := Diagnosis{}
	add := func(signal, suggestion string) {
		if contains(d.Signals, signal) {
			return
		}
		d.Signals = append(d.Signals, signal)
		if suggestion != "" && !contains(d.Suggestions, suggestion) {
			d.Suggestions = append(d.Suggestions, suggestion)
		}
	}

	switch snap.state() {
	case StateFailed:
		add("deployment_failed", "the deployment failed: fix the cause named above, then redeploy the same artifact")
	case StateRunning, StatePending:
		if !snap.UpdatedAt.IsZero() && now.Sub(snap.UpdatedAt) > stallAfter {
			add("stalled", "the deployment has not reported progress recently: check whether the pipeline step or the target environment is stuck, then decide whether to retry or roll back")
		}
	}
	if snap.Healthy != nil && !*snap.Healthy {
		add("unhealthy", "the deployment reported itself unhealthy: verify the service's health endpoint and its dependencies")
	}

	// The logs are the deployment's own output; the error field is part of it too.
	matchAt := -1
	for i, line := range snap.Logs {
		if ok, rules := matchRules(line); ok {
			for _, r := range rules {
				add(r.name, r.suggestion)
			}
			if matchAt == -1 {
				matchAt = i
			}
		}
	}
	if ok, rules := matchRules(snap.Error); ok {
		for _, r := range rules {
			add(r.name, r.suggestion)
		}
	}

	d.Problem = len(d.Signals) > 0
	d.Evidence = evidence(snap.Logs, matchAt, tail)
	d.Summary = summarize(snap, d)
	if !d.Problem {
		if snap.state().terminal() {
			d.Suggestions = append(d.Suggestions, "the deployment finished with no failure signal in the observed output")
		} else {
			d.Suggestions = append(d.Suggestions, "the deployment is still in progress: observe it again on the next cycle to keep following it")
		}
	}
	return d
}

func matchRules(line string) (bool, []logRule) {
	if strings.TrimSpace(line) == "" {
		return false, nil
	}
	var index []logRule
	for _, r := range logRules {
		if r.re.MatchString(line) {
			index = append(index, r)
		}
	}
	return len(index) > 0, index
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// evidence returns the log window behind the diagnosis: around the first
// matching line when there is one (that is where the failure is), otherwise the
// most recent lines. The result is length-bounded, keeping the end.
func evidence(logs []string, matchAt, tail int) string {
	if len(logs) == 0 {
		return ""
	}
	if tail <= 0 {
		tail = defaultTail
	}
	start, end := len(logs)-tail, len(logs)
	if matchAt >= 0 {
		end = matchAt + 1
		start = end - tail
	}
	if start < 0 {
		start = 0
	}
	if end > len(logs) {
		end = len(logs)
	}
	window := logs[start:end]
	text := strings.Join(window, "\n")
	for len(text) > maxEvidenceChars && len(window) > 1 {
		window = window[1:]
		text = strings.Join(window, "\n")
	}
	if len(text) > maxEvidenceChars {
		text = text[len(text)-maxEvidenceChars:]
	}
	return text
}

// summarize is the one-line story the agent reads first.
func summarize(snap Snapshot, d Diagnosis) string {
	parts := []string{fmt.Sprintf("deployment %s is %s", firstNonEmpty(snap.ID, "?"), snap.state())}
	if snap.Phase != "" {
		parts = append(parts, "phase "+snap.Phase)
	}
	if snap.Progress != "" {
		parts = append(parts, "progress "+snap.Progress)
	}
	if len(d.Signals) > 0 {
		parts = append(parts, "signals: "+strings.Join(d.Signals, ", "))
	}
	if first := firstSignalLine(snap); first != "" {
		parts = append(parts, first)
	}
	return strings.Join(parts, "; ")
}

// firstSignalLine is the first log line (or the reported error) that looks like
// the reason, used to make the summary concrete.
func firstSignalLine(snap Snapshot) string {
	for _, line := range snap.Logs {
		if ok, _ := matchRules(line); ok {
			return strings.TrimSpace(line)
		}
	}
	return strings.TrimSpace(snap.Error)
}
