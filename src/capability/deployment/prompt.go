package deployment

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/capability/broker"
)

// The prompt the monitoring agent receives is a repository file, not Go source,
// so its wording can be changed and picked up on the next observation (see
// src/prompt.go and src/capability/software_development/prompt.go):
// $PROJECT_ROOT/src/agent_policy/DEPLOYMENT_MONITOR.md.
const (
	// DefaultPromptRel is the monitoring prompt, relative to PROJECT_ROOT.
	DefaultPromptRel = "src/agent_policy/DEPLOYMENT_MONITOR.md"

	// promptDeployment … are this capability's own placeholders: what to watch,
	// where it lives and what has already been read.
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

// renderPrompt fills the template for one observation. `own` are this
// capability's values (the deployment, its URLs, the raw observation) and a
// missing one renders as "(none)" so a gap is visible in the trace rather than
// silently dropping the line; `frame` is the runtime frame of the delegation
// (broker.WorkerFrame) — the same rendering rules as every other delegation
// (broker.RenderWorkerPrompt), so a section the runtime did not fill reads as
// such instead of reaching the agent as a raw {{NAME}}.
func renderPrompt(tmpl string, own, frame map[string]string) string {
	filled := make(map[string]string, len(own))
	for k, v := range own {
		if strings.TrimSpace(v) == "" {
			v = "(none)"
		}
		filled[k] = v
	}
	return broker.RenderWorkerPrompt(tmpl, filled, frame)
}

// renderObservation renders the raw observation for the prompt: what the
// deterministic reader saw (or why it saw nothing), what the polls before it saw when the
// caller watched, and the log window.
func renderObservation(req Request, snap Snapshot, err error, timeline string) string {
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "the raw reader failed: %v\n", err)
	}
	if timeline != "" {
		b.WriteString(timeline)
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

// renderTrail renders what a watch hands its judge: how often the deployment was read, what
// changed since the poll before it, and — for the changes that looked wrong — the log lines
// the local rules found. It is what makes one question enough: the agent is not asked about
// every poll, and the change that a hundred identical polls would have hidden is in front of
// it, with the evidence that made it a change.
func renderTrail(polls int, changes []trailChange, dropped int) string {
	if len(changes) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the deployment was watched (%d poll(s), %d change(s), oldest first):\n", polls, len(changes)+dropped)
	for _, change := range changes {
		fmt.Fprintf(&b, "  - %s", observationLine(change.Snapshot))
		if len(change.Signals) > 0 {
			fmt.Fprintf(&b, " — signals: %s", strings.Join(change.Signals, ", "))
		}
		b.WriteString("\n")
		for _, line := range change.Evidence {
			fmt.Fprintf(&b, "      %s\n", line)
		}
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "  (%d earlier change(s) not shown)\n", dropped)
	}
	return b.String()
}

// observationLine is one poll's observation in one line: its time, state, and the phase,
// progress and message when it reported them.
func observationLine(snap Snapshot) string {
	parts := []string{observationStamp(snap), string(snap.state())}
	if snap.Phase != "" {
		parts = append(parts, "phase "+snap.Phase)
	}
	if snap.Progress != "" {
		parts = append(parts, "progress "+snap.Progress)
	}
	if snap.Message != "" {
		parts = append(parts, snap.Message)
	}
	return strings.Join(parts, " ")
}

// observationStamp is when a poll was taken, or a placeholder when the reader did not say.
func observationStamp(snap Snapshot) string {
	if snap.UpdatedAt.IsZero() {
		return "(no timestamp)"
	}
	return snap.UpdatedAt.Format("15:04:05")
}
