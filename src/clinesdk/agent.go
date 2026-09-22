package clinesdk

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/llmrun"
)

// AgentFactory creates resident Cline sessions.
type AgentFactory struct{ client *Client }

// CreateOptions configures Create.
type CreateOptions struct {
	ProviderID   string
	ModelID      string
	APIKey       string
	BaseURL      string
	CWD          string
	SystemPrompt string
	// Mode is "yolo" (default) or "plan".
	Mode string
	// ResumeSessionID is a Cline session whose conversation this one continues: the
	// bridge reads that session's stored transcript and seeds the new session with it
	// (src/clinesdk/bridge/resume.mjs) — the SDK does not reload a session when it is
	// handed its id, and handing back the same id overwrites the stored transcript.
	// Empty starts a conversation from nothing, which is also what a session with no
	// transcript left (retention, deletion) falls back to.
	ResumeSessionID string
}

// Agent is one resident Cline session. The session is started lazily by the
// first Send and then reused for every later Send, so the agent keeps its
// conversation, workspace context and prompt cache across autonomy steps.
type Agent struct {
	client *Client

	// ID is the bridge-side handle id.
	ID string
	// SessionID is the Cline session id, known once the first run started.
	SessionID string
	// ResumeSessionID is the session this one was asked to continue, empty for a
	// conversation that started from nothing.
	ResumeSessionID string

	ProviderID string
	ModelID    string
	Mode       string
	CWD        string
}

