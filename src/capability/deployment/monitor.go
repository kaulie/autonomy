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

	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/capability/spec"
)

const (
	// Name is the capability's stable semantic name.
	Name = "deployment.monitor"
	// Domain is the semantic space this capability belongs to.
	Domain = "deployment"
	// Provider is who implements it.
	Provider = "autonomy"

	// EnvAPIURL is where the deployment service listens. It is the same
	// variable service.deploy and the gateway use, so one setting points both
	// the trigger and the monitor at the same deployment control plane.
	EnvAPIURL = "DEPLOYMENT_API_URL"
	// DefaultAPIURL is the local deployment control plane, mirroring
	// service.deploy: monitoring works out of the box against the same host.
	DefaultAPIURL = "http://127.0.0.1:4220"

	defaultTail     = 40
	maxTail         = 500
	defaultInterval = 5 * time.Second
	minInterval     = time.Second
	// trailMaxChanges / trailEvidenceLines bound what a watch hands its judge: the most
	// recent changes are kept (older ones are counted), and a change that looks wrong
	// carries at most this many log lines of evidence.
	trailMaxChanges    = 24
	trailEvidenceLines = 3
	defaultTimeout     = 60 * time.Second
	maxTimeout         = 10 * time.Minute
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
	// Deployment identifies the deployment/pipeline run to follow (required).
	Deployment string
	// Endpoint is the deployment service base URL; StatusURL takes precedence.
	Endpoint string
	// StatusURL is the full status URL; when empty it is derived from
	// Endpoint + the deployment id, or from Poll.
	StatusURL string
	// Poll is the relative status path a trigger capability handed back (e.g.
	// service.deploy's `poll`: /api/pipelines/<id>); it is resolved against
	// Endpoint.
	Poll string
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
	// TaskID is the task this observation belongs to; it is recorded on the
	// monitoring agent's run when the observer is agent-backed.
	TaskID string
}

// Snapshot is one observation of a deployment: what the source says its state
// is right now, plus the log lines that came with it.
type Snapshot struct {
	ID             string
	State          State
	Phase          string
	Progress       string
	Healthy        *bool
	Error          string
	Message        string
	Service        string
	Version        string
	DeploymentName string
	UpdatedAt      time.Time
	Logs           []string

	// The Agent* fields carry the monitoring agent's own judgement when the
	// observer is agent-backed (see AgentObserver). They take precedence over
	// the built-in rules, which remain the fallback.
	AgentProblem     *bool
	AgentSignals     []string
	AgentDiagnosis   string
	AgentSuggestions []string
	AgentNote        string
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
	// Observer is the monitoring provider. When nil, the monitor picks one:
	// an agent-backed observer if Agents is set (the monitoring agent observes
	// and judges), otherwise the deterministic HTTP one.
	Observer Observer
	// Agents is the agent broker (Runtime.AcquireAgent) used by the agent-backed
	// observer. When Agents is nil and Observer is nil, the monitor falls back
	// to reading the deployment API directly.
	Agents broker.AgentBroker
	// Sleep is optional: the wait between polls, replaced in tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (Monitor) Name() string { return Name }

func (Monitor) Domain() string { return Domain }

func (Monitor) Provider() string { return Provider }

func (Monitor) Description() string {
	return `observe an in-flight deployment/pipeline and report whether it is progressing, what failed, and which logs explain it; never changes the deployment. input: {"deployment":"<id>"} or {"pipeline_id":"<id>"} (required; "poll" from service.deploy works too), optional "status_url", "endpoint" (default $DEPLOYMENT_API_URL or http://127.0.0.1:4220), "logs_url", "watch":"true", "interval" (s), "timeout" (s), "tail". output: state, phase, progress, healthy, terminal, problem, signals, diagnosis, evidence, suggestions, source`
}

// Inputs / Outputs declare the capability's call signature for {{CONSTRUCTS}}.
func (Monitor) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "deployment", Aliases: []string{"pipeline_id", "pipeline", "request_id", "target", "run", "id"}, Description: "the deployment / pipeline to follow, by the id the deployment control plane knows it as; required unless poll names it"},
		{Name: "poll", Description: "the status path handed back by service.deploy (e.g. /api/pipelines/<id>); naming it is enough on its own and it is joined onto endpoint/status_url"},
		{Name: "status_url", Description: "the full status URL, when it is not <endpoint>/api/pipelines/<deployment>; it wins over endpoint"},
		{Name: "endpoint", Description: "the deployment API base URL; default $DEPLOYMENT_API_URL, else http://127.0.0.1:4220, with the status path <endpoint>/api/pipelines/<deployment>"},
		{Name: "logs_url", Description: "a separate logs URL, for a control plane whose logs are not at <status_url>/logs; a status answer that already carries logs is used instead"},
		{Name: "watch", Description: `"true" polls until the deployment reaches a terminal state or the window closes; the default takes one look only`},
		{Name: "interval", Description: "seconds between polls when watching (default 5, minimum 1)"},
		{Name: "timeout", Description: "the whole observation window in seconds when watching (default 60, maximum 600)"},
		{Name: "tail", Description: "how many trailing log lines to keep as evidence (default 40, maximum 500)"},
		{Name: "task_id", Description: "the task this observation belongs to (the runtime fills it); the monitoring agent's run is attributed to it"},
	}
}

