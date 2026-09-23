package codex_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/codexsdk"
	"github.com/kaulie/autonomy/src/codexsdk/fakebridge"
	"github.com/kaulie/autonomy/src/llmbackend"
	"github.com/kaulie/autonomy/src/llmbackend/codex"
)

// The helper below is the test binary re-executed as a fake codex bridge: it speaks the same
// NDJSON protocol as src/codexsdk/bridge/bridge.mjs and the same event shapes, so the
// harness is covered without Node, the Codex CLI or an account.
func TestFakeCodexBridgeProcess(t *testing.T) {
	if !fakebridge.Enabled() {
		t.Skip("helper process only")
	}
	fakebridge.Main()
}

// testHost is the agent a codex session serves, as the backend needs it.
type testHost struct {
	workspace string
	model     string
	sessionID string
	frameSent bool
	backend   llmbackend.Backend
	provider  llmbackend.Provider
	persists  int
}

func (h *testHost) Facts() llmbackend.Facts {
	return llmbackend.Facts{
		Workspace: h.workspace, Model: h.model, SessionID: h.sessionID, FrameSent: h.frameSent,
		Backend: h.backend, Provider: h.provider,
	}
}
func (h *testHost) SetBackend(b llmbackend.Backend, p llmbackend.Provider) {
	h.backend, h.provider = b, p
}
func (h *testHost) SetWorkspace(w string)  { h.workspace = w }
func (h *testHost) SetModel(m string)      { h.model = m }
func (h *testHost) SetSessionID(id string) { h.sessionID = id }
func (h *testHost) SetFrameSent(v bool)    { h.frameSent = v }
func (h *testHost) Persist()               { h.persists++ }

// installFakeCodexClient points the process-wide codex client at the fake bridge.
func installFakeCodexClient(t *testing.T) {
	t.Helper()
	argv, env := fakebridge.Command(os.Args[0])
	manager := codexsdk.NewBridgeManager()
	manager.Command, manager.Env = argv, env
	previous := codex.SwapCodexClient(codexsdk.NewClient(codexsdk.WithManager(manager)))
	t.Cleanup(func() {
		if client := codex.SwapCodexClient(previous); client != nil {
			_ = client.Close()
		}
	})
}

// newHost is an agent on the codex backend with a workspace and a recorded thread id.
func newHost(t *testing.T, sessionID string) *testHost {
	t.Helper()
	return &testHost{
		workspace: t.TempDir(), backend: llmbackend.Codex, provider: llmbackend.ProviderCodex, sessionID: sessionID,
	}
}

// A planner's recorded thread is continued: the harness hands that id back as the thread to
// resume, so the bridge re-attaches the same conversation instead of starting a new one.
func TestAPlannerThreadIsContinuedAcrossProcesses(t *testing.T) {
	installFakeCodexClient(t)
	host := newHost(t, "0192f0aa-thread-recorded")
	session := llmbackend.New(host)

	text, err := session.PromptText(context.Background(), "what is up", llmbackend.ModePlan)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if text != "pong" {
		t.Fatalf("text=%q want pong", text)
	}
	if !session.Resumed() {
		t.Fatal("the session should report that it continued the recorded thread")
	}
	if got := session.ResumedFrom(); got != "0192f0aa-thread-recorded" {
		t.Fatalf("resumedFrom=%q want the recorded thread", got)
	}
	if got := session.ProviderSessionID(); got != "0192f0aa-thread-recorded" {
		t.Fatalf("provider session=%q want the continued thread", got)
	}
	if host.sessionID != "0192f0aa-thread-recorded" {
		t.Fatalf("host session=%q want the thread kept on the row", host.sessionID)
	}
	if host.backend != llmbackend.Codex || host.provider != llmbackend.ProviderCodex {
		t.Fatalf("backend=%q provider=%q want codex/codex", host.backend, host.provider)
	}
}

