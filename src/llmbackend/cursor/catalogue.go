package cursor

import (
	"context"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// cursorVendors is what a Cursor account can name: Cursor itself. Cursor has no second level
// (the pool keeps the field so all three harnesses look the same), and the SDK talks to Cursor's
// own service, so there is no vendor catalogue to ask.
func cursorVendors(context.Context, llmbackend.Creds) ([]string, error) {
	return []string{"cursor"}, nil
}
