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

// CodexModels is the pickable fallback when the Codex CLI / bridge has no catalogue
// of its own. These are the ids this runtime already names in tests and docs; a
// caller may still type any id the CLI accepts.
func CodexModels() []string {
	return []string{
		"gpt-5-codex",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.3-codex",
		"gpt-5.2-codex",
		"o3",
		"o4-mini",
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