func (Monitor) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "deployment", Description: "the deployment/pipeline that was observed"},
		{Name: "state", Description: "pending / running / succeeded / failed / unknown — what the deployment is doing"},
		{Name: "terminal", Description: `"true" once the state is final, so waiting can stop`},
		{Name: "problem", Description: `"true" when this needs acting on; a succeeded deployment with recovered retries is not a problem`},
		{Name: "signals", Description: "the short machine-readable labels behind the verdict, comma-joined (oom, crash_loop, stalled, unhealthy, deployment_failed, …); empty when there is no problem"},
		{Name: "diagnosis", Description: "one or two sentences: what is happening and why it matters"},
		{Name: "evidence", Description: "the log lines the verdict rests on"},
		{Name: "suggestions", Description: "concrete next steps, semicolon-joined"},
		{Name: "phase", Description: "the stage the pipeline is in, when it reports one"},
		{Name: "progress", Description: "the pipeline's own progress, e.g. 3/5, when it reports one"},
		{Name: "healthy", Description: `"true"/"false" when the deployment reports health`},
		{Name: "error", Description: "the deployment's own failure reason, when it reports one"},
		{Name: "message", Description: "the deployment's own status line"},
		{Name: "service", Description: "the service being deployed, when the pipeline names it"},
		{Name: "version", Description: "the version being deployed, when the pipeline names it"},
		{Name: "deployment_name", Description: "the deployment's name, when the pipeline names it"},
	}
}

// polling is who observes during one monitor call. poll runs every poll of a watch —
// cheap and deterministic, with no agent behind it — and judge produces the one
// observation the call returns and reports. judge is nil when both are the same
// observer (the deterministic reader, or one the host injected).
//
// They differ only when the monitor delegates: an agent-backed monitor polls the
// deployment API itself, and asks its agent **once**, when the observation is over —
// asking a model every five seconds is not what a model is for. What the polls saw on the
// way is not thrown away either: it travels to the agent as a timeline
// (observerWithHistory).
type polling struct {
	poll  Observer
	judge Observer // nil: poll is the one asked for the result
}

// observation is the observer to ask for the result of this call.
func (p polling) observation() Observer {
	if p.judge != nil {
		return p.judge
	}
	return p.poll
}

// observerWithHistory is an Observer that can be told what the polls before it saw, already
// rendered as a timeline (pollTrail.render): the agent-backed observer puts it in its
// prompt, so one question carries the changes in between instead of one question per poll.
type observerWithHistory interface {
	ObserveWithHistory(ctx context.Context, req Request, timeline string) (Snapshot, error)
}

// pollTrail is what a watch saw, as changes rather than as polls: one entry per poll that
// differed materially from the one before it — a different state, phase, progress, message
// or health, or a different local diagnosis. A watch may poll a hundred times while nothing
// changes, and a hundred identical lines is not information; what a monitoring agent is
// asked about is what changed, and the evidence around it.
type pollTrail struct {
	polls   int
	changes []trailChange
	dropped int
	last    string
}

// trailChange is one material change: the observation, the local rules' signals for it, and
// — when they found a problem — the log lines at the end of it.
type trailChange struct {
	Snapshot Snapshot
	Signals  []string
	Evidence []string
}

// observe records one poll, keeping it only when it changed something.
func (t *pollTrail) observe(snap Snapshot, req Request, now time.Time) {
	t.polls++
	d := Diagnose(snap, now, req.Tail)
	key := strings.Join([]string{string(snap.state()), snap.Phase, snap.Progress, snap.Message, fmt.Sprint(snap.Healthy), strings.Join(d.Signals, ",")}, "\x00")
	if key == t.last {
		return
	}
	t.last = key
	change := trailChange{Snapshot: snap, Signals: d.Signals}
	if d.Problem {
		// The evidence a change rests on: the tail of that observation's logs, so a
		// failure that appeared and went away is still in front of the agent.
		change.Evidence = lastLines(snap.Logs, trailEvidenceLines)
	}
	t.changes = append(t.changes, change)
	if len(t.changes) > trailMaxChanges {
		t.changes = t.changes[1:]
		t.dropped++
	}
}

