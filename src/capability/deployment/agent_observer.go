package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaulie/autonomy/src/capability/broker"
)

// AgentObserver monitors a deployment through an agent.
//
// It collects the raw observation deterministically (Base), hands that plus the
// deployment's coordinates to a worker agent — which runs in its own
// AGENT_WORKSPACE with its own tools, can query the deployment API itself, and
// answers in the schema of src/agent_policy/DEPLOYMENT_MONITOR.md — and takes
// back the agent's own verdict: state, problem, signals, diagnosis, suggestions
// and the log lines it found.
//
// The agent observes and reports; it never changes the deployment. Its verdict
// takes precedence over the built-in rules in Diagnose, which stay the fallback
// for the deterministic provider (and for an agent that returned none).
type AgentObserver struct {
	// Agents acquires the monitoring agent (Runtime.AcquireAgent).
	Agents broker.AgentBroker
	// Base is how the raw observation is collected; NewHTTPObserver when nil.
	Base Observer
	// Model overrides the host's default model for this observation.
	Model string
	// Backend overrides the host's default LLM backend.
	Backend string
	// sess is the one agent this observer holds for a call, when its caller bound it
	// (begin). Every observation is then put to that agent — a watch asks one agent
	// about every poll, so the agent sees the states in between and one
	// deployment.monitor call holds one agent. nil means this observer acquires and
	// releases an agent per observation: one observation, self-contained.
	sess broker.AgentSession
}

// begin acquires the one agent this observer may hold for a call and returns an observer
// bound to it, plus the release that ends it.
//
// It is what makes a watch affordable: the polls are put to the same agent instead of a
// fresh worker per poll (task-27's watch acquired four for one deployment), and because
// the session keeps the conversation, the agent knows what it saw before.
func (o *AgentObserver) begin(ctx context.Context, req Request) (*AgentObserver, func(), error) {
	if o == nil || o.Agents == nil {
		return nil, nil, fmt.Errorf("agent observer: agent broker not configured (use Runtime.AcquireAgent)")
	}
	sess, err := o.Agents.AcquireAgent(ctx, broker.AcquireAgentOpts{
		Purpose: Name,
		Backend: o.Backend,
		Model:   o.Model,
		TaskID:  req.TaskID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("agent observer: acquire agent: %w", err)
	}
	bound := *o
	bound.sess = sess
	return &bound, func() { _ = sess.Release(ctx) }, nil
}

func (o *AgentObserver) Observe(ctx context.Context, req Request) (Snapshot, error) {
	if o == nil || o.Agents == nil {
		return Snapshot{}, fmt.Errorf("agent observer: agent broker not configured (use Runtime.AcquireAgent)")
	}
	// The prompt is read before anything is acquired, so a missing template
	// fails the observation instead of creating an agent that cannot be prompted.
	tmpl, err := readPromptTemplate()
	if err != nil {
		return Snapshot{}, fmt.Errorf("agent observer: %w", err)
	}

	// The raw observation is best effort: if it fails, the agent is asked to
	// investigate for itself, and the failure is part of what it is told.
	base, baseErr := o.base().Observe(ctx, req)

	// The session this call holds, or one acquired for this single observation and
	// released with it.
	sess := o.sess
	if sess == nil {
		acquired, err := o.Agents.AcquireAgent(ctx, broker.AcquireAgentOpts{
			Purpose: Name,
			Backend: o.Backend,
			Model:   o.Model,
			TaskID:  req.TaskID,
		})
		if err != nil {
			return Snapshot{}, fmt.Errorf("agent observer: acquire agent: %w", err)
		}
		sess = acquired
		defer func() { _ = sess.Release(ctx) }()
	}

	// The monitoring agent is a delegated worker like any other, so its prompt
	// carries the frame of the runtime that delegated: the World and Runtime
	// Context of the cycle, the Completion Principles of this task's goal type and
	// the Constraints (never act — observe). Which of those DEPLOYMENT_MONITOR.md
	// actually shows is the template's business; the values reach the agent
	// through the session it was acquired with (broker.WorkerFrame).
	prompt := renderPrompt(tmpl, map[string]string{
		promptDeployment:  req.Deployment,
		promptEndpoint:    req.Endpoint,
		promptStatusURL:   firstNonEmpty(req.StatusURL, derivedStatusURL(req)),
		promptLogsURL:     firstNonEmpty(req.LogsURL, derivedLogsURL(req)),
		promptTail:        fmt.Sprintf("%d", req.Tail),
		promptObservation: renderObservation(req, base, baseErr),
	}, broker.WorkerFrame(sess))
	answer, err := sess.Prompt(ctx, prompt)
	if err != nil {
		return Snapshot{}, fmt.Errorf("agent observer: %w", err)
	}
	parsed, ok := parseAgentAnswer(answer)
	if !ok {
		// The agent did not answer in the shape we asked for. Without a raw
		// observation either, there is nothing to report; otherwise the raw one
		// stands and the miss is surfaced rather than hidden.
		if baseErr != nil {
			return Snapshot{}, fmt.Errorf("agent observer: %w", baseErr)
		}
		snap := base
		snap.AgentNote = "the monitoring agent's answer was not the expected JSON; reported the raw observation instead"
		return snap, nil
	}
	return parsed.applyTo(base, req), nil
}

func (o *AgentObserver) base() Observer {
	if o.Base != nil {
		return o.Base
	}
	return NewHTTPObserver()
}

// parseAgentAnswer pulls the JSON object out of an agent's answer. It accepts a
// fenced ```json block or the first JSON object in the text, and requires the
// object to say something usable.
func parseAgentAnswer(answer string) (agentAnswer, bool) {
	if body, ok := fencedBlock(answer); ok {
		if a, ok := decodeAgentAnswer(body); ok {
			return a, true
		}
	}
	if i := strings.IndexByte(answer, '{'); i >= 0 {
		if a, ok := decodeAgentAnswer(answer[i:]); ok {
			return a, true
		}
	}
	return agentAnswer{}, false
}

// decodeAgentAnswer decodes exactly one JSON value from the start of text, so
// trailing prose after the object does not matter.
func decodeAgentAnswer(text string) (agentAnswer, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return agentAnswer{}, false
	}
	var a agentAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		return agentAnswer{}, false
	}
	if a.empty() {
		return agentAnswer{}, false
	}
	return a, true
}

