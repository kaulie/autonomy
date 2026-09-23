package cline

import (
	"context"
	"sort"
	"strings"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// The vendors a Cline account can name, and the models one of them offers.
//
// Both come from the bridge, which asks the Cline SDK: its `ping` reports every provider the SDK
// knows (hundreds) and `models` lists a provider's models. A UI renders them as lists instead of
// asking a human to type an identifier — the same pair of answers web-cursor serves from its
// provider registry (/api/providers, /api/models).
//
// The bridge is the process-wide one (src/llmbackend/cline/client.go): a catalogue is a read, and
// reading must not spawn a second bridge.

// fallbackVendors is what a UI shows when the bridge cannot be reached: the vendors web-cursor
// falls back to as well. A vendor typed by hand is still accepted by the pool — this list is
// advice, not a gate, because the SDK's catalogue is the provider's business and may grow.
var fallbackVendors = []string{"deepseek", "minimax", "anthropic", "openai", "openai-compatible"}

// catalogueVendors lists the Cline vendors, from the bridge when it answers.
func catalogueVendors(ctx context.Context, _ llmbackend.Creds) ([]string, error) {
	_, providers, err := clineClient().Ping(ctx)
	if err != nil {
		return fallbackVendors, nil
	}
	vendors := make([]string, 0, len(providers))
	seen := map[string]bool{}
	for _, provider := range providers {
		name := strings.TrimSpace(provider)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		vendors = append(vendors, name)
	}
	if len(vendors) == 0 {
		return fallbackVendors, nil
	}
	sort.Strings(vendors)
	return vendors, nil
}

// catalogueModels lists one vendor's models, through the account's own credentials when it has
// any (a keyless account reads whatever `cline auth` saved).
func catalogueModels(ctx context.Context, creds llmbackend.Creds) ([]string, error) {
	vendor := strings.TrimSpace(creds.Vendor)
	if vendor == "" {
		return nil, nil
	}
	models, err := clineClient().Models(ctx, vendor)
	if err != nil {
		return nil, nil
	}
	out := make([]string, 0, len(models))
	for _, model := range models {
		if id := strings.TrimSpace(model.ID); id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
