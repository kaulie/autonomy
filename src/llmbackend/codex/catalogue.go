package codex

import (
	"context"
	"strings"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// codexVendors is what a Codex account can name: OpenAI. A deployment that talks to something
// else says so with the account's base URL, not with a vendor name.
func codexVendors(context.Context, llmbackend.Creds) ([]string, error) {
	return []string{"openai"}, nil
}

// CodexModels is the pickable fallback when the Codex CLI / bridge has no
// catalogue of its own. Entries here are models ChatGPT-signed-in Codex
// accepts (`gpt-5-codex` / `o3` are API-key only and return 400 with a
// ChatGPT account). This is a static snapshot — a caller may still type
// any id the CLI accepts.
func CodexModels() []string {
	return []string{
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-6.1-sol",
		"gpt-6-sol",
		"gpt-6-luna",
	}
}

// codexModels lists what the Codex CLI offers, then fills in the well-known ids so a
// create-task picker is never empty. A missing bridge is not a hard error.
func codexModels(ctx context.Context, _ llmbackend.Creds) ([]string, error) {
	out := make([]string, 0)
	seen := map[string]bool{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	if models, err := codexClient().Models(ctx, ""); err == nil {
		for _, model := range models {
			add(model.ID)
		}
	}
	for _, id := range CodexModels() {
		add(id)
	}
	return out, nil
}