// fencedBlock returns the body of the first ```json (or plain ```) fence.
func fencedBlock(text string) (string, bool) {
	idx := strings.Index(text, "```")
	if idx < 0 {
		return "", false
	}
	rest := text[idx+3:]
	// Drop the info string (json / JSON / empty) up to the end of the line.
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		if info := strings.TrimSpace(rest[:nl]); info == "" || strings.EqualFold(info, "json") {
			rest = rest[nl+1:]
		}
	}
	end := strings.Index(rest, "```")
	if end < 0 {
		return "", false
	}
	body := strings.TrimSpace(rest[:end])
	if body == "" {
		return "", false
	}
	return body, true
}

// derivedStatusURL is what the reader would query, so the prompt can name the
// exact URL the agent should fetch if it investigates for itself.
func derivedStatusURL(req Request) string {
	u, err := statusURLFor(req)
	if err != nil {
		return ""
	}
	return u
}

// derivedLogsURL is the logs endpoint the reader would fall back to.
func derivedLogsURL(req Request) string {
	u := derivedStatusURL(req)
	if u == "" {
		return ""
	}
	return strings.TrimRight(u, "/") + "/logs"
}

// agentAnswer is the schema the monitoring agent is asked to answer with.
type agentAnswer struct {
	State       string   `json:"state"`
	Phase       string   `json:"phase"`
	Progress    string   `json:"progress"`
	Healthy     *bool    `json:"healthy"`
	Message     string   `json:"message"`
	Error       string   `json:"error"`
	Problem     *bool    `json:"problem"`
	Signals     []string `json:"signals"`
	Diagnosis   string   `json:"diagnosis"`
	Suggestions []string `json:"suggestions"`
	Logs        []string `json:"logs"`
}

// empty reports whether the answer says nothing usable, so a stray `{}` is not
// mistaken for a monitoring result.
func (a agentAnswer) empty() bool {
	return strings.TrimSpace(a.State) == "" &&
		strings.TrimSpace(a.Message) == "" &&
		strings.TrimSpace(a.Diagnosis) == "" &&
		len(a.Logs) == 0
}

// applyTo merges the agent's verdict onto the raw observation: the agent's
// answer wins where it speaks, the raw observation fills the rest.
func (a agentAnswer) applyTo(base Snapshot, req Request) Snapshot {
	snap := base
	if v := strings.TrimSpace(a.State); v != "" {
		// An explicitly reported state is authoritative; an unrecognised one is
		// not turned into a state we were not told.
		if s := normalizeState(v); s != StateUnknown {
			snap.State = s
		}
	}
	if v := strings.TrimSpace(a.Phase); v != "" {
		snap.Phase = v
	}
	if v := strings.TrimSpace(a.Progress); v != "" {
		snap.Progress = v
	}
	if a.Healthy != nil {
		snap.Healthy = a.Healthy
	}
	if v := strings.TrimSpace(a.Message); v != "" {
		snap.Message = v
	}
	if v := strings.TrimSpace(a.Error); v != "" {
		snap.Error = v
	}
	if lines := firstNonEmptyLines(a.Logs); len(lines) > 0 {
		snap.Logs = tailLines(lines, req.Tail)
	}
	snap.AgentProblem = a.Problem
	snap.AgentSignals = a.Signals
	snap.AgentDiagnosis = strings.TrimSpace(a.Diagnosis)
	snap.AgentSuggestions = a.Suggestions
	return snap
}
