package deployment

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The prompt the monitoring agent receives is a repository file, not Go source,
// so its wording can be changed and picked up on the next observation (see
// src/prompt.go and src/capability/software_development/prompt.go):
// $PROJECT_ROOT/src/agent_policy/DEPLOYMENT_MONITOR.md.
const (
	// DefaultPromptRel is the monitoring prompt, relative to PROJECT_ROOT.
	DefaultPromptRel = "src/agent_policy/DEPLOYMENT_MONITOR.md"

	promptDeployment  = "{{DEPLOYMENT}}"
	promptEndpoint    = "{{ENDPOINT}}"
	promptStatusURL   = "{{STATUS_URL}}"
	promptLogsURL     = "{{LOGS_URL}}"
	promptTail        = "{{TAIL}}"
	promptObservation = "{{OBSERVATION}}"
)

// readPromptTemplate reads the monitoring prompt from PROJECT_ROOT.
func readPromptTemplate() (string, error) {
	root := strings.TrimSpace(os.Getenv("PROJECT_ROOT"))
	if root == "" {
		return "", fmt.Errorf("PROJECT_ROOT is required to read the monitoring prompt")
	}
	path := filepath.Join(root, filepath.FromSlash(DefaultPromptRel))
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read monitoring prompt %s: %w", path, err)
	}
	return string(b), nil
}

// renderPrompt fills the template for one observation. A placeholder with no
// value renders as "(none)" so a missing URL is visible in the trace instead of
// silently dropping the line.
func renderPrompt(tmpl string, values map[string]string) string {
	out := tmpl
	for k, v := range values {
		if strings.TrimSpace(v) == "" {
			v = "(none)"
		}
		out = strings.ReplaceAll(out, k, v)
	}
	return out
}

// renderObservation renders the raw observation for the prompt: what the
// deterministic reader saw (or why it saw nothing), and the log window.
func renderObservation(req Request, snap Snapshot, err error) string {
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "the raw reader failed: %v\n", err)
	}
	fmt.Fprintf(&b, "deployment: %s\n", firstNonEmpty(snap.ID, req.Deployment))
	fmt.Fprintf(&b, "state: %s\n", snap.state())
	if snap.Phase != "" {
		fmt.Fprintf(&b, "phase: %s\n", snap.Phase)
	}
	if snap.Progress != "" {
		fmt.Fprintf(&b, "progress: %s\n", snap.Progress)
	}
	if snap.Healthy != nil {
		fmt.Fprintf(&b, "healthy: %t\n", *snap.Healthy)
	}
	if snap.Service != "" {
		fmt.Fprintf(&b, "service: %s\n", snap.Service)
	}
	if snap.Version != "" {
		fmt.Fprintf(&b, "version: %s\n", snap.Version)
	}
	if snap.Message != "" {
		fmt.Fprintf(&b, "message: %s\n", snap.Message)
	}
	if snap.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", snap.Error)
	}
	if !snap.UpdatedAt.IsZero() {
		fmt.Fprintf(&b, "updated_at: %s\n", snap.UpdatedAt.Format(time.RFC3339))
	}
	if len(snap.Logs) > 0 {
		b.WriteString("logs:\n")
		b.WriteString(strings.Join(snap.Logs, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}
