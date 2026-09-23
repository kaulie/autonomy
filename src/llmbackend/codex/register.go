package codex

import "github.com/kaulie/autonomy/src/llmbackend"

// The Codex harness registers itself: importing this package (directly or through
// src/llmbackend/all) is what makes AUTONOMY_LLM_BACKEND=codex runnable, and nothing in the
// core or the runtime names it.
func init() {
	llmbackend.Register(llmbackend.Harness{
		Backend:      llmbackend.Codex,
		Provider:     llmbackend.ProviderCodex,
		New:          func(host llmbackend.Host) llmbackend.SessionImpl { return newCodexSession(host) },
		Adapter:      CodexStreamAdapter{},
		DefaultModel: CodexDefaultModel,
		CloseClient:  func() error { return CloseCodexClient() },
		Probe:        probeCodex,
	})
}