// render is the timeline that travels to the judge: how often the deployment was read, what
// changed, and the evidence around the changes that looked wrong.
func (t pollTrail) render(polls int) string {
	return renderTrail(polls, t.changes, t.dropped)
}

// observer picks the monitoring provider: an explicit one wins, then the agent-backed
// observer when an agent broker is available, then the deterministic HTTP reader. Which
// one it was is the capability's own wiring, not part of the observation (an agent-backed
// observation shows as this step's interaction row: one row per agent).
func (m Monitor) observer() polling {
	if m.Observer != nil {
		// The host's own observer is asked for every poll: its cost is its business.
		return polling{poll: m.Observer}
	}
	if m.Agents != nil {
		agent := &AgentObserver{Agents: m.Agents}
		return polling{poll: agent.base(), judge: agent}
	}
	return polling{poll: NewHTTPObserver()}
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
	obs := m.observer()
	snap, _, err := observe(obs, req, m.Sleep)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Name, err)
	}
	return report(snap, req, time.Now()), nil
}

// observe takes one observation, or — with Watch — polls until the deployment reaches a
// terminal state or the window closes. The window is bounded twice (poll count and wall
// clock) so a capability call can never block the decision loop indefinitely.
//
// One observation asks the observer that decides (the agent, when the monitor delegates).
// A watch polls `p.poll` — the deterministic reader, for a delegated monitor — and asks
// `p.judge` once, when the observation is over, handing it what the polls saw on the way.
// So one `deployment.monitor` call holds at most one agent, asks it once, and still tells
// it about the states in between.
func observe(p polling, req Request, sleep func(context.Context, time.Duration) error) (Snapshot, int, error) {
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

	if !req.Watch {
		snap, err := p.observation().Observe(ctx, req)
		if err != nil {
			return Snapshot{}, 1, err
		}
		return snap, 1, nil
	}

	deadline := time.Now().Add(timeout)
	maxPolls := 1 + int(timeout/interval)
	var last Snapshot
	trail := pollTrail{}
	var lastErr error
	polls := 0
	for {
		polls++
		snap, err := p.poll.Observe(ctx, req)
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			last = snap
			if snap.state().terminal() {
				break
			}
			// What the polls on the way saw is kept as *changes* — one entry per
			// material difference, with the evidence around it — not as a poll log:
			// a watch may poll a hundred times while nothing happens.
			trail.observe(snap, req, time.Now())
		}
		if polls >= maxPolls || !time.Now().Before(deadline) {
			break
		}
		if err := sleep(ctx, interval); err != nil {
			break
		}
	}
	if lastErr != nil {
		return Snapshot{}, polls, lastErr
	}
	// One judge, one question: the observation that settled, told what came before.
	judge := p.observation()
	if withHistory, ok := judge.(observerWithHistory); ok {
		snap, err := withHistory.ObserveWithHistory(ctx, req, trail.render(polls))
		if err != nil {
			return Snapshot{}, polls, err
		}
		return snap, polls, nil
	}
	if p.judge == nil {
		return last, polls, nil
	}
	snap, err := judge.Observe(ctx, req)
	if err != nil {
		return Snapshot{}, polls, err
	}
	return snap, polls, nil
}

