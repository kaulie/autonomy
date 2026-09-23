package llmbackend

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

// ClineClientFactory builds the process-wide Cline client. Tests replace it with
// a client backed by the fake bridge (see cline_agent_test.go).
var ClineClientFactory = newClineClient

// clineClient returns the process-wide Cline client, creating it (and thus
// the single bridge) on first use.
func clineClient() *clinesdk.Client {
	sharedClineMu.Lock()
	defer sharedClineMu.Unlock()
	if sharedClineClnt == nil {
		sharedClineClnt = ClineClientFactory(agentWorkspace())
	}
	return sharedClineClnt
}

// CloseClineClient shuts the bridge down. It should only be called at
// runtime teardown (or process exit), never while agents are using it.
func CloseClineClient() error {
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
		clinesdk.WithProvider(ResolveClineProvider()),
		clinesdk.WithModel(ResolveClineModel()),
		clinesdk.WithAPIKey(strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_API_KEY"))),
		clinesdk.WithBaseURL(strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_BASE_URL"))),
		clinesdk.WithSystemPrompt(defaultClineSystemPrompt()),
		clinesdk.WithWorkspace(workspace),
	)
}

func agentWorkspace() string {
	if root, err := ProjectRoot(); err == nil && strings.TrimSpace(root) != "" {
		return root
	}
	return "."
}

// ResolveClineProvider is the Cline provider id (e.g. "deepseek", "anthropic");
// empty means "let the bridge fall back to the provider saved by cline auth".
func ResolveClineProvider() string {
	return strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_PROVIDER"))
}

// ResolveClineModel is the Cline model id. It deliberately does NOT fall back to
// AUTONOMY_LLM_MODEL: that variable holds the default (Cursor) backend's model,
// and those ids are meaningless to a Cline provider. Empty means "let the bridge
// resolve the model from the saved cline auth config".
func ResolveClineModel() string {
	return strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_MODEL"))
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
func DefaultBackend() Backend {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUTONOMY_LLM_BACKEND"))) {
	case string(Cline), "cline_sdk":
		return Cline
	default:
		return Cursor
	}
}

// SwapClineClient replaces the process-wide Cline client and returns the one that was
// there; passing nil forgets it (the next clineClient call builds a fresh one). It is how
// a test points the bridge at an in-process fake and restores it afterwards.
func SwapClineClient(next *clinesdk.Client) *clinesdk.Client {
	sharedClineMu.Lock()
	defer sharedClineMu.Unlock()
	previous := sharedClineClnt
	sharedClineClnt = next
	return previous
}
