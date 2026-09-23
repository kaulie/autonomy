package cursor

import (
	"github.com/kaulie/autonomy/src/llmbackend"
	"os"
	"strings"
	"sync"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// Shared llmbackend.Cursor SDK client (single bridge). All cursor-backed agents
// multiplex on this process-wide client instead of spawning one bridge each.
var (
	sharedCursorMu   sync.Mutex
	sharedCursorClnt *cursorsdk.Client
	// sharedCursorByKey holds one bridge per Cursor key an account names
	// (src/accounts.go): the bridge runs with the key it was started with.
	sharedCursorByKey = map[string]*cursorsdk.Client{}
)

// cursorClientFor returns the SDK client for one credential. A runtime may serve accounts on
// several Cursor keys, and each key needs its own bridge process — the bridge is started with
// the key it will use — so clients are cached per key. The key-less case keeps a single
// process-wide client (what a test swaps, and what a runtime with no pool entry used to be).
// The per-agent CWD is still passed on each CreateAgent call, so one bridge serves agents in
// different workspaces.
func cursorClientFor(apiKey string) *cursorsdk.Client {
	key := strings.TrimSpace(apiKey)
	sharedCursorMu.Lock()
	defer sharedCursorMu.Unlock()
	if key == "" {
		if sharedCursorClnt == nil {
			sharedCursorClnt = NewCursorClient(cursorWorkspace(), "")
		}
		return sharedCursorClnt
	}
	if client, ok := sharedCursorByKey[key]; ok {
		return client
	}
	client := NewCursorClient(cursorWorkspace(), key)
	sharedCursorByKey[key] = client
	return client
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

func cursorWorkspace() string {
	if root, err := llmbackend.ProjectRoot(); err == nil && strings.TrimSpace(root) != "" {
		return root
	}
	return "."
}

// NewCursorClient is the single entry for constructing a llmbackend.Cursor SDK client/bridge.
// It attaches to an external bridge when CURSOR_SDK_BRIDGE_URL and
// CURSOR_SDK_BRIDGE_TOKEN are both set; otherwise it spawns its own bridge.
//
// apiKey is the account's credential (src/accounts.go) — the pool is the runtime's only
// source, so nothing here reads CURSOR_API_KEY: an empty key means the account runs on the
// bridge's own saved auth.
func NewCursorClient(workspace, apiKey string) *cursorsdk.Client {
	opts := []cursorsdk.ClientOption{
		cursorsdk.WithAPIKey(apiKey),
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

// DefaultCursorModel is what a cursor agent runs on when its account names no model.
func DefaultCursorModel() string { return "composer-2" }

// SwapCursorClient replaces the process-wide llmbackend.Cursor client and returns the one that was
// there, so a caller (a test pointing the client at an in-process bridge) can put it back.
// Passing nil forgets the current client — the next cursorClient call builds a fresh one.
func SwapCursorClient(next *cursorsdk.Client) *cursorsdk.Client {
	sharedCursorMu.Lock()
	defer sharedCursorMu.Unlock()
	previous := sharedCursorClnt
	sharedCursorClnt = next
	return previous
}
