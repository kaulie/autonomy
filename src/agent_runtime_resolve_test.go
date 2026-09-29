package autonomy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kaulie/autonomy/src/llmbackend"
)

func TestAgentRuntimeResolvesModelForPlanner(t *testing.T) {
	store := resumeTestStore(t)
	cursor, err := store.CreateAccount(Account{
		Harness: "cursor", Label: "cursor test", Model: "composer-2", Enabled: true,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	planner := &Agent{
		Role: AgentRolePlanner, AccountID: cursor.ID, Backend: llmbackend.Cursor,
	}
	if err := store.UpsertAgent(planner); err != nil {
		t.Fatal(err)
	}
	auto := &Autonomy{Store: store, AgentFactory: NewAgentFactory()}
	got, err := auto.AgentRuntime(planner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "composer-2" {
		t.Fatalf("model=%q want composer-2", got.Model)
	}
	if got.Backend != string(llmbackend.Cursor) {
		t.Fatalf("backend=%q want cursor", got.Backend)
	}
	if got.LLMProvider != string(llmbackend.Cursor) {
		t.Fatalf("llm_provider=%q want cursor", got.LLMProvider)
	}
}

func TestAgentRuntimeHTTP(t *testing.T) {
	store := resumeTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	cursor, err := store.CreateAccount(Account{
		Harness: "cursor", Label: "cursor http", Model: "composer-2", Enabled: true,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{
		State: "idle", Lifecycle: AgentLifecyclePersistent,
		Role: AgentRolePlanner, AccountID: cursor.ID, Backend: llmbackend.Cursor,
	}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	auto := &Autonomy{Store: store, AgentFactory: NewAgentFactory()}
	srv := NewHTTPServer(auto)

	path := "/api/agents/" + strconv.FormatInt(agent.ID, 10) + "/runtime"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET runtime = %d body=%s", rec.Code, rec.Body.String())
	}
	var body AgentRuntimeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AgentID != agent.ID {
		t.Fatalf("agent_id=%d want %d", body.AgentID, agent.ID)
	}
	if body.Model == "" {
		t.Fatalf("expected a resolved model, got %+v", body)
	}
}
