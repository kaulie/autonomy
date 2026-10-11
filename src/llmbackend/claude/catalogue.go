package claude

import (
	"context"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// claudeVendors is what a Claude account can name: Anthropic.
func claudeVendors(context.Context, llmbackend.Creds) ([]string, error) {
	return []string{"anthropic"}, nil
}

// ClaudeModels is the pickable catalogue for create-task / account UIs.
//
// Full ids pin a release (`claude-sonnet-5-5`). Family aliases (`sonnet`) track
// whatever Claude Code currently resolves for that family — they do not name a
// version. Both are accepted by `claude --model`. The list is advice, not a gate.
func ClaudeModels() []string {
	return []string{
		"claude-sonnet-5-5",
		"claude-sonnet-4-6",
		"claude-sonnet-4-5",
		"claude-opus-5-5",
		"claude-opus-4-8",
		"claude-opus-4-6",
		"claude-haiku-5-5",
		"claude-haiku-4-5",
		"claude-fable-5-1",
		"claude-fable-5",
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
