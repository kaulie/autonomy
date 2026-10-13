// Package general holds domain-general capabilities (online issue confirmation, etc.).
package general

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/capability/deployment"
	"github.com/kaulie/autonomy/src/capability/spec"
)

const (
	// Name is the stable semantic name for confirming/reproducing an online issue.
	Name = "issue.reproduce"
	// Domain is the general-purpose semantic space for this capability.
	Domain = "general"
	// Provider implements the capability inside Autonomy.
	Provider = "autonomy"

	modeExceptionLog = "exception_log"
	modeUI           = "ui"

	defaultTail     = 80
	maxTail         = 500
	maxLogTextChars = 12000
	maxScreenshot   = 4 << 20 // 4 MiB
	httpTimeout     = 25 * time.Second

	envRuntimeDir = "RUNTIME_DIR"
)

// IssueReproduce captures evidence to confirm an online issue: exception logs or a UI screenshot.
type IssueReproduce struct {
	HTTPClient *http.Client
	// HeadlessScreenshot runs a headless browser capture when set (tests inject a fake).
	HeadlessScreenshot func(ctx context.Context, pageURL string, width, height int) ([]byte, string, error)
}

func (IssueReproduce) Name() string     { return Name }
func (IssueReproduce) Domain() string   { return Domain }
func (IssueReproduce) Provider() string { return Provider }

func (IssueReproduce) Description() string {
	return `confirm an online issue with displayable evidence. input: {"mode":"exception_log"|"ui"} — exception_log: fetch deployment or runtime logs (reuse deployment.monitor wiring: deployment/poll/endpoint/logs_url, or log_path defaulting to $RUNTIME_DIR/backend/server.log); optional tail, pattern (regex filter for error lines). ui: capture a screenshot of {"url":"..."} (direct image URLs, or headless browser when ISSUE_REPRODUCE_BROWSER / CHROMIUM_PATH is set). output: mode, log_text/logs (exception_log), screenshot_base64/content_type (ui). Read-only; does not change deployments or pages.`
}

func (IssueReproduce) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "mode", Required: true, Description: `confirmation mode: "exception_log" (aliases exception/log/logs) or "ui" (aliases screenshot/ui_screenshot)`},
		{Name: "deployment", Aliases: []string{"pipeline_id", "pipeline", "request_id", "target", "run", "id"}, Description: "pipeline/deployment id — same as deployment.monitor; fetches logs via the deployment control plane"},
		{Name: "poll", Description: "relative status path from service.deploy; joined onto endpoint"},
		{Name: "endpoint", Description: "deployment API base URL; default $DEPLOYMENT_API_URL or http://127.0.0.1:4220"},
		{Name: "status_url", Description: "full deployment status URL when not derived from endpoint + deployment"},
		{Name: "logs_url", Description: "direct logs URL (same decode as deployment.monitor /api/pipelines/<id>/logs)"},
		{Name: "log_path", Description: "local log file to tail (default when mode=exception_log and nothing else names a source: $RUNTIME_DIR/backend/server.log)"},
		{Name: "tail", Description: "trailing log lines to keep (default 80, max 500)"},
		{Name: "pattern", Description: "optional regex; when set, log_text and matched_lines highlight/filter to matching lines"},
		{Name: "url", Description: `page or image URL to capture when mode=ui`},
		{Name: "viewport_width", Description: "optional headless browser width in pixels (default 1280)"},
		{Name: "viewport_height", Description: "optional headless browser height in pixels (default 720)"},
	}
}

func (IssueReproduce) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "mode", Description: "the mode that ran: exception_log or ui"},
		{Name: "log_text", Description: "exception_log: newline-joined log lines for display"},
		{Name: "logs", Description: "exception_log: JSON array of log lines"},
		{Name: "line_count", Description: "exception_log: number of lines returned"},
		{Name: "matched_lines", Description: "exception_log: JSON array of lines matching pattern when pattern was set"},
		{Name: "screenshot_base64", Description: "ui: PNG or JPEG bytes, standard base64"},
		{Name: "content_type", Description: "ui: image media type of the screenshot"},
		{Name: "url", Description: "ui: the URL that was captured"},
	}
}

func (c IssueReproduce) Run(in map[string]string) (map[string]string, error) {
	mode, err := normalizeMode(in["mode"])
	if err != nil {
		return nil, err
	}
	switch mode {
	case modeExceptionLog:
		return c.runExceptionLog(in)
	case modeUI:
		return c.runUI(in)
	default:
		return nil, fmt.Errorf("%s: unsupported mode %q", Name, mode)
	}
}

func normalizeMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", modeExceptionLog, "exception", "log", "logs":
		return modeExceptionLog, nil
	case modeUI, "screenshot", "ui_screenshot":
		return modeUI, nil
	default:
		return "", fmt.Errorf("%s: invalid mode %q (want exception_log or ui)", Name, raw)
	}
}

