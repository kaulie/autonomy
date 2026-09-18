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
)

func TestStoreQueryTaskAgentAndStream(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	task := &Task{ID: "task-api", Description: "d", Domain: TaskDomainServer, Status: TaskStatusRunning, AgentID: 10001}
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{ID: 10001, Name: "agent-10001", State: "running", CurrentTask: task, Lifecycle: AgentLifecycleEphemeral}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetTask("task-api")
	if err != nil || got == nil || got.AgentID != 10001 {
		t.Fatalf("GetTask=%+v err=%v", got, err)
	}
	gotAgent, err := store.GetAgent(10001)
	if err != nil || gotAgent == nil || gotAgent.State != "running" {
		t.Fatalf("GetAgent=%+v err=%v", gotAgent, err)
	}

	h, err := store.BeginReasonTurn(ReasonTurn{
		TaskID: "task-api", AgentID: 10001, Cycle: 1, Mode: ReasonModePlan,
		Input: "hello", Status: string(LLMStatusRunning), CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.ActiveReasonTurn("task-api", 10001)
	if err != nil || active == nil || active.ID != h.TurnID {
		t.Fatalf("ActiveReasonTurn=%+v err=%v want turn %d", active, err, h.TurnID)
	}
	if err := store.AppendLLMMessages(h.TurnID, []LLMMessage{{
		TurnID: h.TurnID, TaskID: "task-api", AgentID: 10001, Cycle: 1, Seq: 1,
		Role: LLMMessageRoleThinking, Content: "thinking…", CreatedAt: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.ListLLMMessagesAfter("task-api", 10001, 0, 10)
	if err != nil || len(msgs) < 2 {
		t.Fatalf("ListLLMMessagesAfter=%v err=%v, want input+thinking", msgs, err)
	}
	after := msgs[0].ID
	more, err := store.ListLLMMessagesAfter("task-api", 10001, after, 10)
	if err != nil || len(more) != len(msgs)-1 {
		t.Fatalf("incremental=%v err=%v", more, err)
	}
}

func TestHTTPAPIAcceptProgressAgentStream(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "http.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prevStore, prevAuto, prevFlag := _store, _autonomy, bootstrapFlag
	t.Cleanup(func() { _store, _autonomy, bootstrapFlag = prevStore, prevAuto, prevFlag })
	_store = store
	bootstrapFlag = true
	t.Setenv("AUTONOMY_REASONER", "local")
	t.Setenv("AUTONOMY_MAX_STEPS", "1")

	auto := &Autonomy{
		AgentFactory: NewAgentFactory(),
		Runtime:      NewRuntime(NewAgentFactory()),
		Store:        store,
		MaxSteps:     1,
	}
	_autonomy = auto
	srv := NewHTTPServer(auto)

	body := `{"description":"http api task","domain":"server"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("accept status=%d body=%s", rec.Code, rec.Body.String())
	}
	var accepted AcceptTaskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.TaskID == "" {
		t.Fatal("missing task_id")
	}

	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("health status=%d body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	var progress TaskProgress
	for time.Now().Before(deadline) {
		req = httptest.NewRequest(http.MethodGet, "/api/tasks/"+accepted.TaskID, nil)
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			_ = json.Unmarshal(rec.Body.Bytes(), &progress)
			if progress.AgentID != 0 && progress.Status != TaskStatusPending && progress.Status != TaskStatusRunning {
				break
			}
			if progress.Status == TaskStatusCompleted || progress.Status == TaskStatusError || progress.Status == TaskStatusUnverified {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	if progress.TaskID != accepted.TaskID {
		t.Fatalf("progress=%+v", progress)
	}
	agentID := progress.AgentID
	if agentID == 0 {
		agentID = accepted.AgentID
	}
	if agentID == 0 {
		t.Fatal("agent_id never assigned")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/tasks/"+accepted.TaskID+"/agents/"+strconv.FormatInt(agentID, 10), nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent status=%d body=%s", rec.Code, rec.Body.String())
	}
	var status AgentWorkStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.AgentID != agentID {
		t.Fatalf("status=%+v", status)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/tasks/"+accepted.TaskID+"/agents/"+strconv.FormatInt(agentID, 10)+"/events?last_synced_message_seq=0", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", rec.Code, rec.Body.String())
	}
	var stream AgentStreamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &stream); err != nil {
		t.Fatal(err)
	}
	if stream.TaskID != accepted.TaskID || stream.AgentID != agentID {
		t.Fatalf("stream=%+v", stream)
	}
}
