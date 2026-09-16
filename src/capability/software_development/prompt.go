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
	// itself: the worker's own sandbox and the goal it was delegated.
	promptWorkspace = "{{WORKSPACE}}"
	promptGoal      = "{{GOAL}}"

	// promptMissingValue is what a placeholder of promptVocabulary renders as when
	// the host does not provide a value for it, so the prompt reads as a gap
	// instead of leaking a raw {{NAME}} to the worker.
	promptMissingValue = "(not provided by this runtime)"
)

// promptVocabulary is the frame vocabulary the shipped template may use beyond
// the two above — the same placeholder names as src/agent_policy/AGENT_V2.md.
// The host renders them (broker.WorkerPromptContext, see
// Runtime.WorkerPlaceholders): the worker is told the World, Runtime Context,
// Constraints, Constructs and Completion Principles of the runtime that
// delegated to it, instead of guessing them.
var promptVocabulary = []string{
	"{{WORLD}}",
	"{{RUNTIME_CONTEXT}}",
	"{{COMPLETION_PRINCIPLES}}",
	"{{CONSTRAINTS}}",
	"{{CONSTRUCTS}}",
}

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

// renderPrompt fills the template for one delegation. hostValues are the frame
// values the runtime rendered for this worker (may be nil). The workspace and the
// goal are this package's own: they always win, even if the host offers values
// for them. A vocabulary placeholder the host left empty renders as
// promptMissingValue; a placeholder outside the vocabulary is left as-is, so a
// template typo stays visible in the trace rather than silently dropping text.
func renderPrompt(tmpl, workspace, goal string, hostValues map[string]string) string {
	out := tmpl
	for k, v := range hostValues {
		if k == promptWorkspace || k == promptGoal {
			continue
		}
		if strings.TrimSpace(v) == "" {
			continue
		}
		out = strings.ReplaceAll(out, k, v)
	}
	for _, k := range promptVocabulary {
		if v := strings.TrimSpace(hostValues[k]); v != "" {
			continue
		}
		out = strings.ReplaceAll(out, k, promptMissingValue)
	}
	out = strings.ReplaceAll(out, promptWorkspace, workspace)
	return strings.ReplaceAll(out, promptGoal, goal)
}

// workerPlaceholders asks the host that acquired this session for the frame
// values this prompt may use (broker.WorkerPromptContext). A session whose host
// cannot describe a runtime context — a test double, a host without a World —
// yields none, and those sections render as not provided.
func workerPlaceholders(sess broker.AgentSession) map[string]string {
	if sess == nil {
		return nil
	}
	provider, ok := sess.(broker.WorkerPromptContext)
	if !ok {
		return nil
	}
	return provider.WorkerPlaceholders()
}
