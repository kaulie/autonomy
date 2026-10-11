package codex

import (
	"context"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// codexVendors is what a Codex account can name: OpenAI. A deployment that talks to something
// else says so with the account's base URL, not with a vendor name.
func codexVendors(context.Context, llmbackend.Creds) ([]string, error) {
	return []string{"openai"}, nil
}

// CodexModels is the pickable fallback when `codex app-server` cannot be
// reached. Entries here are models ChatGPT-signed-in Codex accepts
// (`gpt-5-codex` / `o3` are API-key only and return 400 with a ChatGPT
// account). This is a static snapshot — a live list from the CLI wins,
// and a caller may still type any id the CLI accepts.
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

// codexModels lists what this Codex account can run, from `codex app-server`
// `model/list`. A missing CLI is not a hard error: the snapshot keeps the
// picker from going empty. Live rows are not merged with the snapshot — a
// stale id in the snapshot is how `gpt-5-codex` reached a ChatGPT account.
func codexModels(ctx context.Context, creds llmbackend.Creds) ([]string, error) {
	if live, err := listLiveCodexModels(ctx, creds); err == nil && len(live) > 0 {
		return live, nil
	}
	return CodexModels(), nil
}
