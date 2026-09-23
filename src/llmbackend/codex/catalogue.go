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

// codexModels lists what the Codex CLI offers (usually nothing: it resolves the model itself).
func codexModels(ctx context.Context, _ llmbackend.Creds) ([]string, error) {
	models, err := codexClient().Models(ctx, "")
	if err != nil {
		return nil, nil
	}
	out := make([]string, 0, len(models))
	for _, model := range models {
		if id := strings.TrimSpace(model.ID); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}
