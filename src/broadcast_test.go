package autonomy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// A broadcast is one sentence said to many agents at once (src/broadcast.go): the
// agents of one project, or of every project. These tests pin what the scope
// resolves to, that a scope has to be named out loud, that every target receives
// exactly the message an HTTP instruction would have given it, and what happens
// to a target that cannot be reached.

// broadcastFixture is a runtime over its own store, with the process-wide store
// pointing at it (the agents' consumers write through the same one).
func broadcastFixture(t *testing.T) (*Autonomy, rawStore) {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "broadcast.db"))
	if err != nil {
		t.Fatal(err)
	}
	prevStore, prevAuto, prevFlag := _store, _autonomy, bootstrapFlag
	t.Cleanup(func() {
		_store, _autonomy, bootstrapFlag = prevStore, prevAuto, prevFlag
		_ = store.Close()
	})
	_store = store
	bootstrapFlag = true
	auto := &Autonomy{
		AgentFactory: NewAgentFactory(),
		Runtime:      NewRuntime(NewAgentFactory()),
		Store:        store,
		MaxSteps:     1,
	}
	_autonomy = auto
	return auto, store
}

// projectTask is one accepted task in a project, with the live agent it is paired
// with — the shape the runtime keeps for a task that has been accepted
// (docs/agent.md): the project on the task's own row, the agent on agent_id.
//
// The runtime is handed the agent's handle here too, because that is the state a
// broadcast normally meets: an agent is resident, so the task's agent is found
// where the process already holds it rather than rebuilt from its row and
// re-attached to a provider session (that round trip is the subject of
// agent_resume_test.go, not this file's).
func projectTask(t *testing.T, auto *Autonomy, store rawStore, taskID, projectID string) (*Task, *Agent) {
	t.Helper()
	row := &Agent{State: "idle", Lifecycle: AgentLifecyclePersistent}
	if err := store.UpsertAgent(row); err != nil {
		t.Fatal(err)
	}
	task := &Task{
		ID:          taskID,
		Description: "the work of " + taskID,
		Status:      TaskStatusCompleted,
		AgentID:     row.ID,
		ContextRef:  map[ContextContainerType]string{ContextContainerTypeProject: projectID},
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	agent := auto.AgentFactory.Adopt(row)
	if agent == nil {
		t.Fatal("adopt the task's agent")
	}
	agent.Role = AgentRolePlanner
	agent.CurrentTask = task
	return task, agent
}

// inboxOf reads an agent's inbox in arrival order.
func inboxOf(t *testing.T, store rawStore, agentID int64) []AgentMessage {
	t.Helper()
	messages, err := store.ListAgentMessages(agentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

// TestABroadcastReachesTheAgentsOfOneProject: naming a project delivers to that
// project's tasks, and to nothing else.
func TestABroadcastReachesTheAgentsOfOneProject(t *testing.T) {
	auto, store := broadcastFixture(t)
	first, firstAgent := projectTask(t, auto, store, "task-a", "project-1")
	_, otherAgent := projectTask(t, auto, store, "task-other", "project-2")
	second, secondAgent := projectTask(t, auto, store, "task-b", "project-1")

	const message = "先停下手上的活，把线上告警处理掉"
	resp, err := auto.Broadcast(BroadcastRequest{Content: message, ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Scope != BroadcastScopeProject || resp.ProjectID != "project-1" {
		t.Fatalf("scope = %q / %q, want project project-1", resp.Scope, resp.ProjectID)
	}
	if resp.Targets != 2 || resp.Delivered != 2 || resp.Skipped != 0 || resp.Failed != 0 {
		t.Fatalf("report = %+v, want both of project-1's tasks delivered", resp)
	}

	// Each target's inbox has the same one instruction an HTTP accept would have
	// put there: about its own task, from the user, saying exactly this.
	for _, target := range []struct {
		task  *Task
		agent *Agent
	}{{first, firstAgent}, {second, secondAgent}} {
		messages := inboxOf(t, store, target.agent.ID)
		if len(messages) != 1 {
			t.Fatalf("inbox of agent %d = %d messages, want the broadcast", target.agent.ID, len(messages))
		}
		msg := messages[0]
		if msg.TaskID != target.task.ID || msg.Kind != MessageKindInstruction ||
			msg.Sender != MessageSenderUser || msg.Content != message {
			t.Fatalf("message = %+v, want a user instruction about %s", msg, target.task.ID)
		}
	}
	// The same project's tasks are targets; another project's agent is not.
	if messages := inboxOf(t, store, otherAgent.ID); len(messages) != 0 {
		t.Fatalf("agent %d outside the project got %d messages", otherAgent.ID, len(messages))
	}
	// And the report says where each message landed: one line per target,
	// carrying the inbox id the caller can follow up on.
	for _, delivery := range resp.Deliveries {
		if delivery.Status != BroadcastDelivered || delivery.MessageID == 0 || delivery.Queued != 0 {
			t.Fatalf("delivery = %+v, want a delivered message at the head of the queue", delivery)
		}
	}
}

// TestABroadcastToEveryProjectSaysSoOutLoud: "every project" is a scope, not what
// a missing project means — and the two scopes are mutually exclusive.
func TestABroadcastToEveryProjectSaysSoOutLoud(t *testing.T) {
	auto, store := broadcastFixture(t)
	onProject, projectAgent := projectTask(t, auto, store, "task-a", "project-1")
	noProject, looseAgent := projectTask(t, auto, store, "task-b", "")

	if _, err := auto.Broadcast(BroadcastRequest{Content: "hello"}); err == nil {
		t.Fatal("a broadcast without a scope must be refused")
	}
	if _, err := auto.Broadcast(BroadcastRequest{Content: "hello", ProjectID: "project-1", AllProjects: true}); err == nil {
		t.Fatal("a broadcast naming both scopes must be refused")
	}
	if _, err := auto.Broadcast(BroadcastRequest{Content: "  ", AllProjects: true}); err == nil {
		t.Fatal("a broadcast with nothing to say must be refused")
	}

	resp, err := auto.Broadcast(BroadcastRequest{Content: "站会改到十点", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Scope != BroadcastScopeAll || resp.ProjectID != "" {
		t.Fatalf("scope = %q / %q, want all", resp.Scope, resp.ProjectID)
	}
	// A task that names no project still has an agent: "every project" reaches it.
	if resp.Targets != 2 || resp.Delivered != 2 {
		t.Fatalf("report = %+v, want both tasks", resp)
	}
	for _, target := range []struct {
		task  *Task
		agent *Agent
	}{{onProject, projectAgent}, {noProject, looseAgent}} {
		messages := inboxOf(t, store, target.agent.ID)
		if len(messages) != 1 || messages[0].Content != "站会改到十点" {
			t.Fatalf("agent %d inbox = %+v, want the broadcast", target.agent.ID, messages)
		}
	}
}

// TestABroadcastSkipsTargetsItCannotReach: a task with no agent, or one whose
// agent was let go, is reported and left alone — a broadcast does not create an
// agent to hand the message to, and the targets it can reach are unaffected.
func TestABroadcastSkipsTargetsItCannotReach(t *testing.T) {
	auto, store := broadcastFixture(t)
	_, liveAgent := projectTask(t, auto, store, "task-live", "project-1")
	if err := store.UpsertTask(&Task{
		ID: "task-no-agent", Description: "never picked up", Status: TaskStatusPending,
		ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-1"},
	}); err != nil {
		t.Fatal(err)
	}
	_, goneAgent := projectTask(t, auto, store, "task-gone", "project-1")
	if err := store.SoftDeleteAgent(goneAgent.ID); err != nil {
		t.Fatal(err)
	}

	resp, err := auto.Broadcast(BroadcastRequest{Content: "大家看一下这个公告", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Targets != 3 || resp.Delivered != 1 || resp.Skipped != 2 || resp.Failed != 0 {
		t.Fatalf("report = %+v, want one delivered and two skipped", resp)
	}
	reasons := map[string]string{}
	for _, delivery := range resp.Deliveries {
		reasons[delivery.TaskID] = delivery.Status + ": " + delivery.Reason
	}
	if got := reasons["task-no-agent"]; !strings.HasPrefix(got, BroadcastSkipped) || !strings.Contains(got, "no agent") {
		t.Fatalf("task-no-agent = %q, want it skipped for having no agent", got)
	}
	if got := reasons["task-gone"]; !strings.HasPrefix(got, BroadcastSkipped) || !strings.Contains(got, "let go") {
		t.Fatalf("task-gone = %q, want it skipped for its agent being let go", got)
	}
	if messages := inboxOf(t, store, liveAgent.ID); len(messages) != 1 {
		t.Fatalf("the reachable agent got %d messages, want the broadcast", len(messages))
	}
	if messages := inboxOf(t, store, goneAgent.ID); len(messages) != 0 {
		t.Fatalf("the agent that was let go got %d messages", len(messages))
	}
	// The skipped task was not revived: no new agent was made for it.
	task, err := store.GetTask("task-no-agent")
	if err != nil || task == nil || task.AgentID != 0 {
		t.Fatalf("task-no-agent = %+v (err %v), want it left as it was", task, err)
	}
}

// TestBroadcastOverHTTP: the endpoint is the runtime's own door onto a broadcast,
// and what it answers is the report (docs/http-api.md).
func TestBroadcastOverHTTP(t *testing.T) {
	auto, store := broadcastFixture(t)
	task, agent := projectTask(t, auto, store, "task-a", "project-1")
	srv := NewHTTPServer(auto)

	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/broadcast", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"content":"大家注意"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "scope") {
		t.Fatalf("no scope = %d %s, want a refusal that says what is missing", rec.Code, rec.Body.String())
	}
	rec := post(`{"content":"大家注意","project_id":"project-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("broadcast = %d %s", rec.Code, rec.Body.String())
	}
	var resp BroadcastResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Scope != "project" || resp.ProjectID != "project-1" || resp.Targets != 1 || resp.Delivered != 1 {
		t.Fatalf("report = %+v", resp)
	}
	if len(resp.Deliveries) != 1 || resp.Deliveries[0].TaskID != task.ID || resp.Deliveries[0].AgentID != agent.ID {
		t.Fatalf("deliveries = %+v", resp.Deliveries)
	}
	messages := inboxOf(t, store, agent.ID)
	if len(messages) != 1 || messages[0].Content != "大家注意" {
		t.Fatalf("inbox = %+v, want the broadcast", messages)
	}
}

// TestABroadcastJoinsTheAgentsQueue: a broadcast does not overwrite what an agent
// already has — it is one more instruction in the same inbox, behind what is
// there, and the report points at the row it became.
func TestABroadcastJoinsTheAgentsQueue(t *testing.T) {
	auto, store := broadcastFixture(t)
	task, agent := projectTask(t, auto, store, "task-a", "project-1")

	if _, err := auto.AcceptTask(AcceptTaskRequest{ID: task.ID, Description: "first instruction"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	resp, err := auto.Broadcast(BroadcastRequest{Content: "顺便看一眼这个", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Targets != 1 || resp.Delivered != 1 {
		t.Fatalf("report = %+v", resp)
	}
	delivery := resp.Deliveries[0]
	if delivery.AgentID != agent.ID || delivery.TaskID != task.ID {
		t.Fatalf("delivery = %+v, want the task's own agent %d", delivery, agent.ID)
	}
	messages := inboxOf(t, store, agent.ID)
	if len(messages) != 2 {
		t.Fatalf("inbox = %d messages, want the instruction and the broadcast", len(messages))
	}
	if messages[0].Content != "first instruction" || messages[1].ID != delivery.MessageID ||
		messages[1].Content != "顺便看一眼这个" {
		t.Fatalf("inbox = %+v, want the broadcast after the instruction it queued behind", messages)
	}
}
