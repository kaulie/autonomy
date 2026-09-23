package cline

import "github.com/kaulie/autonomy/src/llmbackend"

// The Cline harness registers itself: importing this package (directly or through
// src/llmbackend/all) is what makes AUTONOMY_LLM_BACKEND=cline runnable, and nothing in the
// core or the runtime names it.
func init() {
	llmbackend.Register(llmbackend.Harness{
		Backend:      llmbackend.Cline,
		Provider:     llmbackend.ProviderCline,
		New:          func(host llmbackend.Host) llmbackend.SessionImpl { return newClineSession(host) },
		Adapter:      ClineStreamAdapter{},
		DefaultModel: ResolveClineModel,
		CloseClient:  func() error { return CloseClineClient() },
	})
}