// A fresh agent opens a thread, and the id its first run reported is recorded — that is what
// the next process continues from.
func TestAFreshThreadIsOpenedAndItsIDRecorded(t *testing.T) {
	installFakeCodexClient(t)
	host := newHost(t, "")
	session := llmbackend.New(host)

	if _, err := session.PromptText(context.Background(), "hello", llmbackend.ModePlan); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if session.Resumed() {
		t.Fatal("a fresh thread is not a resume")
	}
	if host.sessionID == "" {
		t.Fatal("the thread the run reported must be recorded on the agent row")
	}
	if host.persists == 0 {
		t.Fatal("recording the thread must persist the row")
	}
	if host.frameSent {
		t.Fatal("a thread this process opened has not been sent the reasoning frame")
	}
}

// A worker's thread is never continued: it belongs to one delegation.
func TestAWorkerThreadIsNeverContinued(t *testing.T) {
	installFakeCodexClient(t)
	host := newHost(t, "0192f0aa-thread-recorded")
	session := llmbackend.New(host)

	if _, err := session.PromptText(context.Background(), "delegated work", llmbackend.ModeAgent); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if session.Resumed() {
		t.Fatal("a worker's session is fresh, not a resume")
	}
	if got := session.ResumedFrom(); got != "" {
		t.Fatalf("resumedFrom=%q want empty for a worker", got)
	}
	// The worker ran on its own thread, and recording is the planner's: the row keeps the
	// thread the task's own conversation is on.
	if host.sessionID != "0192f0aa-thread-recorded" {
		t.Fatalf("a worker's thread must not replace the planner's recorded thread (got %q)", host.sessionID)
	}
}

// A row that still holds the bridge's own handle (cdx_…) is not something to resume from:
// that handle died with the bridge process that minted it.
func TestABridgeHandleIsNotResumed(t *testing.T) {
	installFakeCodexClient(t)
	host := newHost(t, "cdx_7")
	session := llmbackend.New(host)

	if _, err := session.PromptText(context.Background(), "hello", llmbackend.ModePlan); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if session.Resumed() {
		t.Fatal("a bridge handle is not a resumable thread")
	}
	if host.sessionID == "cdx_7" {
		t.Fatalf("the stale handle should have been replaced, got %q", host.sessionID)
	}
}

// The run's events reach the runtime as neutral events: the model's message and the turn's
// usage, in the keys the trace and aggregation layers read.
func TestTheRunStreamIsMappedToNeutralEvents(t *testing.T) {
	installFakeCodexClient(t)
	host := newHost(t, "")
	session := llmbackend.New(host)

	var kinds []llmbackend.EventKind
	var assistantText, usagePayload string
	if _, _, err := session.Prompt(context.Background(), "hello", llmbackend.ModePlan, func(ev llmbackend.Event) {
		kinds = append(kinds, ev.Kind)
		if ev.Channel == llmbackend.ChannelAssistant {
			assistantText = ev.TextDelta
		}
		if ev.Kind == llmbackend.KindUsage {
			usagePayload = ev.PayloadJSON()
		}
	}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if assistantText != "pong" {
		t.Fatalf("assistant text=%q want pong", assistantText)
	}
	if usagePayload == "" {
		t.Fatal("the turn's usage must arrive as a neutral usage event")
	}
	for _, want := range []string{"\"input_tokens\":11", "\"output_tokens\":2"} {
		if !strings.Contains(usagePayload, want) {
			t.Fatalf("usage payload %s should carry %s", usagePayload, want)
		}
	}
	var sawAssistant, sawUsage bool
	for _, k := range kinds {
		switch k {
		case llmbackend.KindAssistant:
			sawAssistant = true
		case llmbackend.KindUsage:
			sawUsage = true
		}
	}
	if !sawAssistant || !sawUsage {
		t.Fatalf("kinds=%v want an assistant and a usage event", kinds)
	}
}