// Create registers a resident session handle with the bridge. No model call
// happens until the first Send.
func (f *AgentFactory) Create(ctx context.Context, opts CreateOptions) (*Agent, error) {
	tr, err := f.client.ensure(ctx)
	if err != nil {
		return nil, err
	}
	provider := firstNonEmpty(opts.ProviderID, f.client.ProviderID)
	model := firstNonEmpty(opts.ModelID, f.client.ModelID)
	cwd := firstNonEmpty(opts.CWD, f.client.Workspace)
	mode := firstNonEmpty(opts.Mode, f.client.Mode, DefaultMode)
	systemPrompt := firstNonEmpty(opts.SystemPrompt, f.client.SystemPrompt)
	resumeID := strings.TrimSpace(opts.ResumeSessionID)

	raw, err := tr.call(ctx, "createAgent", map[string]any{
		"providerId":      provider,
		"modelId":         model,
		"apiKey":          firstNonEmpty(opts.APIKey, f.client.APIKey),
		"baseUrl":         firstNonEmpty(opts.BaseURL, f.client.BaseURL),
		"cwd":             cwd,
		"mode":            mode,
		"systemPrompt":    systemPrompt,
		"resumeSessionId": resumeID,
	})
	if err != nil {
		return nil, err
	}
	var res struct {
		AgentID         string `json:"agentId"`
		Mode            string `json:"mode"`
		CWD             string `json:"cwd"`
		ProviderID      string `json:"providerId"`
		ModelID         string `json:"modelId"`
		ResumeSessionID string `json:"resumeSessionId"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, bridgeErr("decode createAgent: %v", err)
	}
	// The bridge resolves a missing provider/model from the saved cline auth
	// config, so adopt what it actually used (this is also what gets recorded on
	// the agent and in reason_turns.model).
	return &Agent{
		client:          f.client,
		ID:              res.AgentID,
		ProviderID:      firstNonEmpty(res.ProviderID, provider),
		ModelID:         firstNonEmpty(res.ModelID, model),
		Mode:            mode,
		CWD:             res.CWD,
		ResumeSessionID: firstNonEmpty(res.ResumeSessionID, resumeID),
	}, nil
}

// Send prompts the resident session, starting it on the first call, and returns
// the live run. Events stream into Run.WaitStream, which also decodes the
// terminal result.
func (a *Agent) Send(ctx context.Context, prompt string) (*Run, error) {
	if a == nil || a.client == nil {
		return nil, bridgeErr("nil agent")
	}
	tr, err := a.client.ensure(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := tr.beginRun(ctx, "send", map[string]any{
		"agentId": a.ID,
		"prompt":  prompt,
		"mode":    a.Mode,
	})
	if err != nil {
		return nil, err
	}
	return &Run{stream: stream, agent: a, prompt: prompt, startedAt: time.Now()}, nil
}

// Stop ends the resident session (the handle stays usable for a fresh session).
func (a *Agent) Stop(ctx context.Context) error {
	tr, err := a.client.ensure(ctx)
	if err != nil {
		return err
	}
	if _, err := tr.call(ctx, "stop", map[string]any{"agentId": a.ID}); err != nil {
		return err
	}
	a.SessionID = ""
	return nil
}

// Close stops the session and forgets the handle.
func (a *Agent) Close(ctx context.Context) error {
	tr, err := a.client.ensure(ctx)
	if err != nil {
		return err
	}
	_, err = tr.call(ctx, "close", map[string]any{"agentId": a.ID})
	return err
}

// Usage returns the session's cumulative usage as the SDK accounts for it.
func (a *Agent) Usage(ctx context.Context) (*RunUsage, error) {
	tr, err := a.client.ensure(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := tr.call(ctx, "usage", map[string]any{"agentId": a.ID})
	if err != nil {
		return nil, err
	}
	var res struct {
		Usage *RunUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, bridgeErr("decode usage: %v", err)
	}
	return res.Usage, nil
}

// Run is one live prompt/response cycle on a resident session.
type Run struct {
	stream *runStream
	agent  *Agent

	prompt    string
	startedAt time.Time
}

// Prompt is the prompt that started this run.
func (r *Run) Prompt() string { return r.prompt }

// WaitStream drains the run's event stream until the session answers, invoking
// onEvent for every native provider event in arrival order (onEvent may be nil).
// A cancelled context (an idle timeout, for example) aborts the wait and asks
// the bridge to stop the session so the provider stops generating.
func (r *Run) WaitStream(ctx context.Context, onEvent func(RunEvent)) (*RunResult, error) {
	events := 0
	arrived := 0
	last := ""
	started := time.Now()
	lastEventAt := time.Now()
	Trace("Wait", "begin agent=%s model=%s prompt_bytes=%d", r.agent.ID, r.agent.ModelID, len(r.prompt))
	stopHB := startHeartbeat(func() (int, string, time.Duration) {
		return events, last, time.Since(lastEventAt)
	})
	defer stopHB()

	for {
		ev, ok, err := r.stream.next(ctx)
		if err != nil {
			r.cancelRun()
			res := r.baseResult(LLMStatusCancelled)
			res.ErrorMessage = err.Error()
			Trace("Wait", "aborted after %s events=%d last=%s: %v", time.Since(started).Round(time.Millisecond), events, last, err)
			return res, r.describeWaitError(err, arrived)
		}
		if !ok {
			break
		}
		arrived++
		if !isAgentEcho(ev) {
			events++
			last = eventLabel(ev)
			lastEventAt = time.Now()
		}
		// Provider activity resets the run's idle budget. Without this the budget
		// would bound the *total* run time instead of silence, killing busy runs.
		llmrun.Touch(ctx)
		if TraceVerbose() {
			Trace("event", "%s", last)
		}
		if r.agent.SessionID == "" && ev.SessionID != "" {
			r.agent.SessionID = ev.SessionID
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}
	raw, err := r.stream.waited(ctx)
	if err != nil {
		r.cancelRun()
		res := r.baseResult(LLMStatusCancelled)
		res.ErrorMessage = err.Error()
		return res, r.describeWaitError(err, arrived)
	}
	res, err := r.decodeResult(raw)
	if err != nil {
		return res, err
	}
	if res.Status == LLMStatusError {
		return res, &RunError{Code: res.ErrorCode, Message: res.ErrorMessage}
	}
	return res, nil
}

// Wait is WaitStream without event delivery.
func (r *Run) Wait(ctx context.Context) (*RunResult, error) {
	return r.WaitStream(ctx, nil)
}

// eventLabel renders one native event for progress logs.
func eventLabel(ev RunEvent) string {
	if ev.Type == "" {
		return "?"
	}
	inner := ev.Payload["event"]
	if m, ok := inner.(map[string]any); ok {
		if t, ok := m["type"].(string); ok {
			if ct, ok := m["contentType"].(string); ok {
				return ev.Type + "/" + t + ":" + ct
			}
			return ev.Type + "/" + t
		}
	}
	if stream, ok := ev.Payload["stream"].(string); ok {
		return ev.Type + ":" + stream
	}
	return ev.Type
}

// isAgentEcho reports whether an event is the SDK's verbatim JSON echo of the
// agent stream. The neutral mapping drops those (they duplicate agent_event), so
// progress counters skip them too and match what actually gets persisted.
func isAgentEcho(ev RunEvent) bool {
	if ev.Type != "chunk" {
		return false
	}
	stream, _ := ev.Payload["stream"].(string)
	return stream == "" || stream == "agent"
}

// startHeartbeat logs progress every 15s until stopped, so a long run is
// visibly alive instead of looking like a hang (the states probe events, the
// last event label and the time since that event).
func startHeartbeat(state func() (int, string, time.Duration)) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				events, last, silent := state()
				Trace("Wait", "still running events=%d last=%s silent=%s", events, last, silent.Round(time.Second))
			}
		}
	}()
	return func() { close(done) }
}

// describeWaitError explains a failed wait. A run that produced no events at all
// stalled before the provider answered, which needs a different investigation
// than a run that went quiet midway.
func (r *Run) describeWaitError(err error, events int) error {
	if err == nil {
		return nil
	}
	if events > 0 {
		return err
	}
	return fmt.Errorf("%w (no SDK events arrived at all: check the provider status/quota for %s/%s; AUTONOMY_CLINE_TRACE=1 traces provider events)",
		err, r.agent.ProviderID, r.agent.ModelID)
}

// cancelRun asks the bridge to stop the session after a cancelled wait, so a
// timed-out run does not keep burning tokens in the background.
func (r *Run) cancelRun() {
	if r.agent == nil || r.agent.client == nil {
		return
	}
	agent := r.agent
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = agent.Stop(ctx)
	}()
}

func (r *Run) baseResult(status string) *RunResult {
	return &RunResult{
		AgentID:    r.agent.ID,
		SessionID:  r.agent.SessionID,
		Status:     status,
		StartedAt:  r.startedAt,
		EndedAt:    time.Now(),
		DurationMS: time.Since(r.startedAt).Milliseconds(),
	}
}

func (r *Run) decodeResult(raw json.RawMessage) (*RunResult, error) {
	// The summary is decoded leniently: the bridge/SDK may hand back a structured
	// object where a string is expected (a provider error, for example) or a
	// numeric string, and a finished run must never fail to decode because of it.
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, bridgeErr("decode run result: %v", err)
	}
	res := &RunResult{
		AgentID:      firstNonEmpty(anyText(payload["agentId"]), r.agent.ID),
		SessionID:    firstNonEmpty(anyText(payload["sessionId"]), r.agent.SessionID),
		ResumedFrom:  anyText(payload["resumedFrom"]),
		Mode:         anyText(payload["mode"]),
		Status:       normalizeStatus(anyText(payload["status"])),
		Text:         anyText(payload["text"]),
		FinishReason: anyText(payload["finishReason"]),
		UsageSource:  anyText(payload["usageSource"]),
		ErrorMessage: messageText(payload["lastError"]),
		StartedAt:    r.startedAt,
		EndedAt:      time.Now(),
	}
	res.DurationMS = time.Since(r.startedAt).Milliseconds()
	if usage, ok := payload["usage"].(map[string]any); ok {
		res.Usage = decodeUsageMap(usage)
	}
	if res.SessionID != "" {
		r.agent.SessionID = res.SessionID
	}
	if res.Status == LLMStatusError && res.ErrorMessage == "" {
		res.ErrorMessage = firstNonEmpty(res.FinishReason, "cline run failed")
	}
	return res, nil
}

// anyText renders a decoded JSON value as text: strings pass through, numbers and
// booleans become their literal form, objects/arrays become compact JSON.
func anyText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		if b, err := json.Marshal(t); err == nil {
			return string(b)
		}
		return fmt.Sprint(t)
	}
}

// messageText renders an error-ish value as a human message, preferring the usual
// message keys (recursing into nested objects) before falling back to compact JSON.
func messageText(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return anyText(v)
	}
	for _, key := range []string{"message", "error", "detail", "reason", "code"} {
		switch t := m[key].(type) {
		case nil:
			continue
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		case map[string]any, []any:
			if s := messageText(t); s != "" {
				return s
			}
		default:
			if s := anyText(t); s != "" && s != "{}" {
				return s
			}
		}
	}
	if s := anyText(m); s != "" && s != "{}" {
		return s
	}
	return ""
}

// LLM run statuses, matching the neutral model used by the rest of autonomy.
const (
	LLMStatusFinished  = "finished"
	LLMStatusError     = "error"
	LLMStatusCancelled = "cancelled"
)

func normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case LLMStatusFinished, "completed", "ok", "":
		return LLMStatusFinished
	case LLMStatusCancelled, "aborted", "stopped":
		return LLMStatusCancelled
	case LLMStatusError, "failed":
		return LLMStatusError
	default:
		return LLMStatusFinished
	}
}

// RunUsage is one run's (or a session's cumulative) token accounting.
type RunUsage struct {
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	ReasoningTokens  *int64  `json:"reasoningTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	CostUSD          float64 `json:"costUsd"`
	// HasCost distinguishes "no cost reported" from "zero cost".
	HasCost bool `json:"-"`
}

// UnmarshalJSON reads a usage object leniently (numbers or numeric strings) and
// remembers whether a cost was present at all.
func (u *RunUsage) UnmarshalJSON(data []byte) error {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*u = decodeUsageMap(m)
	return nil
}

// decodeUsageMap reads the usage numbers, accepting both the camelCase keys the
// bridge sends and the snake_case spellings a provider might use.
func decodeUsageMap(m map[string]any) RunUsage {
	var u RunUsage
	int64Field := func(target *int64, keys ...string) {
		for _, key := range keys {
			if v, ok := anyInt64(m[key]); ok {
				*target = v
				return
			}
		}
	}
	int64Field(&u.InputTokens, "inputTokens", "input_tokens")
	int64Field(&u.OutputTokens, "outputTokens", "output_tokens")
	int64Field(&u.CacheReadTokens, "cacheReadTokens", "cache_read_tokens")
	int64Field(&u.CacheWriteTokens, "cacheWriteTokens", "cache_write_tokens")
	int64Field(&u.TotalTokens, "totalTokens", "total_tokens")
	for _, key := range []string{"reasoningTokens", "reasoning_tokens"} {
		if v, ok := anyInt64(m[key]); ok {
			u.ReasoningTokens = &v
			break
		}
	}
	if cost, ok := anyFloat(m["costUsd"], m["cost_usd"], m["totalCost"], m["cost"]); ok {
		u.CostUSD = cost
		u.HasCost = true
	}
	return u
}

// anyInt64 reads a decoded JSON value as an integer, accepting numeric strings.
func anyInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	case string:
		if t = strings.TrimSpace(t); t == "" {
			return 0, false
		}
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			if f, ferr := strconv.ParseFloat(t, 64); ferr == nil {
				return int64(f), true
			}
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// anyFloat returns the first value that reads as a float.
func anyFloat(vals ...any) (float64, bool) {
	for _, v := range vals {
		switch t := v.(type) {
		case float64:
			return t, true
		case int64:
			return float64(t), true
		case json.Number:
			if f, err := t.Float64(); err == nil {
				return f, true
			}
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

// RunResult is the terminal outcome of one run.
type RunResult struct {
	AgentID   string
	SessionID string
	// ResumedFrom is the session this run continued, when it was the first run of a
	// session seeded with an earlier one's transcript (src/clinesdk/bridge/resume.mjs).
	ResumedFrom string
	Mode        string
	// Status is finished | error | cancelled.
	Status       string
	Text         string
	FinishReason string
	// UsageSource says where the usage came from: "run" (the SDK reported it on
	// the result) or "accumulated_delta" (diffed from the session's running
	// total, which is what a resident send returns).
	UsageSource  string
	Usage        RunUsage
	ErrorCode    string
	ErrorMessage string
	DurationMS   int64
	StartedAt    time.Time
	EndedAt      time.Time
}

// OK reports whether the run finished normally.
func (r RunResult) OK() bool { return r.Status == LLMStatusFinished }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
