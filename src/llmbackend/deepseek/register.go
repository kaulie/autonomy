package deepseek

import (
	"context"
	"fmt"
	"os"

	"github.com/kaulie/autonomy/src/llmbackend"
)

func init() {
	llmbackend.Register(llmbackend.Harness{
		Backend: llmbackend.Deepseek, Provider: llmbackend.ProviderDeepseekHarness,
		New:          func(h llmbackend.Host) llmbackend.SessionImpl { return newSession(h) },
		Adapter:      streamAdapter{},
		DefaultModel: func() string { return DefaultModel },
		Vendors:      vendors,
		Models:       models,
		Probe:        probe,
	})
}

// vendors is what a DeepSeek account can name: DeepSeek itself.
func vendors(context.Context, llmbackend.Creds) ([]string, error) {
	return []string{"deepseek"}, nil
}

// Models is the pickable catalogue for create-task / account UIs: advice, not a gate (any
// id the endpoint accepts works). The live list is GET /models, which the probe reads.
func Models() []string {
	return []string{DefaultModel, "deepseek-chat", "deepseek-reasoner"}
}

func models(context.Context, llmbackend.Creds) ([]string, error) { return Models(), nil }

// probe checks an account without an agent behind it: GET /models with its key is the load
// check (the credential really is checked), and a live probe runs one short turn.
func probe(ctx context.Context, creds llmbackend.Creds, live bool) (llmbackend.ProbeResult, error) {
	if creds.APIKey == "" {
		return llmbackend.ProbeResult{}, fmt.Errorf("deepseek: the account has no API key")
	}
	model := llmbackend.FirstNonEmptyString(creds.Model, DefaultModel)
	listed, err := newClient(creds.BaseURL, creds.APIKey).models(ctx)
	if err != nil {
		return llmbackend.ProbeResult{}, err
	}
	result := llmbackend.ProbeResult{Load: true, Model: model, Detail: fmt.Sprintf("deepseek API reachable, key accepted (%d models)", len(listed))}
	if !live {
		return result, nil
	}
	dir, err := os.MkdirTemp("", "autonomy-deepseek-probe-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	host := &probeHost{facts: llmbackend.Facts{Workspace: dir, Creds: creds, Ephemeral: true}}
	result.Text, _, err = newSession(host).Prompt(ctx, "Reply with exactly: OK", llmbackend.ModePlan, nil)
	result.Live = err == nil
	result.Detail = "deepseek live probe"
	return result, err
}

// probeHost is the Host of a session with no agent behind it (a probe, a test).
type probeHost struct{ facts llmbackend.Facts }

func (h *probeHost) Facts() llmbackend.Facts { return h.facts }
func (h *probeHost) SetBackend(b llmbackend.Backend, p llmbackend.Provider) {
	h.facts.Backend, h.facts.Provider = b, p
}
func (h *probeHost) SetWorkspace(v string) { h.facts.Workspace = v }
func (h *probeHost) SetModel(v string)     { h.facts.Model = v }
func (h *probeHost) SetSessionID(v string) { h.facts.SessionID = v }
func (h *probeHost) SetFrameSent(v bool)   { h.facts.FrameSent = v }
func (*probeHost) Persist()                {}
