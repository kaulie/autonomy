package autonomy

import (
	"os"
	"strings"
	"sync"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// Shared Cline SDK client (single bridge process). Every cline-backed agent
// multiplexes on this process-wide client, exactly like the Cursor path: one
// Node bridge holds all resident Cline sessions.
var (
	sharedClineMu   sync.Mutex
	sharedClineClnt *clinesdk.Client
)

// clineClientFactory builds the process-wide Cline client. Tests replace it with
// a client backed by the fake bridge (see cline_agent_test.go).
var clineClientFactory = newClineClient

// sharedClineClient returns the process-wide Cline client, creating it (and thus
// the single bridge) on first use.
func sharedClineClient() *clinesdk.Client {
	sharedClineMu.Lock()
	defer sharedClineMu.Unlock()
	if sharedClineClnt == nil {
		sharedClineClnt = clineClientFactory(sharedAgentWorkspace())
	}
	return sharedClineClnt
}

// closeSharedClineClient shuts the bridge down. It should only be called at
// runtime teardown (or process exit), never while agents are using it.
func closeSharedClineClient() error {
	sharedClineMu.Lock()
	defer sharedClineMu.Unlock()
	if sharedClineClnt == nil {
		return nil
	}
	err := sharedClineClnt.Close()
	sharedClineClnt = nil
	return err
}

// newClineClient is the single entry for constructing a Cline client/bridge.
func newClineClient(workspace string) *clinesdk.Client {
	return clinesdk.NewClient(
		clinesdk.WithProvider(defaultClineProvider()),
		clinesdk.WithModel(defaultClineModel()),
		clinesdk.WithAPIKey(strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_API_KEY"))),
		clinesdk.WithBaseURL(strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_BASE_URL"))),
		clinesdk.WithSystemPrompt(defaultClineSystemPrompt()),
		clinesdk.WithWorkspace(workspace),
	)
}

func sharedAgentWorkspace() string {
	if root, err := projectRoot(); err == nil && strings.TrimSpace(root) != "" {
		return root
	}
	return "."
}

// defaultClineProvider is the Cline provider id (e.g. "deepseek", "anthropic").
func defaultClineProvider() string {
	return strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_PROVIDER"))
}

// defaultClineModel prefers the Cline-specific model, then the shared one.
func defaultClineModel() string {
	if m := strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_MODEL")); m != "" {
		return m
	}
	return strings.TrimSpace(os.Getenv("AUTONOMY_LLM_MODEL"))
}

// defaultClineSystemPrompt is the session system prompt. The Cline SDK requires
// one, and autonomy's own instructions ride on the prompt itself, so this is
// deliberately generic; AUTONOMY_CLINE_SYSTEM_PROMPT overrides it.
func defaultClineSystemPrompt() string {
	if p := strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_SYSTEM_PROMPT")); p != "" {
		return p
	}
	return "You are an autonomous coding agent running inside the autonomy runtime. " +
		"Work inside the workspace of the current task, use the available tools to " +
		"complete it, verify your work, and finish with a concise summary of what you " +
		"changed and why."
}

// defaultAgentBackend resolves which LLM backend acquired agents use:
// AUTONOMY_LLM_BACKEND=cursor (default) or =cline.
func defaultAgentBackend() AgentBackend {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUTONOMY_LLM_BACKEND"))) {
	case string(AgentBackendCline), "cline_sdk":
		return AgentBackendCline
	default:
		return AgentBackendCursor
	}
}
