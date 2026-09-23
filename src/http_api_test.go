package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

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
	store, err := openStore(filepath.Join(t.TempDir(), "api.db"))
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
		Input: "hello", Status: string(llmbackend.StatusRunning), CreatedAt: time.Now(),
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
	store, err := openStore(filepath.Join(t.TempDir(), "http.db"))
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
	// The message id the caller gets back is in the contract's message space: a consumer
	// that numbers its own rows from 1 can keep both in one table and tell them apart.
	if accepted.MessageID < MessageIDBase {
		t.Fatalf("accepted message_id = %d, want at least %d", accepted.MessageID, MessageIDBase)
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
	// The poll cursor lives in the same message space as the accept's message_id.
	for _, event := range stream.Events {
		if event.MessageSeq < MessageIDBase {
			t.Fatalf("event message_seq = %d, want at least %d", event.MessageSeq, MessageIDBase)
		}
	}
}

// TestAgentStreamCarriesTheRunAndTheToolCall: the stream is what a timeline renders, so
// it has to carry a tool call's *call* (name / args / call_id — the half that lives in
// normalized_content) and the run's own result (turns[]: model, duration, how it ended),
// and it has to answer with a 404 rather than an empty conversation when the ids do not
// name one.
func TestAgentStreamCarriesTheRunAndTheToolCall(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "stream.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const taskID = "task-stream"
	if err := store.UpsertTask(&Task{ID: taskID, Description: "watch me", Status: TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{State: "running", LLMProvider: llmbackend.Provider("cline"), Model: "deepseek-v4-flash"}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	// The task's own agent: the stream asks this task for this agent, and the agent's row
	// says that is who it is for.
	if err := store.UpsertTask(&Task{ID: taskID, Description: "watch me", Status: TaskStatusRunning, AgentID: agent.ID}); err != nil {
		t.Fatal(err)
	}
	agent.CurrentTask = &Task{ID: taskID}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	handle, err := store.BeginReasonTurn(ReasonTurn{
		TaskID: taskID, AgentID: agent.ID, Cycle: 1, Mode: ReasonModePlan,
		LLMProvider: agent.LLMProvider, Model: agent.Model, Input: "look around", CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	// One tool message as the aggregator writes it: the result in content, the call in
	// normalized_content (src/llm_message.go).
	if err := store.AppendLLMMessages(handle.TurnID, []LLMMessage{{
		Seq: 1, Role: LLMMessageRoleTool, ParentID: handle.InputMessageID, CreatedAt: at,
		Content:           `[{"exit":0}]`,
		NormalizedContent: `{"name":"shell","call_id":"call-1","args":{"cmd":"ls"}}`,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishReasonTurn(handle, llmbackend.RunResult{
		RawOutput: `{"type":"plan"}`, Status: llmbackend.StatusFinished, ProviderRunID: "run-1",
		DurationMS: 1234, EventCount: 3, EndedAt: at.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewHTTPServer(&Autonomy{Store: store})
	path := "/api/tasks/" + taskID + "/agents/" + strconv.FormatInt(agent.ID, 10) + "/events?last_synced_message_seq=0"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stream = %d %s", rec.Code, rec.Body.String())
	}
	var stream AgentStreamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &stream); err != nil {
		t.Fatal(err)
	}
	var tool *StreamEvent
	for i := range stream.Events {
		if stream.Events[i].Role == LLMMessageRoleTool {
			tool = &stream.Events[i]
		}
	}
	if tool == nil {
		t.Fatalf("no tool event in %+v", stream.Events)
	}
	if tool.Content != `[{"exit":0}]` || !strings.Contains(tool.NormalizedContent, `"name":"shell"`) ||
		!strings.Contains(tool.NormalizedContent, `"call_id":"call-1"`) {
		t.Fatalf("tool event = %+v, want the result and the call's name/call_id", tool)
	}
	if tool.Model != "deepseek-v4-flash" || tool.Provider != "cline" || tool.Cycle != 1 {
		t.Fatalf("tool event lost its run's backend: %+v", tool)
	}
	// The run this page touches, as its own header has it: the timeline closes on this
	// rather than guessing from the last message.
	if len(stream.Turns) != 1 {
		t.Fatalf("turns = %+v, want the one run this page touches", stream.Turns)
	}
	run := stream.Turns[0]
	if run.TurnID != handle.TurnID || run.Status != string(llmbackend.StatusFinished) || run.DurationMS != 1234 ||
		run.Model != "deepseek-v4-flash" || run.Mode != string(ReasonModePlan) || run.Cycle != 1 || run.RunID != "run-1" {
		t.Fatalf("run summary = %+v", run)
	}
	// A poll past the last message has nothing to say and says it: no events, no runs.
	past := "/api/tasks/" + taskID + "/agents/" + strconv.FormatInt(agent.ID, 10) +
		"/events?last_synced_message_seq=" + strconv.FormatInt(stream.LastMessageSeq, 10)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, past, nil))
	var follow AgentStreamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &follow); err != nil {
		t.Fatal(err)
	}
	if len(follow.Events) != 0 || len(follow.Turns) != 0 {
		t.Fatalf("follow-up poll = %+v, want nothing new", follow)
	}

	// An id nobody knows is not an empty conversation: 404 for the task and for the
	// agent, and a 400 for an agent that belongs to a different task.
	other := &Agent{State: "idle"}
	if err := store.UpsertAgent(other); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTask(&Task{ID: "task-other", Description: "someone else", AgentID: other.ID}); err != nil {
		t.Fatal(err)
	}
	other.CurrentTask = &Task{ID: "task-other"}
	if err := store.UpsertAgent(other); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		path string
		want int
	}{
		{"unknown task", "/api/tasks/task-nope/agents/" + strconv.FormatInt(agent.ID, 10) + "/events", http.StatusNotFound},
		{"unknown agent", "/api/tasks/" + taskID + "/agents/999999/events", http.StatusNotFound},
		{"agent of another task", "/api/tasks/" + taskID + "/agents/" + strconv.FormatInt(other.ID, 10) + "/events", http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("GET %s = %d (%s), want %d", tc.path, rec.Code, rec.Body.String(), tc.want)
		}
	}
}
