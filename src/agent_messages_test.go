package autonomy

import (
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
	if len(messages.Received) != 1 || messages.Received[0].Content != "do the thing" || messages.Received[0].Sender != MessageSenderUser {
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
}

// The two pages a dashboard row opens are served, with or without the shell around them.
func TestTheAgentViewPagesAreServed(t *testing.T) {
	server := NewHTTPServer(nil)
	for _, path := range []string{"/agents/10001/events?task=task-1", "/agents/10001/messages", "/agents/10001/events?task=task-1&embed=1&every=15"} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d", path, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("GET %s content-type=%q", path, rec.Header().Get("Content-Type"))
		}
	}
}

// strconvID is the decimal form of an agent id, for building a path.
func strconvID(id int64) string { return strconv.FormatInt(id, 10) }
