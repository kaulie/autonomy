package autonomy

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/clinesdk"
	"github.com/kaulie/autonomy/src/clinesdk/fakebridge"
)

// TestFakeBridgeProcess re-executes this test binary as the fake cline bridge,
// which lets the Cline backend be tested without Node, the Cline SDK or a
// provider account.
func TestFakeBridgeProcess(t *testing.T) {
	if !fakebridge.Enabled() {
		t.Skip("helper process only")
	}
	fakebridge.Main()
}

// installFakeClineClient points the process-wide Cline client at the fake bridge
// and resets it afterwards.
func installFakeClineClient(t *testing.T) {
	t.Helper()
	sharedClineMu.Lock()
	previousClient := sharedClineClnt
	previousFactory := clineClientFactory
	sharedClineClnt = nil
	clineClientFactory = func(string) *clinesdk.Client {
		return clinesdk.NewClient(
			clinesdk.WithManager(fakebridge.Manager(os.Args[0])),
			clinesdk.WithProvider("deepseek"),
			clinesdk.WithModel("deepseek-v4-pro"),
		)
	}
	sharedClineMu.Unlock()
	t.Cleanup(func() {
		sharedClineMu.Lock()
		client := sharedClineClnt
		sharedClineClnt = previousClient
		clineClientFactory = previousFactory
		sharedClineMu.Unlock()
		if client != nil {
			_ = client.Close()
		}
	})
}

func newClineTestAgent(t *testing.T) *Agent {
	t.Helper()
	agent := &Agent{ID: 9001, Name: "agent-9001", Lifecycle: AgentLifecycleEphemeral, Workspace: t.TempDir()}
	if err := agent.AttachCline(context.Background()); err != nil {
		t.Fatalf("attach cline: %v", err)
	}
	return agent
}

func TestAttachClineCreatesResidentSession(t *testing.T) {
	installFakeClineClient(t)
	agent := newClineTestAgent(t)

	if err := agent.AttachCline(context.Background()); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if agent.Backend != AgentBackendCline || agent.LLMProvider != LLMProviderCline {
		t.Fatalf("backend=%q provider=%q", agent.Backend, agent.LLMProvider)
	}
	if agent.LLMAgentID == "" || agent.clineAgent == nil {
		t.Fatal("no cline session handle was bound")
	}
	// Attaching twice is a no-op (same handle).
	handle := agent.clineAgent.ID
	if err := agent.AttachCline(context.Background()); err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	if agent.clineAgent.ID != handle {
		t.Fatalf("handle changed: %q → %q", handle, agent.clineAgent.ID)
	}
}

