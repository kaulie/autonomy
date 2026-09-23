package codex

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/kaulie/autonomy/src/codexsdk"
	"github.com/kaulie/autonomy/src/llmbackend"
)

// probeCodex loads the Codex bridge on one set of credentials — the handshake a UI needs
// before it trusts a pool entry — and, when live is asked for, runs one short turn on a
// throwaway thread.
//
// It deliberately uses its own client, not the process-wide one: a probe has to be able to
// say "these credentials do not work" without disturbing the sessions the runtime is already
// running, and without leaving a broken bridge behind for the next agent.
func probeCodex(ctx context.Context, creds llmbackend.Creds, live bool) (llmbackend.ProbeResult, error) {
	client := codexsdk.NewClient(
		codexsdk.WithModel(creds.Model),
		codexsdk.WithAPIKey(creds.APIKey),
		codexsdk.WithBaseURL(creds.BaseURL),
		codexsdk.WithWorkspace(probeWorkspace()),
	)
	defer func() { _ = client.Close() }()

	if _, _, err := client.Ping(ctx); err != nil {
		return llmbackend.ProbeResult{}, fmt.Errorf("codex bridge: %w", err)
	}
	result := llmbackend.ProbeResult{
		Load:   true,
		Model:  creds.Model,
		Detail: "codex bridge handshaken (no credentials spent)",
	}
	if !live {
		return result, nil
	}
	agent, err := client.Agents().Create(ctx, codexsdk.CreateOptions{
		ModelID: creds.Model, APIKey: creds.APIKey, BaseURL: creds.BaseURL,
		CWD: probeWorkspace(), Mode: codexsdk.DefaultMode,
	})
	if err != nil {
		return result, fmt.Errorf("create codex thread: %w", err)
	}
	defer func() { _ = agent.Close(ctx) }()
	run, err := agent.Send(ctx, probePrompt)
	if err != nil {
		return result, fmt.Errorf("codex send: %w", err)
	}
	finished, err := run.Wait(ctx)
	if err != nil {
		return result, fmt.Errorf("codex run: %w", err)
	}
	result.Live = true
	result.Text = strings.TrimSpace(finished.Text)
	result.Model = creds.Model
	result.Detail = fmt.Sprintf("one turn answered in %dms", finished.DurationMS)
	return result, nil
}

// probePrompt is what a live probe asks: one word, so the cost is a round trip and nothing
// else.
const probePrompt = "Reply with exactly: OK"

// probeWorkspace is where a probe's throwaway thread runs. A probe must not touch any
// agent's workspace.
func probeWorkspace() string {
	if dir, err := os.MkdirTemp("", "autonomy-codex-probe-"); err == nil {
		return dir
	}
	return os.TempDir()
}
