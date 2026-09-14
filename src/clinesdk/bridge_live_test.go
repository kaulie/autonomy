package clinesdk_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// TestClineBridgeLive drives the real Node bridge (bridge/bridge.mjs) against a
// configured provider. It needs a Node runtime, the bridge's npm dependencies
// (scripts/install-cline-bridge.sh) and provider credentials, so it stays opt-in:
//
//	CLINE_LIVE=1 AUTONOMY_CLINE_PROVIDER=deepseek AUTONOMY_CLINE_MODEL=deepseek-v4-pro \
//	AUTONOMY_CLINE_API_KEY=sk-... go test ./src/clinesdk -run TestClineBridgeLive -v -timeout 6m
//
// The third prompt checks the session is resident: the model must remember the
// first prompt of the same session.
func TestClineBridgeLive(t *testing.T) {
	if os.Getenv("CLINE_LIVE") != "1" {
		t.Skip("set CLINE_LIVE=1 to run")
	}
	provider := os.Getenv("AUTONOMY_CLINE_PROVIDER")
	model := os.Getenv("AUTONOMY_CLINE_MODEL")
	if provider == "" || model == "" {
		// Allowed: the bridge falls back to the provider/model saved by
		// `cline auth`, so an authenticated machine can run with no env at all.
		t.Logf("AUTONOMY_CLINE_PROVIDER/MODEL unset; relying on the saved cline auth config")
	}
	workspace := t.TempDir()
	client := clinesdk.NewClient(
		clinesdk.WithProvider(provider),
		clinesdk.WithModel(model),
		clinesdk.WithWorkspace(workspace),
	)
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	info, providers, err := client.Ping(ctx)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Logf("bridge sdk=%s node=%s providers=%d", info.SDK, info.Node, len(providers))
	if provider != "" && !containsString(providers, provider) {
		t.Fatalf("provider %q not in bridge catalog", provider)
	}

	agent, err := client.Agents().Create(ctx, clinesdk.CreateOptions{
		SystemPrompt: "You are a terse assistant. Follow the instruction literally.",
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	defer func() { _ = agent.Close(context.Background()) }()

	events := 0
	channels := map[string]int{}
	run, err := agent.Send(ctx, "Reply with exactly: pong. Do not use any tools.")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	res, err := run.WaitStream(ctx, func(ev clinesdk.RunEvent) {
		events++
		channels[ev.Type]++
	})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	t.Logf("turn 1: status=%s text=%q events=%d usage=%+v", res.Status, res.Text, events, res.Usage)
	if !res.OK() || !strings.Contains(strings.ToLower(res.Text), "pong") {
		t.Fatalf("unexpected run result: %+v", res)
	}
	if res.SessionID == "" {
		t.Fatal("no session id on the run result")
	}
	if res.Usage.InputTokens <= 0 || res.Usage.OutputTokens <= 0 {
		t.Fatalf("usage was not reported: %+v", res.Usage)
	}
	if len(channels) == 0 {
		t.Fatal("no native events streamed")
	}

	// Second turn resumes the same session (and may use a tool).
	run2, err := agent.Send(ctx, "Now run the shell command `echo hello-from-cline` and reply with just its output.")
	if err != nil {
		t.Fatalf("send 2: %v", err)
	}
	res2, err := run2.WaitStream(ctx, nil)
	if err != nil {
		t.Fatalf("wait 2: %v", err)
	}
	t.Logf("turn 2: status=%s text=%q usage=%+v source=%s", res2.Status, res2.Text, res2.Usage, res2.UsageSource)
	if res2.SessionID != res.SessionID {
		t.Fatalf("session changed across turns: %q → %q", res.SessionID, res2.SessionID)
	}

	// Third turn proves residency: the session remembers the first prompt.
	run3, err := agent.Send(ctx, "What did I ask you in my very first message? One short line.")
	if err != nil {
		t.Fatalf("send 3: %v", err)
	}
	res3, err := run3.WaitStream(ctx, nil)
	if err != nil {
		t.Fatalf("wait 3: %v", err)
	}
	t.Logf("turn 3 (resident memory): %q", res3.Text)
	if !strings.Contains(strings.ToLower(res3.Text), "pong") {
		t.Fatalf("session did not remember the first prompt: %q", res3.Text)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
