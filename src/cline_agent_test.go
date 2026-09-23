package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"fmt"
	"github.com/kaulie/autonomy/src/llmbackend/cline"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/bridgesdk/fakebridge"
	"github.com/kaulie/autonomy/src/clinesdk"
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
	previousClient := cline.SwapClineClient(nil)
	previousFactory := cline.ClineClientFactory
	cline.ClineClientFactory = func(string) *clinesdk.Client {
		argv, env := fakebridge.Command(os.Args[0])
		manager := clinesdk.NewBridgeManager()
		manager.Command, manager.Env = argv, env
		return clinesdk.NewClient(
			clinesdk.WithManager(manager),
			clinesdk.WithProvider("deepseek"),
			clinesdk.WithModel("deepseek-v4-pro"),
		)
	}
	t.Cleanup(func() {
		client := cline.SwapClineClient(previousClient)
		cline.ClineClientFactory = previousFactory
		if client != nil {
			_ = client.Close()
		}
	})
	// The pool entry these tests run on: an agent's harness, vendor and model come from its
	// account now (src/accounts.go), so a fixture that drives cline turns provides one beside
	// the fake bridge. A test that brought its own store keeps it — the pool is seeded into
	// whatever store is active, so writes and reads stay in the same database.
	if err := ensureTestPool(); err != nil {
		store := resumeTestStore(t)
		seedTestAccounts(t, store)
	}
	if _, err := clineTestAccount(activeStore()); err != nil {
		t.Fatalf("seed cline account: %v", err)
	}
}

// ensureTestPool seeds the pool into the active store, reporting why it could not: a store an
// earlier test left closed is not a store this fixture should write to.
func ensureTestPool() error {
	store := activeStore()
	if store == nil {
		return fmt.Errorf("no active store")
	}
	return seedPool(store)
}

// clineTestAccount puts the cline account the fixtures expect in the pool: vendor deepseek and
// the model the fake bridge is asked for. Idempotent, so every fixture call is safe.
func clineTestAccount(store Store) (Account, error) {
	if existing, err := store.GetAccount(testClineAccountID); err == nil && existing != nil {
		return *existing, nil
	}
	return store.CreateAccount(Account{
		ID: testClineAccountID, Harness: string(llmbackend.Cline), Vendor: "deepseek",
		Label: "test cline", Model: "deepseek-v4-pro", Enabled: true, IsDefault: true,
	})
}

// testClineAccountID is the pool entry the cline fixtures run on (see clineTestAccount).
const testClineAccountID = "acct-test-cline"

// testClineAccount is that entry as an in-memory value, for a test whose agent never reaches a
// store (an agent that already carries its account does not need the pool to agree).
func testClineAccount() *Account {
	return &Account{
		ID: testClineAccountID, Harness: string(llmbackend.Cline), Vendor: "deepseek",
		Label: "test cline", Model: "deepseek-v4-pro", Enabled: true, IsDefault: true,
	}
}

func newClineTestAgent(t *testing.T) *Agent {
	t.Helper()
	agent := &Agent{ID: 9001, Name: "agent-9001", Lifecycle: AgentLifecycleEphemeral, Workspace: t.TempDir()}
	agent.adoptAccount(testClineAccount())
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
	if agent.Backend != llmbackend.Cline || agent.LLMProvider != llmbackend.ProviderCline {
		t.Fatalf("backend=%q provider=%q", agent.Backend, agent.LLMProvider)
	}
	if agent.llm == nil || agent.llm.ProviderSessionID() == "" {
		t.Fatal("no cline session handle was bound")
	}
	// The session id itself is only known once a run started: until then the row keeps
	// nothing to continue from (see TestClineSessionIDIsKeptForTheNextProcess).
	if agent.LLMAgentID != "" {
		t.Fatalf("LLMAgentID=%q, want empty before the first run", agent.LLMAgentID)
	}
	// Attaching twice is a no-op (same handle).
	handle := agent.llm.ProviderSessionID()
	if err := agent.AttachCline(context.Background()); err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	if agent.llm.ProviderSessionID() != handle {
		t.Fatalf("handle changed: %q → %q", handle, agent.llm.ProviderSessionID())
	}
}

