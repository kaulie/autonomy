package claude

import (
	"context"
	"fmt"
	"github.com/kaulie/autonomy/src/llmbackend"
	"os"
	"os/exec"
)

func init() {
	llmbackend.Register(llmbackend.Harness{
		Backend: llmbackend.Claude, Provider: llmbackend.ProviderClaude,
		New:     func(h llmbackend.Host) llmbackend.SessionImpl { return newSession(h) },
		Adapter: streamAdapter{},
		Vendors: func(context.Context, llmbackend.Creds) ([]string, error) { return []string{"anthropic"}, nil },
		Models:  func(context.Context, llmbackend.Creds) ([]string, error) { return []string{}, nil },
		Probe:   probe,
	})
}

func probe(ctx context.Context, creds llmbackend.Creds, live bool) (llmbackend.ProbeResult, error) {
	cmd := exec.CommandContext(ctx, binary(), "--version")
	cmd.Env = environment(creds)
	if err := cmd.Run(); err != nil {
		return llmbackend.ProbeResult{}, fmt.Errorf("claude CLI: %w", err)
	}
	result := llmbackend.ProbeResult{Load: true, Model: creds.Model, Detail: "claude CLI executable (credentials not checked)"}
	if !live {
		return result, nil
	}
	dir, err := os.MkdirTemp("", "autonomy-claude-probe-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	host := &probeHost{facts: llmbackend.Facts{Workspace: dir, Creds: creds, Ephemeral: true}}
	result.Text, _, err = newSession(host).Prompt(ctx, "Reply with exactly: OK", llmbackend.ModePlan, nil)
	result.Live = err == nil
	result.Detail = "claude live probe"
	return result, err
}

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
