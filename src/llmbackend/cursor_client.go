package llmbackend

import (
	"os"
	"strings"
	"sync"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// Shared Cursor SDK client (single bridge). All cursor-backed agents
// multiplex on this process-wide client instead of spawning one bridge each.
var (
	sharedCursorMu   sync.Mutex
	sharedCursorClnt *cursorsdk.Client
)

// CursorClient returns the process-wide Cursor SDK client, creating it
// (and thus the single bridge) on first use. The per-agent CWD is passed on
// each CreateAgent call, so one bridge can serve agents in different workspaces.
func CursorClient() *cursorsdk.Client {
	sharedCursorMu.Lock()
	defer sharedCursorMu.Unlock()
	if sharedCursorClnt == nil {
		sharedCursorClnt = NewCursorClient(CursorWorkspace())
	}
	return sharedCursorClnt
}

// CloseCursorClient shuts down the process-wide bridge. It should only
// be called at runtime teardown (or process exit), never while agents are
// actively using the client.
func CloseCursorClient() error {
	sharedCursorMu.Lock()
	defer sharedCursorMu.Unlock()
	if sharedCursorClnt == nil {
		return nil
	}
	err := sharedCursorClnt.Close()
	sharedCursorClnt = nil
	return err
}

func CursorWorkspace() string {
	if root, err := ProjectRoot(); err == nil && strings.TrimSpace(root) != "" {
		return root
	}
	return "."
}

// NewCursorClient is the single entry for constructing a Cursor SDK client/bridge.
// It attaches to an external bridge when CURSOR_SDK_BRIDGE_URL and
// CURSOR_SDK_BRIDGE_TOKEN are both set; otherwise it spawns its own bridge.
func NewCursorClient(workspace string) *cursorsdk.Client {
	opts := []cursorsdk.ClientOption{
		cursorsdk.WithAPIKey(os.Getenv("CURSOR_API_KEY")),
		cursorsdk.WithWorkspace(workspace),
		cursorsdk.WithBridgeBin(os.Getenv("CURSOR_SDK_BRIDGE_BIN")),
	}
	if url := strings.TrimSpace(os.Getenv("CURSOR_SDK_BRIDGE_URL")); url != "" {
		if token := os.Getenv("CURSOR_SDK_BRIDGE_TOKEN"); token != "" {
			opts = append(opts, cursorsdk.WithEndpoint(url, token))
		}
	}
	return cursorsdk.NewClient(opts...)
}

func DefaultCursorModel() string {
	if m := strings.TrimSpace(os.Getenv("AUTONOMY_LLM_MODEL")); m != "" {
		return m
	}
	return "composer-2"
}

// SwapCursorClient replaces the process-wide Cursor client and returns the one that was
// there, so a caller (a test pointing the client at an in-process bridge) can put it back.
// Passing nil forgets the current client — the next CursorClient call builds a fresh one.
func SwapCursorClient(next *cursorsdk.Client) *cursorsdk.Client {
	sharedCursorMu.Lock()
	defer sharedCursorMu.Unlock()
	previous := sharedCursorClnt
	sharedCursorClnt = next
	return previous
}