func TestPromptLLMStreamMapsClineEventsAndUsage(t *testing.T) {
	installFakeClineClient(t)
	agent := newClineTestAgent(t)

	var events []LLMEvent
	text, meta, err := agent.PromptLLMStream(context.Background(), "say pong", ReasonModeAgent, func(ev LLMEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if text != "pong" {
		t.Fatalf("text=%q want pong", text)
	}
	if meta.Status != LLMStatusFinished {
		t.Fatalf("status=%q", meta.Status)
	}
	if meta.Usage.InputTokens != 11 || meta.Usage.OutputTokens != 2 || !meta.Usage.CostKnown {
		t.Fatalf("usage=%+v", meta.Usage)
	}
	if meta.RawOutput != "pong" || meta.LLMAgentID == "" {
		t.Fatalf("meta=%+v", meta)
	}

	channels := map[LLMEventChannel]int{}
	for _, ev := range events {
		channels[ev.Channel]++
	}
	for _, want := range []LLMEventChannel{LLMChannelAssistant, LLMChannelThought, LLMChannelStatus, LLMChannelMeta, LLMChannelResult} {
		if channels[want] == 0 {
			t.Fatalf("no %q event in %v", want, channels)
		}
	}

	// The second prompt resumes the same resident session.
	text2, _, err := agent.PromptLLMStream(context.Background(), "and now?", ReasonModeAgent, nil)
	if err != nil {
		t.Fatalf("prompt 2: %v", err)
	}
	if text2 != "tool said hi" {
		t.Fatalf("text2=%q want the tool turn answer", text2)
	}
}

func TestPromptLLMStreamSwitchesSessionMode(t *testing.T) {
	installFakeClineClient(t)
	agent := newClineTestAgent(t)
	ctx := context.Background()

	if _, _, err := agent.PromptLLMStream(ctx, "plan something", ReasonModeAgent, nil); err != nil {
		t.Fatalf("agent-mode prompt: %v", err)
	}
	yoloHandle := agent.clineAgent.ID
	if agent.clineAgent.Mode != clinesdk.DefaultMode {
		t.Fatalf("mode=%q want %q", agent.clineAgent.Mode, clinesdk.DefaultMode)
	}

	if _, _, err := agent.PromptLLMStream(ctx, "plan something", ReasonModePlan, nil); err != nil {
		t.Fatalf("plan-mode prompt: %v", err)
	}
	if agent.clineAgent.Mode != "plan" {
		t.Fatalf("mode=%q want plan", agent.clineAgent.Mode)
	}
	if agent.clineAgent.ID == yoloHandle {
		t.Fatal("a mode switch must move to a fresh session")
	}
	if agent.LLMAgentID != agent.clineAgent.ID {
		t.Fatalf("agent id not updated: %q vs %q", agent.LLMAgentID, agent.clineAgent.ID)
	}
}

// TestPromptLLMStreamTreatsAnEmptyAnswerAsAFailure: a provider can end a run as
// "finished" while having answered nothing at all — an exhausted account does,
// after a long think. That run is a failure, and the row has to say so, or the
// only account of it is the message it carried.
//
// The reason has to reach the stream too: the runtime stores these events, and a
// failure whose reason exists only in the run result is not diagnosable from what
// was recorded.
func TestPromptLLMStreamTreatsAnEmptyAnswerAsAFailure(t *testing.T) {
	installFakeClineClient(t)
	agent := newClineTestAgent(t)

	var events []LLMEvent
	text, meta, err := agent.PromptLLMStream(context.Background(), "out of balance", ReasonModeAgent, func(ev LLMEvent) {
		events = append(events, ev)
	})
	if err == nil {
		t.Fatalf("an empty answer was reported as a run: text=%q meta=%+v", text, meta)
	}
	for _, want := range []string{"empty model response", "status=finished", "Insufficient Balance"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	// An empty answer is the one failure no prompt can fix (quota, a limit, a
	// model that is gone), so the error names where its fix is: the backend a
	// runtime runs on is a runtime setting.
	if !strings.Contains(err.Error(), "AUTONOMY_LLM_BACKEND") {
		t.Errorf("error %q does not name the backend setting", err.Error())
	}
	if meta.Status != LLMStatusError {
		t.Errorf("status=%q want error: an empty answer is not a finished run", meta.Status)
	}
	// What the provider said is kept: it is the only reason there is.
	if !strings.Contains(meta.ErrorMessage, "Insufficient Balance") {
		t.Errorf("error message=%q, want the provider's own", meta.ErrorMessage)
	}

	// And it is in the stream, as an error event the run result sourced.
	var carried bool
	for _, ev := range events {
		if ev.Channel != LLMChannelError {
			continue
		}
		if payloadString(payloadMap(ev.Payload, "error"), "message") != "Insufficient Balance" {
			continue
		}
		if payloadString(ev.Payload, "source") != "run_result" {
			t.Errorf("event=%+v, want it marked as sourced from the run result", ev.Payload)
		}
		carried = true
	}
	if !carried {
		t.Fatalf("no error event carried the reason; events=%+v", events)
	}
}

func TestPromptLLMStreamReportsProviderErrors(t *testing.T) {
	installFakeClineClient(t)
	agent := newClineTestAgent(t)

	_, meta, err := agent.PromptLLMStream(context.Background(), "fail me", ReasonModeAgent, nil)
	if err == nil {
		t.Fatal("expected the provider error to surface")
	}
	if meta.Status != LLMStatusError {
		t.Fatalf("status=%q want error", meta.Status)
	}
	if !strings.Contains(meta.ErrorMessage, "provider exploded") {
		t.Fatalf("error=%q", meta.ErrorMessage)
	}
}

func TestPromptLLMStreamFlowsThroughPromptLLMText(t *testing.T) {
	installFakeClineClient(t)
	agent := newClineTestAgent(t)
	text, err := agent.PromptLLMText(context.Background(), "hello", ReasonModeAgent)
	if err != nil {
		t.Fatalf("text prompt: %v", err)
	}
	if text != "pong" {
		t.Fatalf("text=%q", text)
	}
}

func TestDefaultAgentBackendFromEnv(t *testing.T) {
	cases := map[string]AgentBackend{
		"":        AgentBackendCursor,
		"cursor":  AgentBackendCursor,
		"cline":   AgentBackendCline,
		"CLINE":   AgentBackendCline,
		"unknown": AgentBackendCursor,
	}
	for env, want := range cases {
		t.Setenv("AUTONOMY_LLM_BACKEND", env)
		if got := defaultAgentBackend(); got != want {
			t.Fatalf("AUTONOMY_LLM_BACKEND=%q → %q want %q", env, got, want)
		}
	}
}

func TestClineModeForReasonMode(t *testing.T) {
	if got := clineModeFor(ReasonModePlan); got != "plan" {
		t.Fatalf("plan → %q", got)
	}
	if got := clineModeFor(ReasonModeAgent); got != clinesdk.DefaultMode {
		t.Fatalf("agent → %q", got)
	}
}
