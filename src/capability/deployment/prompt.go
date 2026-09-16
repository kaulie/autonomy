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
func renderObservation(req Request, snap Snapshot, err error, history []Snapshot) string {
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "the raw reader failed: %v\n", err)
	}
	if timeline := renderTimeline(history); timeline != "" {
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

// renderTimeline renders what a watch saw before the observation it is reporting: the
// states it passed through, oldest first, with consecutive repeats collapsed into one line
// — a watch may poll a hundred times, and a hundred identical lines is not information.
//
// It is how a monitoring agent is told what happened in between without being asked about
// every poll: the polls are the deterministic reader's, and the agent is asked once.
func renderTimeline(history []Snapshot) string {
	if len(history) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("earlier observations (oldest first, repeats collapsed):\n")
	for i := 0; i < len(history); {
		run := 1
		for i+run < len(history) && observationKey(history[i]) == observationKey(history[i+run]) {
			run++
		}
		fmt.Fprintf(&b, "  - %s", observationLine(history[i]))
		if run > 1 {
			fmt.Fprintf(&b, " x%d until %s", run, observationStamp(history[i+run-1]))
		}
		b.WriteString("\n")
		i += run
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

// observationStamp is when a poll was taken, or "" when the reader did not say.
func observationStamp(snap Snapshot) string {
	if snap.UpdatedAt.IsZero() {
		return "(no timestamp)"
	}
	return snap.UpdatedAt.Format("15:04:05")
}

// observationKey is what makes two polls the same observation: the state and what the
// deployment said about it, without the time (a repeat is a repeat however long it took).
func observationKey(snap Snapshot) string {
	return strings.Join([]string{string(snap.state()), snap.Phase, snap.Progress, snap.Message}, "\x00")
}
