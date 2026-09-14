package software_development

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The prompt a delegated worker receives is a repository file, not Go source, so
// it can be edited and reloaded per delegation exactly like the agent policy
// (see src/prompt.go): $PROJECT_ROOT/src/agent_policy/CODE_EDIT.md, with
// {{WORKSPACE}} and {{GOAL}} placeholders.
//
// Nothing of the prompt itself lives in this package on purpose: the runtime
// reads the file at call time, so a running deployment picks up wording changes
// without a rebuild.
const (
	// DefaultPromptRel is the worker prompt, relative to PROJECT_ROOT.
	DefaultPromptRel = "src/agent_policy/CODE_EDIT.md"
	// promptWorkspace / promptGoal are the placeholders the runtime fills in.
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

// renderPrompt fills the template for one delegation. A missing placeholder is
// left as-is (so a template typo is visible in the trace rather than silently
// dropping the workspace or the goal).
func renderPrompt(tmpl, workspace, goal string) string {
	prompt := strings.ReplaceAll(tmpl, promptWorkspace, workspace)
	return strings.ReplaceAll(prompt, promptGoal, goal)
}