func TestPromptLLMStreamMapsClineEventsAndUsage(t *testing.T) {
	installFakeClineClient(t)
	agent := newClineTestAgent(t)

	var events []llmbackend.Event
	text, meta, err := agent.PromptLLMStream(context.Background(), "say pong", ReasonModeAgent, func(ev llmbackend.Event) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if text != "pong" {
		t.Fatalf("text=%q want pong", text)
	}
	if meta.Status != llmbackend.StatusFinished {
		t.Fatalf("status=%q", meta.Status)
	}
	if meta.Usage.InputTokens != 11 || meta.Usage.OutputTokens != 2 || !meta.Usage.CostKnown {
		t.Fatalf("usage=%+v", meta.Usage)
	}
	if meta.RawOutput != "pong" || meta.LLMAgentID == "" {
		t.Fatalf("meta=%+v", meta)
	}

	channels := map[llmbackend.EventChannel]int{}
	for _, ev := range events {
		channels[ev.Channel]++
	}
	for _, want := range []llmbackend.EventChannel{llmbackend.ChannelAssistant, llmbackend.ChannelThought, llmbackend.ChannelStatus, llmbackend.ChannelMeta, llmbackend.ChannelResult} {
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
	yoloHandle := agent.llm.ProviderSessionID()
	if agent.llm.Mode() != clinesdk.DefaultMode {
		t.Fatalf("mode=%q want %q", agent.llm.Mode(), clinesdk.DefaultMode)
	}

	if _, _, err := agent.PromptLLMStream(ctx, "plan something", ReasonModePlan, nil); err != nil {
		t.Fatalf("plan-mode prompt: %v", err)
	}
	if agent.llm.Mode() != "plan" {
		t.Fatalf("mode=%q want plan", agent.llm.Mode())
	}
	if agent.llm.ProviderSessionID() == yoloHandle {
		t.Fatal("a mode switch must move to a fresh session")
	}
	// The id the row keeps is the *plan* session's: that is the task's own conversation,
	// and the one a later process continues (src/clinesdk/bridge/resume.mjs).
	if agent.LLMAgentID != agent.llm.ProviderSessionID() {
		t.Fatalf("agent id not updated: %q vs %q", agent.LLMAgentID, agent.llm.ProviderSessionID())
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

	var events []llmbackend.Event
	text, meta, err := agent.PromptLLMStream(context.Background(), "out of balance", ReasonModeAgent, func(ev llmbackend.Event) {
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
	if meta.Status != llmbackend.StatusError {
		t.Errorf("status=%q want error: an empty answer is not a finished run", meta.Status)
	}
	// What the provider said is kept: it is the only reason there is.
	if !strings.Contains(meta.ErrorMessage, "Insufficient Balance") {
		t.Errorf("error message=%q, want the provider's own", meta.ErrorMessage)
	}

	// And it is in the stream, as an error event the run result sourced.
	var carried bool
	for _, ev := range events {
		if ev.Channel != llmbackend.ChannelError {
			continue
		}
		if llmbackend.PayloadString(llmbackend.PayloadMap(ev.Payload, "error"), "message") != "Insufficient Balance" {
			continue
		}
		if llmbackend.PayloadString(ev.Payload, "source") != "run_result" {
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
	if meta.Status != llmbackend.StatusError {
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
	cases := map[string]llmbackend.Backend{
		"":        llmbackend.Cursor,
		"cursor":  llmbackend.Cursor,
		"cline":   llmbackend.Cline,
		"CLINE":   llmbackend.Cline,
		"unknown": llmbackend.Cursor,
	}
	for env, want := range cases {
		t.Setenv("AUTONOMY_LLM_BACKEND", env)
		if got := llmbackend.DefaultBackend(); got != want {
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

// A session lives inside the bridge process, so a bridge restart takes the conversation
// with it. What the row keeps is the session id: the next process reads that session's
// stored transcript and seeds its own session with it, so the task's conversation
// continues instead of starting over (src/clinesdk/bridge/resume.mjs).
func TestClineSessionIDIsKeptForTheNextProcess(t *testing.T) {
	installFakeClineClient(t)
	store, err := openStore(t, filepath.Join(t.TempDir(), "cline-session.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store

	agent := &Agent{ID: 9002, Name: "agent-9002", Lifecycle: AgentLifecyclePersistent, Workspace: t.TempDir()}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The planner's session is the one whose id the row keeps.
	if err := agent.attachClineMode(ctx, clineModeFor(ReasonModePlan)); err != nil {
		t.Fatalf("attach plan: %v", err)
	}
	if _, _, err := agent.PromptLLMStream(ctx, "plan something", ReasonModePlan, nil); err != nil {
		t.Fatalf("plan prompt: %v", err)
	}
	sessionID := agent.llm.ProviderSessionID()
	if sessionID == "" {
		t.Fatal("the run reported no session id")
	}
	if agent.LLMAgentID != sessionID {
		t.Fatalf("LLMAgentID=%q, want the plan session %q", agent.LLMAgentID, sessionID)
	}
	// The row is what the next process reads, so it has to be written.
	stored, err := store.GetAgent(agent.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetAgent=%+v err=%v", stored, err)
	}
	if stored.LLMAgentID != sessionID {
		t.Fatalf("stored LLMAgentID=%q, want %q", stored.LLMAgentID, sessionID)
	}

	// A worker turn (another mode) runs on its own session and must not replace it.
	if _, _, err := agent.PromptLLMStream(ctx, "say pong", ReasonModeAgent, nil); err != nil {
		t.Fatalf("agent prompt: %v", err)
	}
	if agent.LLMAgentID != sessionID {
		t.Fatalf("LLMAgentID=%q after a worker turn, want the plan session %q", agent.LLMAgentID, sessionID)
	}
}

// Continuing a conversation is asked for on attach: the planner's new session is handed
// the recorded session id to continue from. A worker's session is not — it belongs to one
// delegation and carries its own prompt — and neither is a value that is not a session id
// at all (an older row holds the bridge's own handle, which dies with its bridge).
func TestClineSessionContinuesTheRecordedSession(t *testing.T) {
	installFakeClineClient(t)
	ctx := context.Background()

	agent := &Agent{
		ID: 9003, Name: "agent-9003", Lifecycle: AgentLifecyclePersistent,
		Workspace: t.TempDir(), LLMAgentID: "cls-recorded-session",
	}
	if err := agent.attachClineSession(ctx, clineModeFor(ReasonModePlan), agent.Workspace); err != nil {
		t.Fatalf("attach plan: %v", err)
	}
	if agent.llm.ResumedFrom() != "cls-recorded-session" {
		t.Fatalf("resume=%q, want the recorded session", agent.llm.ResumedFrom())
	}

	// A worker's session starts from nothing.
	worker := &Agent{
		ID: 9004, Name: "agent-9004", Lifecycle: AgentLifecycleEphemeral,
		Workspace: t.TempDir(), LLMAgentID: "cls-recorded-session",
	}
	if err := worker.attachClineSession(ctx, clineModeFor(ReasonModeAgent), worker.Workspace); err != nil {
		t.Fatalf("attach agent mode: %v", err)
	}
	if worker.llm.ResumedFrom() != "" {
		t.Fatalf("worker resume=%q, want none", worker.llm.ResumedFrom())
	}

	// A bridge handle from an older row is not a session to continue.
	legacy := &Agent{
		ID: 9005, Name: "agent-9005", Lifecycle: AgentLifecyclePersistent,
		Workspace: t.TempDir(), LLMAgentID: "cls_7788aabb",
	}
	if err := legacy.attachClineSession(ctx, clineModeFor(ReasonModePlan), legacy.Workspace); err != nil {
		t.Fatalf("attach legacy: %v", err)
	}
	if legacy.llm.ResumedFrom() != "" {
		t.Fatalf("legacy resume=%q, want none", legacy.llm.ResumedFrom())
	}
}
