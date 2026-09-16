package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	httpTimeout  = 20 * time.Second
	maxBodyBytes = 1 << 20 // 1 MiB per response
	// statusPath is where a deployment pipeline lives under the control plane
	// base URL (agent-control-plane: GET /api/pipelines/<id>), matching what
	// service.deploy returns in its `poll` output.
	statusPath = "/api/pipelines/"
	// snippetChars bounds an error body kept in an error message.
	snippetChars = 200
)

// HTTPObserver reads deployment state over HTTP: a status endpoint that returns
// the deployment as JSON, and — when the status carries no logs — a logs
// endpoint. It is the default Observer (see NewHTTPObserver).
type HTTPObserver struct {
	// Client is optional; a 20s-timeout client is used when nil.
	Client *http.Client
}

// NewHTTPObserver returns the default HTTP-backed observer.
func NewHTTPObserver() *HTTPObserver { return &HTTPObserver{} }

func (o *HTTPObserver) httpClient() *http.Client {
	if o != nil && o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: httpTimeout}
}

// Observe reads one snapshot: status first, then logs if the status did not
// already include them. A failing logs endpoint is not fatal — the state is
// still an observation, it just arrives without log evidence.
func (o *HTTPObserver) Observe(ctx context.Context, req Request) (Snapshot, error) {
	statusURL, err := statusURLFor(req)
	if err != nil {
		return Snapshot{}, err
	}
	body, err := o.get(ctx, statusURL)
	if err != nil {
		return Snapshot{}, fmt.Errorf("status %s: %w", statusURL, err)
	}
	payload, err := decodeStatus(body)
	if err != nil {
		return Snapshot{}, fmt.Errorf("status %s: %w", statusURL, err)
	}
	logs := payload.logLines()
	if len(logs) == 0 {
		logsURL := strings.TrimSpace(req.LogsURL)
		if logsURL == "" {
			logsURL = strings.TrimRight(statusURL, "/") + "/logs"
		}
		if raw, err := o.get(ctx, logsURL); err == nil {
			logs = decodeLogs(raw)
		}
	}
	return payload.toSnapshot(req, logs), nil
}

// statusURLFor resolves the status URL: an explicit status_url wins, then the
// relative poll path a trigger capability handed back, then the control plane's
// pipeline path derived from the endpoint base URL.
func statusURLFor(req Request) (string, error) {
	if u := strings.TrimSpace(req.StatusURL); u != "" {
		return u, nil
	}
	base := strings.TrimRight(strings.TrimSpace(req.Endpoint), "/")
	if poll := strings.TrimSpace(req.Poll); poll != "" {
		if base == "" {
			return poll, nil
		}
		return base + "/" + strings.TrimLeft(poll, "/"), nil
	}
	if base == "" {
		return "", fmt.Errorf("missing status url")
	}
	if req.Deployment == "" {
		return "", fmt.Errorf("missing deployment")
	}
	return base + statusPath + url.PathEscape(req.Deployment), nil
}

func (o *HTTPObserver) get(ctx context.Context, rawURL string) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json, text/plain")
	resp, err := o.httpClient().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body of a failing status call is itself a diagnosis signal, so it
		// travels with the error instead of being dropped.
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, snippet(body))
	}
	return body, nil
}

// statusPayload is the JSON shape a deployment status endpoint may return. The
// tags are tolerant on purpose: a deployment system that says `status` instead
// of `state`, `stage` instead of `phase`, or `requestId` instead of `id` (the
// control plane's pipeline job) is still readable.
type statusPayload struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	RequestID  string   `json:"requestId"`
	ServiceID  string   `json:"serviceId"`
	State      string   `json:"state"`
	Status     string   `json:"status"`
	Phase      string   `json:"phase"`
	Stage      string   `json:"stage"`
	Progress   string   `json:"progress"`
	Healthy    *bool    `json:"healthy"`
	Health     string   `json:"health"`
	Error      string   `json:"error"`
	Message    string   `json:"message"`
	Version    string   `json:"version"`
	Deployment string   `json:"deployment"`
	UpdatedAt  string   `json:"updated_at"`
	Logs       []string `json:"logs"`
	Lines      []string `json:"lines"`
}

func decodeStatus(body []byte) (statusPayload, error) {
	var p statusPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return statusPayload{}, fmt.Errorf("decode status: %w (body: %s)", err, snippet(body))
	}
	return p, nil
}

