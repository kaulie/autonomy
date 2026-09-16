package software_development

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaulie/autonomy/src/capability/broker"
)

// The prompt a delegated worker receives is a repository file, not Go source, so
// it can be edited and reloaded per delegation exactly like the agent policy
// (see src/prompt.go): $PROJECT_ROOT/src/agent_policy/CODE_EDIT.md.
//
// Nothing of the prompt itself lives in this package on purpose: the runtime
// reads the file at call time, so a running deployment picks up wording changes
// without a rebuild.
const (
	// DefaultPromptRel is the worker prompt, relative to PROJECT_ROOT.
	DefaultPromptRel = "src/agent_policy/CODE_EDIT.md"
	// promptWorkspace / promptGoal are the placeholders this package fills in
	// itself: the worker's own sandbox and the goal it was delegated. Everything
	// else the template may use is the frame vocabulary of the runtime that
	// delegated (broker.WorkerFramePlaceholders).
	promptWorkspace = "{{WORKSPACE}}"
	promptGoal      = "{{GOAL}}"
)

// readPromptTemplate reads the worker prompt template from PROJECT_ROOT.
func readPromptTemplate() (string, error) {
	root := strings.TrimSpace(os.Getenv("PROJECT_ROOT"))
	if root == "" {
		return "", fmt.Errorf("PROJECT_ROOT is required to read the code_edit prompt")
	}
	path := filepath.Join(root, filepath.FromSlash(DefaultPromptRel))
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read code_edit prompt %s: %w", path, err)
	}
	return string(b), nil
}

// renderPrompt fills the template for one delegation. frame are the values the
// runtime that delegated rendered for this worker (broker.WorkerFrame, may be
// nil); the workspace and the goal are this package's own: they always win, even
// if the host offers values for them. Which sections the worker is told about is
// the template's business — the rendering rules are shared with every other
// capability that delegates (broker.RenderWorkerPrompt).
func renderPrompt(tmpl, workspace, goal string, frame map[string]string) string {
	return broker.RenderWorkerPrompt(tmpl, map[string]string{
		promptWorkspace: workspace,
		promptGoal:      goal,
	}, frame)
}
