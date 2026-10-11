package claude

import (
	"context"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// claudeVendors is what a Claude account can name: Anthropic.
func claudeVendors(context.Context, llmbackend.Creds) ([]string, error) {
	return []string{"anthropic"}, nil
}

// ClaudeModels is the pickable catalogue for create-task / account UIs. Claude Code
// accepts aliases (`--model sonnet`) that track the latest of each family, so the
// list stays stable even as versioned IDs change. A caller may still type a full
// model id; the catalogue is advice, not a gate.
func ClaudeModels() []string {
	return []string{
		"sonnet",
		"opus",
		"haiku",
		"fable",
		"opusplan",
		"sonnet[1m]",
		"opus[1m]",
	}
}

func claudeModels(context.Context, llmbackend.Creds) ([]string, error) {
	return ClaudeModels(), nil
}
