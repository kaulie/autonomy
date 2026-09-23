package cline

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/kaulie/autonomy/src/clinesdk"
	"github.com/kaulie/autonomy/src/llmbackend"
)

// probeCline loads the Cline bridge on one set of credentials — the handshake a UI needs
// before it trusts a pool entry — and, when live is asked for, runs one short turn on a
// throwaway session.
//
// It deliberately uses its own client, not the process-wide one: a probe has to be able to
// report broken credentials without disturbing the sessions the runtime is already running.
// A vendor is what a Cline key belongs to, so it is passed through: without it the bridge
// resolves whatever `cline auth` saved, which is also what a key-less account relies on.
func probeCline(ctx context.Context, creds llmbackend.Creds, live bool) (llmbackend.ProbeResult, error) {
	client := clinesdk.NewClient(
		clinesdk.WithProvider(creds.Vendor),
		clinesdk.WithModel(creds.Model),
		clinesdk.WithAPIKey(creds.APIKey),
		clinesdk.WithBaseURL(creds.BaseURL),
		clinesdk.WithWorkspace(probeWorkspace()),
	)
	defer func() { _ = client.Close() }()

	info, providers, err := client.Ping(ctx)
	if err != nil {
		return llmbackend.ProbeResult{}, fmt.Errorf("cline bridge: %w", err)
	}
	result := llmbackend.ProbeResult{
		Load:  true,
		Model: creds.Model,
		Detail: fmt.Sprintf("cline bridge handshaken (protocol %s, sdk %s, %d providers; no credentials spent)",
			info.Protocol, info.SDK, len(providers)),
	}
	if !live {
		return result, nil
	}
	agent, err := client.Agents().Create(ctx, clinesdk.CreateOptions{
		ProviderID: creds.Vendor, ModelID: creds.Model, APIKey: creds.APIKey, BaseURL: creds.BaseURL,
		CWD: probeWorkspace(), Mode: clinesdk.DefaultMode,
	})
	if err != nil {
		return result, fmt.Errorf("create cline agent: %w", err)
	}
	defer func() { _ = agent.Close(ctx) }()
	run, err := agent.Send(ctx, probePrompt)
	if err != nil {
		return result, fmt.Errorf("cline send: %w", err)
	}
	finished, err := run.Wait(ctx)
	if err != nil {
		return result, fmt.Errorf("cline run: %w", err)
	}
	result.Live = true
	result.Text = strings.TrimSpace(finished.Text)
	result.Model = creds.Model
	result.Detail = fmt.Sprintf("one turn answered in %dms (%s)", finished.DurationMS, finished.Status)
	return result, nil
}

// probePrompt is what a live probe asks: one word, so the cost is a round trip and nothing
// else.
const probePrompt = "Reply with exactly: OK"

// probeWorkspace is where a probe's throwaway session runs: never an agent's workspace.
func probeWorkspace() string {
	if dir, err := os.MkdirTemp("", "autonomy-cline-probe-"); err == nil {
		return dir
	}
	return os.TempDir()
}