func (c IssueReproduce) runExceptionLog(in map[string]string) (map[string]string, error) {
	tail := parseTail(in["tail"])
	lines, err := c.fetchLogLines(in, tail)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Name, err)
	}
	pattern := strings.TrimSpace(in["pattern"])
	var matched []string
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid pattern: %w", Name, err)
		}
		for _, line := range lines {
			if re.MatchString(line) {
				matched = append(matched, line)
			}
		}
		if len(matched) > 0 {
			lines = matched
		}
	}
	logText := strings.Join(lines, "\n")
	if len(logText) > maxLogTextChars {
		logText = logText[len(logText)-maxLogTextChars:]
	}
	logsJSON, err := json.Marshal(lines)
	if err != nil {
		return nil, err
	}
	out := map[string]string{
		"mode":       modeExceptionLog,
		"log_text":   logText,
		"logs":       string(logsJSON),
		"line_count": strconv.Itoa(len(lines)),
	}
	if pattern != "" {
		mJSON, _ := json.Marshal(matched)
		out["matched_lines"] = string(mJSON)
	}
	return out, nil
}

func (c IssueReproduce) fetchLogLines(in map[string]string, tail int) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	logsURL := strings.TrimSpace(in["logs_url"])
	deploymentID := firstNonEmpty(
		in["deployment"], in["pipeline_id"], in["pipeline"],
		in["request_id"], in["target"], in["run"], in["id"],
	)
	poll := strings.TrimSpace(in["poll"])

	if deploymentID != "" || poll != "" {
		req, err := deployment.ParseRequest(in)
		if err != nil {
			return nil, err
		}
		req.Tail = tail
		req.Watch = false
		obs := deployment.NewHTTPObserver()
		if c.HTTPClient != nil {
			obs.Client = c.HTTPClient
		}
		snap, err := obs.Observe(ctx, req)
		if err != nil {
			return nil, err
		}
		return snap.Logs, nil
	}
	if logsURL != "" {
		obs := deployment.NewHTTPObserver()
		if c.HTTPClient != nil {
			obs.Client = c.HTTPClient
		}
		lines, err := obs.FetchLogsURL(ctx, logsURL)
		if err != nil {
			return nil, err
		}
		return tailLines(lines, tail), nil
	}

	logPath := strings.TrimSpace(in["log_path"])
	if logPath == "" {
		logPath = defaultServerLogPath()
	}
	return tailFile(logPath, tail)
}

func (c IssueReproduce) runUI(in map[string]string) (map[string]string, error) {
	pageURL := strings.TrimSpace(in["url"])
	if pageURL == "" {
		return nil, fmt.Errorf("%s: ui mode requires url", Name)
	}
	width, height := 1280, 720
	if v := strings.TrimSpace(in["viewport_width"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%s: invalid viewport_width %q", Name, v)
		}
		width = n
	}
	if v := strings.TrimSpace(in["viewport_height"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%s: invalid viewport_height %q", Name, v)
		}
		height = n
	}

	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	data, contentType, err := c.captureScreenshot(ctx, pageURL, width, height)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Name, err)
	}
	if len(data) > maxScreenshot {
		return nil, fmt.Errorf("%s: screenshot exceeds %d bytes", Name, maxScreenshot)
	}
	return map[string]string{
		"mode":              modeUI,
		"url":               pageURL,
		"content_type":      contentType,
		"screenshot_base64": base64.StdEncoding.EncodeToString(data),
	}, nil
}

func (c IssueReproduce) captureScreenshot(ctx context.Context, pageURL string, width, height int) ([]byte, string, error) {
	if c.HeadlessScreenshot != nil {
		return c.HeadlessScreenshot(ctx, pageURL, width, height)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/*,text/html,*/*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScreenshot+1))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("http %d fetching %s", resp.StatusCode, pageURL)
	}
	ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(strings.ToLower(ct), "image/") {
		return body, firstNonEmpty(ct, "image/png"), nil
	}
	return headlessBrowserScreenshot(ctx, pageURL, width, height)
}

func parseTail(raw string) int {
	v := strings.TrimSpace(raw)
	if v == "" {
		return defaultTail
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultTail
	}
	if n > maxTail {
		return maxTail
	}
	return n
}

func defaultServerLogPath() string {
	root := strings.TrimSpace(os.Getenv(envRuntimeDir))
	if root == "" {
		return ""
	}
	return filepath.Join(root, "backend", "server.log")
}

func tailFile(path string, n int) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("no log source: pass deployment, logs_url, log_path, or set %s", envRuntimeDir)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read log_path %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	return tailLines(lines, n), nil
}

func tailLines(lines []string, n int) []string {
	if n <= 0 {
		n = defaultTail
	}
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
