package autonomy

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

// sharedCursorClient returns the process-wide Cursor SDK client, creating it
// (and thus the single bridge) on first use. The per-agent CWD is passed on
// each CreateAgent call, so one bridge can serve agents in different workspaces.
func sharedCursorClient() *cursorsdk.Client {
	sharedCursorMu.Lock()
	defer sharedCursorMu.Unlock()
	if sharedCursorClnt == nil {
		sharedCursorClnt = newCursorClient(sharedCursorWorkspace())
	}
	return sharedCursorClnt
}

// closeSharedCursorClient shuts down the process-wide bridge. It should only
// be called at runtime teardown (or process exit), never while agents are
// actively using the client.
func closeSharedCursorClient() error {
	sharedCursorMu.Lock()
	defer sharedCursorMu.Unlock()
	if sharedCursorClnt == nil {
		return nil
	}
	err := sharedCursorClnt.Close()
	sharedCursorClnt = nil
	return err
}

func sharedCursorWorkspace() string {
	if root, err := projectRoot(); err == nil && strings.TrimSpace(root) != "" {
		return root
	}
	return "."
}

// newCursorClient is the single entry for constructing a Cursor SDK client/bridge.
// It attaches to an external bridge when CURSOR_SDK_BRIDGE_URL and
// CURSOR_SDK_BRIDGE_TOKEN are both set; otherwise it spawns its own bridge.
func newCursorClient(workspace string) *cursorsdk.Client {
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

func defaultCursorModel() string {
	if m := strings.TrimSpace(os.Getenv("AUTONOMY_LLM_MODEL")); m != "" {
		return m
	}
	return "composer-2"
}
