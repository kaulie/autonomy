package autonomy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// One agent's message log: what was addressed to it (its inbox) and what it answered (its turns,
// each with the input it was given and the output it produced).
func TestAnAgentsMessagesAreWhatItReceivedAndWhatItAnswered(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	agent := &Agent{State: "idle", Lifecycle: AgentLifecyclePersistent}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueMessage(AgentMessage{
		AgentID: agent.ID, TaskID: "task-msg", Sender: MessageSenderUser, SenderID: "u-1",
		Kind: MessageKindInstruction, Content: "do the thing", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	handle, err := store.BeginReasonTurn(ReasonTurn{
		TaskID: "task-msg", AgentID: agent.ID, Cycle: 1, Mode: ReasonModePlan,
		Input: "do the thing", Status: string(llmbackend.StatusRunning), CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishReasonTurn(handle, llmbackend.RunResult{
		Status: llmbackend.StatusFinished, RawOutput: "did it", StartedAt: time.Now(), EndedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	runtime := &Autonomy{Store: store}
	messages, err := runtime.AgentMessages(agent.ID, 0)
	if err != nil {
		t.Fatalf("AgentMessages: %v", err)
	}
	if len(messages.Received) != 1 || messages.Received[0].Content != "do the thing" || messages.Received[0].Sender != string(MessageSenderUser) {
		t.Fatalf("received=%+v want the inbox message", messages.Received)
	}
	if len(messages.Sent) != 1 {
		t.Fatalf("sent=%+v want this agent's turn", messages.Sent)
	}
	if !strings.Contains(messages.Sent[0].Input, "do the thing") {
		t.Fatalf("sent input=%q want the prompt it answered", messages.Sent[0].Input)
	}
	if !strings.Contains(messages.Sent[0].Output, "did it") {
		t.Fatalf("sent output=%q want its answer", messages.Sent[0].Output)
	}

	// An unknown agent is a 404 over HTTP, and a bad id a 400.
	server := NewHTTPServer(runtime)
	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/agents/999999/messages", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing agent status=%d want 404", missing.Code)
	}
	bad := httptest.NewRecorder()
	server.Handler().ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/agents/not-a-number/messages", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad id status=%d want 400", bad.Code)
	}
	ok := httptest.NewRecorder()
	server.Handler().ServeHTTP(ok, httptest.NewRequest(http.MethodGet, "/api/agents/"+strconvID(agent.ID)+"/messages?limit=10", nil))
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), "do the thing") {
		t.Fatalf("GET messages status=%d body=%s", ok.Code, ok.Body.String())
	}
	// The keys are what a page reads, so they are part of the contract: snake_case, not the store's
	// Go field names (the bug this assertion exists to keep fixed).
	var decoded struct {
		Received []map[string]any `json:"received"`
		Sent     []map[string]any `json:"sent"`
	}
	if err := json.Unmarshal(ok.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Received) != 1 || decoded.Received[0]["sender"] != "user" || decoded.Received[0]["kind"] != "instruction" {
		t.Fatalf("received json=%v want sender/kind spelled for a page", decoded.Received)
	}
	if len(decoded.Sent) != 1 || decoded.Sent[0]["output"] == nil || decoded.Sent[0]["input"] == nil {
		t.Fatalf("sent json=%v want input/output", decoded.Sent)
	}
}

// strconvID is the decimal form of an agent id, for building a path.
func strconvID(id int64) string { return strconv.FormatInt(id, 10) }
