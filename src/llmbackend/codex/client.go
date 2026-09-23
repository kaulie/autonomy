package codex

import (
	"os"
	"strings"
	"sync"

	"github.com/kaulie/autonomy/src/codexsdk"
	"github.com/kaulie/autonomy/src/llmbackend"
)

// Shared Codex SDK client (single bridge process). Every codex-backed agent multiplexes on
// this process-wide client, exactly like the cline and cursor paths: one Node bridge holds
// all resident Codex threads.
var (
	sharedCodexMu   sync.Mutex
	sharedCodexClnt *codexsdk.Client
)

// CodexClientFactory builds the process-wide Codex client. Tests replace it with a client
// backed by the fake bridge.
var CodexClientFactory = newCodexClient

// codexClient returns the process-wide Codex client, creating it (and thus the single
// bridge) on first use.
func codexClient() *codexsdk.Client {
	sharedCodexMu.Lock()
	defer sharedCodexMu.Unlock()
	if sharedCodexClnt == nil {
		sharedCodexClnt = CodexClientFactory(agentWorkspace())
	}
	return sharedCodexClnt
}

// CloseCodexClient shuts the bridge down. It should only be called at runtime teardown (or
// process exit), never while agents are using it.
func CloseCodexClient() error {
	sharedCodexMu.Lock()
	defer sharedCodexMu.Unlock()
	if sharedCodexClnt == nil {
		return nil
	}
	err := sharedCodexClnt.Close()
	sharedCodexClnt = nil
	return err
}

// SwapCodexClient replaces the process-wide Codex client and returns the one that was
// there; passing nil forgets it. It is how a test points the bridge at an in-process fake
// and restores it afterwards.
func SwapCodexClient(next *codexsdk.Client) *codexsdk.Client {
	sharedCodexMu.Lock()
	defer sharedCodexMu.Unlock()
	previous := sharedCodexClnt
	sharedCodexClnt = next
	return previous
}

// newCodexClient is the single entry for constructing a Codex client/bridge. The Codex SDK
// reads its own credentials (AUTONOMY_CODEX_API_KEY / CODEX_API_KEY, or `codex auth`), so
// there is no provider id here — the bridge's own config resolves the rest.
func newCodexClient(workspace string) *codexsdk.Client {
	return codexsdk.NewClient(
		codexsdk.WithModel(ResolveCodexModel()),
		codexsdk.WithAPIKey(strings.TrimSpace(os.Getenv("AUTONOMY_CODEX_API_KEY"))),
		codexsdk.WithBaseURL(strings.TrimSpace(os.Getenv("AUTONOMY_CODEX_BASE_URL"))),
		codexsdk.WithSystemPrompt(defaultCodexSystemPrompt()),
		codexsdk.WithWorkspace(workspace),
	)
}

func agentWorkspace() string {
	if root, err := llmbackend.ProjectRoot(); err == nil && strings.TrimSpace(root) != "" {
		return root
	}
	return "."
}

// ResolveCodexModel is the Codex model id (e.g. "gpt-5-codex"); empty means "let the CLI
// decide", which is the Codex SDK's own behaviour.
func ResolveCodexModel() string {
	return strings.TrimSpace(os.Getenv("AUTONOMY_CODEX_MODEL"))
}

// defaultCodexSystemPrompt is the thread's instructions. The Codex SDK has no system-prompt
// option, so the bridge prefixes this to a fresh thread's first turn (a resumed thread
// already carries it); AUTONOMY_CODEX_SYSTEM_PROMPT overrides it.
func defaultCodexSystemPrompt() string {
	if p := strings.TrimSpace(os.Getenv("AUTONOMY_CODEX_SYSTEM_PROMPT")); p != "" {
		return p
	}
	return "You are an autonomous coding agent running inside the autonomy runtime. " +
		"Work inside the workspace of the current task, use the available tools to " +
		"complete it, verify your work, and finish with a concise summary of what you " +
		"changed and why."
}

// CodexModeFor maps an autonomy reasoning mode onto a Codex session mode. The bridge turns
// it into a sandbox: a planner's cycle decides read-only, everything else may write the
// workspace.
func CodexModeFor(mode llmbackend.Mode) string {
	if mode == llmbackend.ModePlan {
		return "plan"
	}
	return codexsdk.DefaultMode
}
