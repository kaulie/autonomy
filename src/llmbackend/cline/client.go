package cline

import (
	"github.com/kaulie/autonomy/src/llmbackend"
	"os"
	"strings"
	"sync"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// Shared llmbackend.Cline SDK client (single bridge process). Every cline-backed agent
// multiplexes on this process-wide client, exactly like the llmbackend.Cursor path: one
// Node bridge holds all resident llmbackend.Cline sessions.
var (
	sharedClineMu   sync.Mutex
	sharedClineClnt *clinesdk.Client
)

// ClineClientFactory builds the process-wide llmbackend.Cline client. Tests replace it with
// a client backed by the fake bridge (see cline_agent_test.go).
var ClineClientFactory = newClineClient

// clineClient returns the process-wide llmbackend.Cline client, creating it (and thus
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

// newClineClient is the single entry for constructing a llmbackend.Cline client/bridge.
//
// It carries no credentials of its own: one bridge process serves every Cline account, and
// each agent's account rides on its own CreateAgent call (src/llmbackend/cline/session.go).
// The pool is the runtime's only source of keys — nothing here reads the environment.
func newClineClient(workspace string) *clinesdk.Client {
	return clinesdk.NewClient(
		clinesdk.WithSystemPrompt(defaultClineSystemPrompt()),
		clinesdk.WithWorkspace(workspace),
	)
}

func agentWorkspace() string {
	if root, err := llmbackend.ProjectRoot(); err == nil && strings.TrimSpace(root) != "" {
		return root
	}
	return "."
}

// ClineDefaultModel is what a cline agent runs on when its account names no model: empty,
// which leaves the choice to the bridge (it resolves one from the saved cline auth).
func ClineDefaultModel() string { return "" }

// defaultClineSystemPrompt is the session system prompt. The llmbackend.Cline SDK requires
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
func DefaultBackend() llmbackend.Backend {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUTONOMY_LLM_BACKEND"))) {
	case string(llmbackend.Cline), "cline_sdk":
		return llmbackend.Cline
	default:
		return llmbackend.Cursor
	}
}

// SwapClineClient replaces the process-wide llmbackend.Cline client and returns the one that was
// there; passing nil forgets it (the next clineClient call builds a fresh one). It is how
// a test points the bridge at an in-process fake and restores it afterwards.
func SwapClineClient(next *clinesdk.Client) *clinesdk.Client {
	sharedClineMu.Lock()
	defer sharedClineMu.Unlock()
	previous := sharedClineClnt
	sharedClineClnt = next
	return previous
}
