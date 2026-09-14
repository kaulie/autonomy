package clinesdk_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
	"github.com/kaulie/autonomy/src/clinesdk/fakebridge"
	"github.com/kaulie/autonomy/src/llmrun"
)

// The helper below is the test binary re-executed as a fake cline bridge: it
// speaks the same NDJSON protocol as bridge/bridge.mjs, which keeps the Go
// transport covered in CI without Node or a provider account.
func TestFakeBridgeProcess(t *testing.T) {
	if !fakebridge.Enabled() {
		t.Skip("helper process only")
	}
	fakebridge.Main()
}

func newFakeClient(t *testing.T) *clinesdk.Client {
	t.Helper()
	client := clinesdk.NewClient(
		clinesdk.WithManager(fakebridge.Manager(os.Args[0])),
		clinesdk.WithProvider("deepseek"),
		clinesdk.WithModel("deepseek-v4-pro"),
		clinesdk.WithWorkspace(t.TempDir()),
	)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestPingReportsProviderCatalog(t *testing.T) {
	client := newFakeClient(t)
	info, providers, err := client.Ping(context.Background())
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if info.Protocol != clinesdk.Protocol {
		t.Fatalf("protocol=%q want %q", info.Protocol, clinesdk.Protocol)
	}
	if len(providers) == 0 || providers[0] != "deepseek" {
		t.Fatalf("providers=%v want deepseek first", providers)
	}
}

func TestResidentSendStreamsEventsAndKeepsSession(t *testing.T) {
	client := newFakeClient(t)
	ctx := context.Background()
	agent, err := client.Agents().Create(ctx, clinesdk.CreateOptions{SystemPrompt: "be terse"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var events []clinesdk.RunEvent
	run, err := agent.Send(ctx, "first prompt")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	res, err := run.WaitStream(ctx, func(ev clinesdk.RunEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !res.OK() || res.Text != "pong" {
		t.Fatalf("res=%+v want finished text=pong", res)
	}
	if res.SessionID == "" || agent.SessionID != res.SessionID {
		t.Fatalf("session id not learned: res=%q agent=%q", res.SessionID, agent.SessionID)
	}
	if len(events) == 0 || events[0].Type != "status" {
		t.Fatalf("events=%v want a status event first", eventTypes(events))
	}
	if res.Usage.InputTokens != 11 || res.Usage.OutputTokens != 2 || !res.Usage.HasCost {
		t.Fatalf("usage=%+v want the reported tokens and cost", res.Usage)
	}

	// A second prompt must reuse the same resident session, and its usage is
	// reported as a delta because a resident send carries none on its result.
	run2, err := agent.Send(ctx, "second prompt")
	if err != nil {
		t.Fatalf("send 2: %v", err)
	}
	res2, err := run2.WaitStream(ctx, nil)
	if err != nil {
		t.Fatalf("wait 2: %v", err)
	}
	if res2.SessionID != res.SessionID {
		t.Fatalf("session changed: %q → %q", res.SessionID, res2.SessionID)
	}
	if res2.Text != "tool said hi" || res2.UsageSource != "accumulated_delta" {
		t.Fatalf("res2=%+v want the resident delta result", res2)
	}
	if res2.Usage.InputTokens != 5 {
		t.Fatalf("delta usage=%+v want 5 input tokens", res2.Usage)
	}
}

// TestBusyRunOutlivesTheIdleBudget is the regression test for the stall that
// killed real Cline runs: the idle budget must bound *silence*, not the total
// run time, so a run that keeps producing events has to survive.
func TestBusyRunOutlivesTheIdleBudget(t *testing.T) {
	client := newFakeClient(t)
	agent, err := client.Agents().Create(context.Background(), clinesdk.CreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The fake bridge streams for ~720ms (12 events, 60ms apart) while the idle
	// budget is only 250ms: without per-event touches this aborts at 250ms.
	wd := llmrun.NewIdleWatchdog(context.Background(), 250*time.Millisecond)
	defer wd.Stop()
	ctx := llmrun.WithIdleWatchdog(wd.Context(), wd)

	events := 0
	run, err := agent.Send(ctx, "stream slowly please")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	res, err := run.WaitStream(ctx, func(clinesdk.RunEvent) { events++ })
	if err != nil {
		t.Fatalf("busy run was aborted after %d events: %v", events, err)
	}
	if res.Text != "slow but alive" || events < 10 {
		t.Fatalf("res=%+v events=%d want the whole run", res, events)
	}
}

func TestWaitAbortsOnContextCancel(t *testing.T) {
	client := newFakeClient(t)
	agent, err := client.Agents().Create(context.Background(), clinesdk.CreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	wd := llmrun.NewIdleWatchdog(context.Background(), 150*time.Millisecond)
	defer wd.Stop()
	ctx := llmrun.WithIdleWatchdog(wd.Context(), wd)

	// The fake bridge holds this run open (no events, no result).
	run, err := agent.Send(ctx, "hang")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	res, err := run.WaitStream(ctx, nil)
	if err == nil {
		t.Fatalf("run was not aborted: %+v", res)
	}
	if !strings.Contains(err.Error(), "idle") {
		t.Fatalf("err=%v want the idle-timeout cause", err)
	}
	if res == nil || res.Status != clinesdk.LLMStatusCancelled {
		t.Fatalf("res=%+v want a cancelled result alongside the error", res)
	}
}

func TestRunErrorSurfacesProviderFailure(t *testing.T) {
	client := newFakeClient(t)
	agent, err := client.Agents().Create(context.Background(), clinesdk.CreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	run, err := agent.Send(context.Background(), "fail me")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	res, err := run.Wait(context.Background())
	if err == nil {
		t.Fatalf("expected a run error, got %+v", res)
	}
	if res.Status != clinesdk.LLMStatusError || !strings.Contains(res.ErrorMessage, "provider exploded") {
		t.Fatalf("res=%+v want the provider error message", res)
	}
}

func eventTypes(events []clinesdk.RunEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Type)
	}
	return out
}
