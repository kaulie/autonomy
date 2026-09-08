package autonomy

import (
	"os"
	"strings"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// newCursorClient is the single entry for constructing a Cursor SDK client/bridge.
func newCursorClient(workspace string) *cursorsdk.Client {
	return cursorsdk.NewClient(
		cursorsdk.WithAPIKey(os.Getenv("CURSOR_API_KEY")),
		cursorsdk.WithWorkspace(workspace),
		cursorsdk.WithBridgeBin(os.Getenv("CURSOR_SDK_BRIDGE_BIN")),
	)
}

func defaultCursorModel() string {
	if m := strings.TrimSpace(os.Getenv("AUTONOMY_LLM_MODEL")); m != "" {
		return m
	}
	return "composer-2"
}