// decodeLogs accepts either a JSON object with a lines/logs array (or a bare
// JSON array of lines) or plain text. A body that is valid JSON in some other
// shape is not log text, so it yields nothing instead of a JSON blob as one
// "log line".
func decodeLogs(body []byte) []string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var wrapped struct {
			Logs  []string `json:"logs"`
			Lines []string `json:"lines"`
		}
		if err := json.Unmarshal(body, &wrapped); err == nil {
			if lines := firstNonEmptyLines(wrapped.Logs, wrapped.Lines); len(lines) > 0 {
				return lines
			}
		}
		var lines []string
		if err := json.Unmarshal(body, &lines); err == nil {
			return lines
		}
		if json.Valid(body) {
			return nil
		}
	}
	return trailingNewline(trimmed)
}

// toSnapshot normalizes a decoded payload into the neutral Snapshot, keeping
// only the last req.Tail log lines.
func (p statusPayload) toSnapshot(req Request, logs []string) Snapshot {
	if len(p.Logs) > 0 && len(logs) == 0 {
		logs = p.Logs
	}
	tail := req.Tail
	if tail <= 0 {
		tail = defaultTail
	}
	logs = tailLines(logs, tail)
	return Snapshot{
		ID:             firstNonEmpty(p.ID, p.RequestID, p.Name, req.Deployment),
		State:          normalizeState(firstNonEmpty(p.State, p.Status)),
		Phase:          firstNonEmpty(p.Phase, p.Stage),
		Progress:       strings.TrimSpace(p.Progress),
		Healthy:        p.healthy(),
		Error:          strings.TrimSpace(p.Error),
		Message:        strings.TrimSpace(p.Message),
		Service:        strings.TrimSpace(p.ServiceID),
		Version:        strings.TrimSpace(p.Version),
		DeploymentName: strings.TrimSpace(p.Deployment),
		UpdatedAt:      parseTime(p.UpdatedAt),
		Logs:           logs,
	}
}

// logLines is the inline log array, if the status endpoint included one.
func (p statusPayload) logLines() []string {
	return firstNonEmptyLines(p.Logs, p.Lines)
}

// healthy normalizes the many ways a deployment can say "I am (un)healthy". An
// unstated health is nil, which the diagnosis treats as "unknown", not "fine".
func (p statusPayload) healthy() *bool {
	if p.Healthy != nil {
		return p.Healthy
	}
	yes, no := true, false
	switch strings.ToLower(strings.TrimSpace(p.Health)) {
	case "healthy", "ok", "up", "true", "passing", "green":
		return &yes
	case "unhealthy", "down", "false", "failing", "red", "degraded", "error":
		return &no
	}
	return nil
}

// normalizeState maps the vocabulary a deployment system may use onto the
// neutral State. Anything unrecognised — including an empty value — is unknown,
// so the monitor never invents a state it was not told.
func normalizeState(v string) State {
	s := strings.ToLower(strings.TrimSpace(v))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	switch s {
	case "success", "succeeded", "successful", "ok", "complete", "completed", "done", "healthy", "passed", "pass":
		return StateSucceeded
	case "failure", "failed", "fail", "error", "errored", "unhealthy", "crash", "crashed", "aborted", "cancelled", "canceled", "rollback", "rolled_back", "timed_out", "timeout":
		return StateFailed
	case "running", "in_progress", "progress", "deploying", "deploy", "rolling_out", "updating", "started", "start",
		"packaging", "packaging_in_progress", "building", "building_in_progress", "pushing", "publishing", "releasing":
		return StateRunning
	case "pending", "queued", "created", "waiting", "requested", "accepted", "preparing", "not_started":
		return StatePending
	}
	return StateUnknown
}

func parseTime(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t
	}
	return time.Time{}
}

func firstNonEmptyLines(lists ...[]string) []string {
	for _, l := range lists {
		if len(l) > 0 {
			return l
		}
	}
	return nil
}

// tailLines keeps the last n lines (n <= 0 means the default tail).
func tailLines(lines []string, n int) []string {
	if n <= 0 {
		n = defaultTail
	}
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

// trailingNewline splits plain-text logs, dropping one trailing empty line.
func trailingNewline(text string) []string {
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return lines
}

// snippet is a bounded, single-line rendering of a response body for errors.
func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "(empty body)"
	}
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	text = strings.Join(strings.Fields(text), " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return "(empty body)"
	}
	if len(text) > snippetChars {
		text = text[:snippetChars] + "…"
	}
	return text
}
