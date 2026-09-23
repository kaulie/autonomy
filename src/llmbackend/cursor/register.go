package cursor

import "github.com/kaulie/autonomy/src/llmbackend"

// The llmbackend.Cursor harness registers itself: importing this package (directly or through
// src/llmbackend/all) is what makes AUTONOMY_LLM_BACKEND=cursor runnable, and nothing in the
// core or the runtime names it.
func init() {
	llmbackend.Register(llmbackend.Harness{
		Backend:      llmbackend.Cursor,
		Provider:     llmbackend.ProviderCursor,
		New:          func(host llmbackend.Host) llmbackend.SessionImpl { return newCursorSession(host) },
		Adapter:      cursorStreamAdapter{},
		Vendors:      cursorVendors,
		DefaultModel: DefaultCursorModel,
		CloseClient:  func() error { return CloseCursorClient() },
	})
}
