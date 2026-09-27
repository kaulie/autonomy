package cursor

import (
	"context"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// cursorVendors is what a Cursor account can name: Cursor itself. Cursor has no second level
// (the pool keeps the field so all three harnesses look the same), and the SDK talks to Cursor's
// own service, so there is no vendor catalogue to ask.
func cursorVendors(context.Context, llmbackend.Creds) ([]string, error) {
	return []string{"cursor"}, nil
}

// cursorModels is what the Cursor SDK lists for this account's key (or the bridge's
// saved auth). A missing bridge / failed catalogue is not a hard error: the form
// still needs a pickable default, so we fall back to the harness default.
func cursorModels(ctx context.Context, creds llmbackend.Creds) ([]string, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
	}
	client := cursorClientFor(creds.APIKey)
	items, err := client.Cursor().Models(ctx)
	if err != nil {
		return []string{DefaultCursorModel()}, nil
	}
	out := make([]string, 0, len(items))
	for _, model := range items {
		if id := strings.TrimSpace(model.ID); id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return []string{DefaultCursorModel()}, nil
	}
	return out, nil
}