// lastLines is the end of a log window: the evidence nearest the observation, bounded so
// one long log cannot fill the prompt.
func lastLines(logs []string, n int) []string {
	if n <= 0 || len(logs) == 0 {
		return nil
	}
	if len(logs) > n {
		logs = logs[len(logs)-n:]
	}
	return logs
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
	// service.deploy hands back a pipeline id (and a poll path); both name the
	// same thing the monitor follows, so all of them are accepted.
	deployment := firstNonEmpty(
		in["deployment"], in["pipeline_id"], in["pipeline"],
		in["request_id"], in["target"], in["run"], in["id"],
	)
	poll := strings.TrimSpace(in["poll"])
	if deployment == "" && poll == "" {
		return Request{}, fmt.Errorf("%s: missing deployment", Name)
	}
	req := Request{
		Deployment: deployment,
		Endpoint:   strings.TrimSpace(in["endpoint"]),
		StatusURL:  strings.TrimSpace(in["status_url"]),
		Poll:       poll,
		LogsURL:    strings.TrimSpace(in["logs_url"]),
		Tail:       defaultTail,
		Watch:      boolInput(in["watch"]),
		Interval:   defaultInterval,
		Timeout:    defaultTimeout,
		TaskID:     strings.TrimSpace(in["task_id"]),
	}
	if req.Endpoint == "" && req.StatusURL == "" {
		req.Endpoint = firstNonEmpty(os.Getenv(EnvAPIURL), DefaultAPIURL)
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
//
// What the capability *did* (when it looked, how often it polled, which provider
// it is, who observed) is not part of the observation: the step row records when the
// call ran, and an agent-backed look is visible as this step's interaction. The
// observation's own facts — what the deployment reports and what they mean — are the
// output.
func report(snap Snapshot, req Request, now time.Time) map[string]string {
	d := Diagnose(snap, now, req.Tail)
	out := map[string]string{
		"deployment": firstNonEmpty(snap.ID, req.Deployment),
		"state":      string(snap.state()),
		"terminal":   strconv.FormatBool(snap.state().terminal()),
		"problem":    strconv.FormatBool(d.Problem),
		"diagnosis":  d.Summary,
		"evidence":   d.Evidence,
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
	if snap.Message != "" {
		out["message"] = snap.Message
	}
	if snap.Service != "" {
		out["service"] = snap.Service
	}
	if snap.Version != "" {
		out["version"] = snap.Version
	}
	if snap.DeploymentName != "" {
		out["deployment_name"] = snap.DeploymentName
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
// state, an unhealthy target, a stalled rollout) always contribute signals.
// Judgement comes from the monitoring agent when the snapshot carries one
// (agent-backed observer); otherwise the built-in log fingerprints do, and they
// only speak while the outcome is still open. tail is the evidence window size
// (<=0 means the default).
func Diagnose(snap Snapshot, now time.Time, tail int) Diagnosis {
	d := Diagnosis{}
	addSignal := func(name string) {
		if !contains(d.Signals, name) {
			d.Signals = append(d.Signals, name)
		}
	}
	addSuggestion := func(s string) {
		if s != "" && !contains(d.Suggestions, s) {
			d.Suggestions = append(d.Suggestions, s)
		}
	}
	addRule := func(r logRule) {
		addSignal(r.name)
		addSuggestion(r.suggestion)
	}

	// Structural facts about the observed state.
	switch snap.state() {
	case StateFailed:
		addSignal("deployment_failed")
		addSuggestion("the deployment failed: fix the cause named above, then redeploy the same artifact")
	case StateRunning, StatePending:
		if !snap.UpdatedAt.IsZero() && now.Sub(snap.UpdatedAt) > stallAfter {
			addSignal("stalled")
			addSuggestion("the deployment has not reported progress recently: check whether the pipeline step or the target environment is stuck, then decide whether to retry or roll back")
		}
	}
	if snap.Healthy != nil && !*snap.Healthy {
		addSignal("unhealthy")
		addSuggestion("the deployment reported itself unhealthy: verify the service's health endpoint and its dependencies")
	}

	// Judgement: the monitoring agent's own signals replace the built-in log
	// fingerprints; the fingerprints remain the fallback for the deterministic
	// provider, and for an agent that returned none.
	agentJudged := snap.AgentProblem != nil || len(snap.AgentSignals) > 0
	matchAt := -1
	if agentJudged {
		for _, s := range snap.AgentSignals {
			if v := strings.TrimSpace(s); v != "" {
				addSignal(v)
			}
		}
	} else if snap.state() != StateSucceeded {
		for i, line := range snap.Logs {
			if ok, rules := matchRules(line); ok {
				for _, r := range rules {
					addRule(r)
				}
				if matchAt == -1 {
					matchAt = i
				}
			}
		}
		if ok, rules := matchRules(snap.Error); ok {
			for _, r := range rules {
				addRule(r)
			}
		}
	}

	d.Problem = len(d.Signals) > 0
	if snap.AgentProblem != nil {
		d.Problem = *snap.AgentProblem
	}
	for _, s := range snap.AgentSuggestions {
		addSuggestion(strings.TrimSpace(s))
	}

	d.Evidence = evidence(snap.Logs, matchAt, tail)
	d.Summary = snap.AgentDiagnosis
	if d.Summary == "" {
		d.Summary = summarize(snap, d)
	}

	if !d.Problem {
		if snap.state().terminal() {
			addSuggestion("the deployment finished with no failure signal in the observed output")
		} else {
			addSuggestion("the deployment is still in progress: observe it again on the next cycle to keep following it")
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

// firstSignalLine is the first log line (or the reported error/message) that
// looks like the reason, used to make the summary concrete.
func firstSignalLine(snap Snapshot) string {
	for _, line := range snap.Logs {
		if ok, _ := matchRules(line); ok {
			return strings.TrimSpace(line)
		}
	}
	return firstNonEmpty(snap.Error, snap.Message)
}
