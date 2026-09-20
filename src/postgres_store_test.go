package autonomy

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// The postgres engine is exercised against a real PostgreSQL server, in a database of
// its own that the test creates and drops: the SQL is what is under test, so a mock
// would test nothing. Point AUTONOMY_POSTGRES_TEST_DSN at another server to run these
// somewhere else; with no reachable server they skip (they are still compiled and run
// by `go test`, they just have nothing to talk to).

// pgTestDSN is the server the postgres tests use, and whether they may run at all.
func pgTestDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("AUTONOMY_POSTGRES_TEST_DSN")); dsn != "" {
		return dsn
	}
	return "postgres://localhost:5432/postgres?sslmode=disable"
}

var pgTestDBCounter int64

// newPostgresStore opens a Store on a database of its own, created for this test and
// dropped when it ends. Each test gets a database rather than a schema because that is
// what a deployment is: one store, one database.
func newPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	admin, base := pgAdminConn(t)
	name := pgTestDatabase(t, admin)
	store, err := OpenPostgresStore(pgWithDatabase(base, name))
	if err != nil {
		t.Fatalf("OpenPostgresStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// pgAdminConn connects to the server itself (not to a test database), skipping the test
// when there is no server to connect to.
func pgAdminConn(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := pgTestDSN()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("postgres is not configured (%s): %v", dsn, err)
	}
	admin.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := pgTestContext()
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("postgres is not reachable at %s: %v", dsn, err)
	}
	return admin, dsn
}

func pgTestContext() (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// pgWithDatabase is the same DSN pointed at another database on the same server: a URL
// gets its path replaced, a key=value DSN gets dbname=. The tests create their database
// on the server the DSN names, so both spellings have to work.
func pgWithDatabase(dsn, name string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err == nil {
			u.Path = "/" + name
			return u.String()
		}
	}
	if strings.Contains(dsn, "dbname=") {
		return regexpDbname.ReplaceAllString(dsn, "dbname="+name)
	}
	return dsn + " dbname=" + name
}

var regexpDbname = regexp.MustCompile(`dbname=\S+`)

// TestPostgresEngineIsSelectable: the engine registers under its name, needs a DSN
// (there is no meaningful default for a networked database), and is reachable through
// the SPI the rest of the code uses — AUTONOMY_STORE_ENGINE=postgres is the whole
// switch.
func TestPostgresEngineIsSelectable(t *testing.T) {
	names := StoreEngineNames()
	if !slices.Contains(names, StoreEnginePostgres) {
		t.Fatalf("registered engines = %v, want %s among them", names, StoreEnginePostgres)
	}

	// No DSN anywhere: choosing postgres must fail loudly rather than guess a server.
	t.Setenv(EnvPostgresDSN, "")
	t.Setenv(EnvPostgresDatabaseURL, "")
	t.Setenv(EnvStoreDSN, "")
	if _, err := OpenStore(StoreEnginePostgres, ""); err == nil {
		t.Fatal("OpenStore(postgres, no dsn) succeeded; it must ask for one")
	}

	// The whole path: env selects the engine, the engine's DSN comes from its own
	// variable, and the result is a usable Store.
	admin, base := pgAdminConn(t)
	db := pgTestDatabase(t, admin)
	t.Setenv(EnvPostgresDSN, pgWithDatabase(base, db))
	store, err := OpenStore(StoreEnginePostgres, "")
	if err != nil {
		t.Fatalf("OpenStore(%s): %v", StoreEnginePostgres, err)
	}
	defer store.Close()
	if _, ok := store.(*PostgresStore); !ok {
		t.Fatalf("OpenStore returned %T, want a *PostgresStore", store)
	}
}

// TestPostgresStoreTaskAndAgent: the task and agent rows round-trip, including the
// empty-value semantics UpsertTask is documented with (a write that says nothing about
// what the task is leaves what is there) and the id/name pairing of a new agent.
func TestPostgresStoreTaskAndAgent(t *testing.T) {
	store := newPostgresStore(t)

	task := &Task{
		ID: "task-1", Description: "ship it", Domain: TaskDomain("code"), GoalType: GoalType("merge"),
		ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-1"},
		Status:     "running",
		AgentID:    0,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	read, err := store.GetTask("task-1")
	if err != nil || read == nil {
		t.Fatalf("GetTask = %+v, %v", read, err)
	}
	if read.Description != "ship it" || read.Domain != TaskDomain("code") || read.GoalType != GoalType("merge") ||
		read.ContextRef[ContextContainerTypeProject] != "project-1" || read.Status != "running" {
		t.Fatalf("task did not round-trip: %+v", read)
	}
	if read.CreatedAt.IsZero() || read.UpdatedAt.IsZero() {
		t.Fatalf("task lost its timestamps: %+v", read)
	}

	// A write on the way through a run carries neither the task's world nor its agent:
	// it must not take them away.
	if err := store.UpsertTask(&Task{ID: "task-1", Description: "ship it", Status: "completed"}); err != nil {
		t.Fatalf("UpsertTask (second): %v", err)
	}
	read, err = store.GetTask("task-1")
	if err != nil || read == nil {
		t.Fatalf("GetTask (second) = %+v, %v", read, err)
	}
	if read.GoalType != GoalType("merge") || read.ContextRef[ContextContainerTypeProject] != "project-1" {
		t.Fatalf("a write that names neither goal type nor world stripped the task: %+v", read)
	}
	if read.Status != "completed" {
		t.Fatalf("status did not follow the write: %+v", read)
	}

	agent := &Agent{State: "idle", LLMProvider: LLMProvider("cline"), Model: "m", CurrentTask: task}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	if agent.ID < 10000 || agent.Name != fmt.Sprintf("agent-%d", agent.ID) {
		t.Fatalf("new agent = %d/%q, want an id from 10000 up and agent-<id>", agent.ID, agent.Name)
	}
	got, err := store.GetAgent(agent.ID)
	if err != nil || got == nil {
		t.Fatalf("GetAgent = %+v, %v", got, err)
	}
	if got.Name != agent.Name || got.CurrentTask == nil || got.CurrentTask.ID != "task-1" || got.Model != "m" {
		t.Fatalf("agent did not round-trip: %+v", got)
	}
	if err := store.UpsertTask(&Task{ID: "task-1", Description: "ship it", AgentID: agent.ID}); err != nil {
		t.Fatalf("UpsertTask (agent): %v", err)
	}
	read, _ = store.GetTask("task-1")
	if read.AgentID != agent.ID {
		t.Fatalf("task did not take its agent: %+v", read)
	}
	// A later write that names no agent keeps the pair.
	if err := store.UpsertTask(&Task{ID: "task-1", Description: "ship it", Status: "completed"}); err != nil {
		t.Fatalf("UpsertTask (no agent): %v", err)
	}
	read, _ = store.GetTask("task-1")
	if read.AgentID != agent.ID {
		t.Fatalf("a write that names no agent un-paired the task: %+v", read)
	}

	if err := store.SoftDeleteAgent(agent.ID); err != nil {
		t.Fatalf("SoftDeleteAgent: %v", err)
	}
	got, err = store.GetAgent(agent.ID)
	if err != nil || got == nil {
		t.Fatalf("GetAgent (deleted) = %+v, %v", got, err)
	}
	if got.DeletedAt.IsZero() || got.State != "deleted" {
		t.Fatalf("soft delete did not land: %+v", got)
	}

	tasks, err := store.ListTasks()
	if err != nil || len(tasks) != 1 || tasks[0].ID != "task-1" {
		t.Fatalf("ListTasks = %+v, %v", tasks, err)
	}
}

// pgTestDatabase creates a database of its own for one test and returns its name.
func pgTestDatabase(t *testing.T, admin *sql.DB) string {
	t.Helper()
	name := fmt.Sprintf("autonomy_test_%d_%d", os.Getpid(), atomic.AddInt64(&pgTestDBCounter, 1))
	if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`); err != nil {
		t.Fatalf("drop leftover test database: %v", err)
	}
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`); err != nil {
			t.Errorf("drop test database %s: %v", name, err)
		}
	})
	return name
}

// TestPostgresStoreConversationRoundTrip: one run written in one shot, one run streamed
// and finished, and the reads the HTTP poll API and the data API make of them.
func TestPostgresStoreConversationRoundTrip(t *testing.T) {
	store := newPostgresStore(t)

	// A one-shot interaction: header plus the input and the return as two linked rows.
	done := time.Date(2026, 9, 20, 9, 30, 0, 0, time.UTC)
	if err := store.InsertReasonTurn(ReasonTurn{
		TaskID: "task-1", AgentID: 10001, Cycle: 1, Mode: ReasonModePlan,
		LLMProvider: LLMProvider("cline"), Model: "m", Input: "do it", RawOutput: `{"type":"done"}`,
		Status: string(LLMStatusFinished), CreatedAt: done, StartedAt: done, EndedAt: done,
	}); err != nil {
		t.Fatalf("InsertReasonTurn: %v", err)
	}
	turns, total, err := store.QueryTurns(TurnQuery{})
	if err != nil || total != 1 || len(turns) != 1 {
		t.Fatalf("QueryTurns = %d rows, %d total, %v", len(turns), total, err)
	}
	turnID := turns[0].ID
	if turns[0].NormalizedOutput == "" {
		t.Fatalf("normalized_output was not derived at insert: %+v", turns[0])
	}
	if turns[0].CreatedAt != "2026-09-20T09:30:00Z" {
		t.Fatalf("created_at reads as text %q, want the stored instant", turns[0].CreatedAt)
	}
	msgs, err := store.ListLLMMessages(turnID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("ListLLMMessages = %d rows, %v", len(msgs), err)
	}
	if msgs[0].Role != LLMMessageRoleUser || msgs[0].Content != "do it" || msgs[0].Seq != llmMessageSeqUser {
		t.Fatalf("input message = %+v", msgs[0])
	}
	if msgs[1].Role != LLMMessageRoleAssistant || msgs[1].ParentID != msgs[0].ID || msgs[1].Seq != llmMessageSeqAssistant {
		t.Fatalf("assistant message does not answer the input: %+v", msgs[1])
	}
	if id, found, err := store.AssistantMessageID(turnID); err != nil || !found || id != msgs[1].ID {
		t.Fatalf("AssistantMessageID = %d, %v, %v (want %d)", id, found, err, msgs[1].ID)
	}

	// A streamed run: begin, append events (twice, to prove the appends are idempotent),
	// append the aggregated messages while it streams, then finish.
	h, err := store.BeginReasonTurn(ReasonTurn{
		TaskID: "task-2", AgentID: 10002, Cycle: 1, Mode: ReasonModeAgent,
		LLMProvider: LLMProvider("cursor"), Model: "composer", Input: "ask",
	})
	if err != nil {
		t.Fatalf("BeginReasonTurn: %v", err)
	}
	active, err := store.ActiveReasonTurn("task-2", 10002)
	if err != nil || active == nil || active.ID != h.TurnID {
		t.Fatalf("ActiveReasonTurn = %+v, %v", active, err)
	}
	events := []LLMEvent{
		{Seq: 1, Channel: LLMChannelAssistant, Kind: LLMKindAssistantDelta, EventType: "assistant_delta", TextDelta: "hel", CreatedAt: done},
		{Seq: 2, Channel: LLMChannelAssistant, Kind: LLMKindAssistantDelta, EventType: "assistant_delta", TextDelta: "lo", CreatedAt: done},
	}
	for i := 0; i < 2; i++ {
		if err := store.AppendLLMEvents(h.TurnID, "run-1", events); err != nil {
			t.Fatalf("AppendLLMEvents (pass %d): %v", i+1, err)
		}
	}
	stored, err := store.ListLLMEvents(h.TurnID)
	if err != nil || len(stored) != 2 || stored[1].TextDelta != "lo" {
		t.Fatalf("ListLLMEvents = %+v, %v (the second append must be a no-op)", stored, err)
	}
	if err := store.AppendLLMMessages(h.TurnID, []LLMMessage{
		{Seq: 1, Role: LLMMessageRoleThinking, ParentID: h.InputMessageID, Content: "think", NormalizedContent: "think", CreatedAt: done},
	}); err != nil {
		t.Fatalf("AppendLLMMessages: %v", err)
	}
	// The same row again, grown: an upsert, not a second row.
	if err := store.AppendLLMMessages(h.TurnID, []LLMMessage{
		{Seq: 1, Role: LLMMessageRoleThinking, ParentID: h.InputMessageID, Content: "thought", NormalizedContent: "thought", CreatedAt: done},
	}); err != nil {
		t.Fatalf("AppendLLMMessages (second): %v", err)
	}
	if err := store.FinishReasonTurn(h, LLMRunResult{
		RawOutput: "hello", Status: LLMStatusFinished, ProviderRunID: "run-1", LLMAgentID: "bc-1",
		DurationMS: 1200, EventCount: 2, EndedAt: done.Add(2 * time.Second),
		Usage: LLMUsage{InputTokens: 10, OutputTokens: 3, TotalTokens: 13, CostCents: 0.5, CostKnown: true},
	}); err != nil {
		t.Fatalf("FinishReasonTurn: %v", err)
	}
	// A replayed finish changes nothing.
	if err := store.FinishReasonTurn(h, LLMRunResult{
		RawOutput: "hello", Status: LLMStatusFinished, ProviderRunID: "run-1", EndedAt: done.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("FinishReasonTurn (replay): %v", err)
	}
	msgs, err = store.ListLLMMessages(h.TurnID)
	if err != nil {
		t.Fatalf("ListLLMMessages (streamed): %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("streamed run has %d messages, want input + thinking + return: %+v", len(msgs), msgs)
	}
	if msgs[1].Content != "thought" || msgs[1].Seq != 1 {
		t.Fatalf("the aggregated row was not upserted in place: %+v", msgs[1])
	}
	if msgs[2].Role != LLMMessageRoleAssistant || msgs[2].Content != "hello" || msgs[2].Seq != 2 {
		t.Fatalf("the return is not the run's last message: %+v", msgs[2])
	}
	if active, err := store.ActiveReasonTurn("task-2", 10002); err != nil || active != nil {
		t.Fatalf("a finished run is still active: %+v, %v", active, err)
	}

	// The cross-turn sync cursor the HTTP poll API exposes, and the events' run id.
	stream, err := store.ListLLMMessagesAfter("task-2", 10002, 0, 0)
	if err != nil || len(stream) != 3 {
		t.Fatalf("ListLLMMessagesAfter = %d rows, %v", len(stream), err)
	}
	if after, err := store.ListLLMMessagesAfter("task-2", 10002, stream[0].ID, 0); err != nil || len(after) != 2 {
		t.Fatalf("ListLLMMessagesAfter(after first) = %d rows, %v", len(after), err)
	}
	run, _, err := store.QueryTurns(TurnQuery{})
	if err != nil {
		t.Fatalf("QueryTurns: %v", err)
	}
	if len(run) == 0 {
		t.Fatal("QueryTurns returned nothing to check the run id backfill against")
	}
	for _, rec := range run {
		if rec.ID == h.TurnID && rec.RunID != "run-1" {
			t.Fatalf("run_id was not written to the header: %+v", rec)
		}
	}
	if n, err := store.CountTurns(); err != nil || n != 2 {
		t.Fatalf("CountTurns = %d, %v", n, err)
	}
}

// TestPostgresStoreInbox: the queue an agent's messages wait in — arrival order, one
// claim at a time, the counters the acceptance answers with, and the requeue that gives
// a dead process's message back.
func TestPostgresStoreInbox(t *testing.T) {
	store := newPostgresStore(t)
	const agentID = 10001
	var ids []int64
	for _, kind := range []string{"instruction", "note", "instruction"} {
		id, err := store.EnqueueMessage(AgentMessage{
			AgentID: agentID, TaskID: "task-1", Sender: MessageSender("user"), SenderID: "u-1",
			Kind: AgentMessageKind(kind), Content: kind + "-" + fmt.Sprint(len(ids)),
		})
		if err != nil {
			t.Fatalf("EnqueueMessage: %v", err)
		}
		ids = append(ids, id)
	}
	if queued, err := store.CountQueuedMessages(agentID); err != nil || queued != 3 {
		t.Fatalf("CountQueuedMessages = %d, %v", queued, err)
	}
	// The last one asked about has two messages ahead of it: both earlier ones are still
	// unfinished.
	if ahead, err := store.CountMessagesAhead(agentID, ids[2]); err != nil || ahead != 2 {
		t.Fatalf("CountMessagesAhead = %d, %v", ahead, err)
	}

	first, found, err := store.ClaimNextMessage(agentID)
	if err != nil || !found || first.ID != ids[0] {
		t.Fatalf("ClaimNextMessage = %+v, %v, %v (want the oldest queued)", first, found, err)
	}
	if first.Status != MessageStatusRunning || first.StartedAt.IsZero() || first.Content != "instruction-0" {
		t.Fatalf("claimed message = %+v", first)
	}
	// The claimed message is being processed: it is no longer queued, but it is still
	// ahead of the last one.
	if queued, err := store.CountQueuedMessages(agentID); err != nil || queued != 2 {
		t.Fatalf("CountQueuedMessages after claim = %d, %v", queued, err)
	}
	if ahead, err := store.CountMessagesAhead(agentID, ids[2]); err != nil || ahead != 2 {
		t.Fatalf("CountMessagesAhead after claim = %d, %v", ahead, err)
	}
	if err := store.FinishAgentMessage(first.ID, MessageStatusDone, ""); err != nil {
		t.Fatalf("FinishAgentMessage: %v", err)
	}
	if ahead, err := store.CountMessagesAhead(agentID, ids[2]); err != nil || ahead != 1 {
		t.Fatalf("CountMessagesAhead after finish = %d, %v", ahead, err)
	}

	second, found, err := store.ClaimNextMessage(agentID)
	if err != nil || !found || second.ID != ids[1] {
		t.Fatalf("ClaimNextMessage (second) = %+v, %v, %v", second, found, err)
	}
	// A process that dies mid-message leaves it running: requeue puts it back, in its
	// place, and the next claim is that same message again.
	if err := store.RequeueRunningMessages(agentID); err != nil {
		t.Fatalf("RequeueRunningMessages: %v", err)
	}
	again, found, err := store.ClaimNextMessage(agentID)
	if err != nil || !found || again.ID != ids[1] {
		t.Fatalf("ClaimNextMessage after requeue = %+v, %v, %v", again, found, err)
	}
	if again.StartedAt.IsZero() {
		t.Fatalf("a re-claimed message lost its claim time: %+v", again)
	}
	if err := store.FinishAgentMessage(again.ID, MessageStatusFailed, "boom"); err != nil {
		t.Fatalf("FinishAgentMessage (failed): %v", err)
	}

	all, err := store.ListAgentMessages(agentID, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListAgentMessages = %d rows, %v", len(all), err)
	}
	for i, want := range ids {
		if all[i].ID != want {
			t.Fatalf("inbox order = %d at %d, want %d", all[i].ID, i, want)
		}
	}
	if all[1].Status != MessageStatusFailed || all[1].Error != "boom" || all[1].EndedAt.IsZero() {
		t.Fatalf("failed message = %+v", all[1])
	}
	if limited, err := store.ListAgentMessages(agentID, 2); err != nil || len(limited) != 2 {
		t.Fatalf("ListAgentMessages(limit 2) = %d rows, %v", len(limited), err)
	}
	// An empty queue answers nothing rather than erroring, and an agent nobody wrote to
	// is simply empty.
	third, found, err := store.ClaimNextMessage(agentID)
	if err != nil || !found || third.ID != ids[2] {
		t.Fatalf("ClaimNextMessage (third) = %+v, %v, %v", third, found, err)
	}
	if err := store.FinishAgentMessage(third.ID, MessageStatusDone, ""); err != nil {
		t.Fatalf("FinishAgentMessage (third): %v", err)
	}
	if again, found, err := store.ClaimNextMessage(agentID); err != nil || found {
		t.Fatalf("ClaimNextMessage with only finished messages = %+v, %v, %v", again, found, err)
	}
	if _, found, err := store.ClaimNextMessage(10002); err != nil || found {
		t.Fatalf("ClaimNextMessage on an empty inbox = %v, %v", found, err)
	}
}

// TestPostgresStoreExecutionAndVerification: the record of what the runtime planned,
// what it did, what it judged, and the links that make a plan traceable to the input
// that asked for it.
func TestPostgresStoreExecutionAndVerification(t *testing.T) {
	store := newPostgresStore(t)

	turn, err := store.BeginReasonTurn(ReasonTurn{
		TaskID: "task-1", AgentID: 10001, Cycle: 1, Mode: ReasonModePlan,
		LLMProvider: LLMProvider("cline"), Model: "m", Input: "build the thing",
	})
	if err != nil {
		t.Fatalf("BeginReasonTurn: %v", err)
	}
	if err := store.FinishReasonTurn(turn, LLMRunResult{
		RawOutput: `{"type":"plan"}`, Status: LLMStatusFinished, ProviderRunID: "run-9",
	}); err != nil {
		t.Fatalf("FinishReasonTurn: %v", err)
	}
	replyID, found, err := store.AssistantMessageID(turn.TurnID)
	if err != nil || !found {
		t.Fatalf("AssistantMessageID = %d, %v, %v", replyID, found, err)
	}

	planID, err := store.CreateExecutionPlan(ExecutionPlan{
		TaskID: "task-1", AgentID: 10001, Cycle: 1, DecisionType: "plan", Reason: "because",
		Evidence: "[]", Need: `{"slot":"shell"}`, StepCount: 2, PlanHash: "hash",
		ReplyMessageID: replyID, InputMessageID: turn.InputMessageID, ReasonTurnID: turn.TurnID,
	})
	if err != nil || planID == 0 {
		t.Fatalf("CreateExecutionPlan = %d, %v", planID, err)
	}
	if err := store.AppendExecutionStepPlans([]ExecutionStepPlan{
		{PlanID: planID, Idx: 1, Name: "build", Capability: "shell", Input: `{"cmd":"make"}`, ExpectedEffect: "it builds"},
		{PlanID: planID, Idx: 2, Name: "deploy", Capability: "deploy", Input: `{"target":"local"}`, ExpectedEffect: "it runs"},
	}); err != nil {
		t.Fatalf("AppendExecutionStepPlans: %v", err)
	}
	planned, err := store.ListExecutionStepPlan(planID)
	if err != nil || len(planned) != 2 || planned[1].Name != "deploy" {
		t.Fatalf("ListExecutionStepPlan = %+v, %v", planned, err)
	}

	started := time.Now().Add(-2 * time.Second)
	stepID, err := store.AppendExecutionStep(ExecutionStep{
		PlanID: planID, PlanStepID: planned[0].ID, TaskID: "task-1", AgentID: 10001, Cycle: 1, Idx: 1,
		Name: "build", Capability: "shell", Provider: "local", Status: "ok",
		Input: `{"cmd":"make"}`, Output: `{"exit":0}`, StartedAt: started, EndedAt: time.Now(), DurationMS: 2000,
	})
	if err != nil || stepID == 0 {
		t.Fatalf("AppendExecutionStep = %d, %v", stepID, err)
	}
	if _, err := store.AppendExecutionStepInteraction(ExecutionStepInteraction{
		StepID: stepID, Seq: 1, Kind: "llm", Provider: "cline", ReasonTurnID: turn.TurnID,
	}); err != nil {
		t.Fatalf("AppendExecutionStepInteraction: %v", err)
	}
	steps, err := store.ListExecutionSteps(planID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("ListExecutionSteps = %+v, %v", steps, err)
	}
	if steps[0].EndedAt.IsZero() || !steps[0].StartedAt.Equal(started.UTC()) {
		t.Fatalf("execution step lost its timing: %+v", steps[0])
	}
	interactions, err := store.ListExecutionStepInteractions(stepID)
	if err != nil || len(interactions) != 1 || interactions[0].ReasonTurnID != turn.TurnID {
		t.Fatalf("ListExecutionStepInteractions = %+v, %v", interactions, err)
	}
	// A step that was planned but never ran has no execution row: the outcome says how
	// much of the plan ran and how the last step of it ended.
	outcome, ok, err := store.ExecutionPlanOutcome(planID)
	if err != nil || !ok || outcome.Planned != 2 || outcome.Executed != 1 || outcome.Status != "ok" {
		t.Fatalf("ExecutionPlanOutcome = %+v, %v, %v", outcome, ok, err)
	}

	// The task's own input is reachable from any of its plans, and the plan is reachable
	// from its task in creation order.
	planID2, err := store.CreateExecutionPlan(ExecutionPlan{
		TaskID: "task-1", AgentID: 10001, Cycle: 2, DecisionType: "done",
		TaskInputMessageID: turn.InputMessageID,
	})
	if err != nil {
		t.Fatalf("CreateExecutionPlan (second): %v", err)
	}
	if inputID, found, err := store.TaskInputMessageID("task-1"); err != nil || !found || inputID != turn.InputMessageID {
		t.Fatalf("TaskInputMessageID = %d, %v, %v (want %d)", inputID, found, err, turn.InputMessageID)
	}
	plans, err := store.ListExecutionPlans("task-1")
	if err != nil || len(plans) != 2 || plans[0].ID != planID || plans[1].ID != planID2 {
		t.Fatalf("ListExecutionPlans = %+v, %v", plans, err)
	}
	if plans[1].TaskInputMessageID != turn.InputMessageID || plans[1].ReasonTurnID != 0 {
		t.Fatalf("plan links were not read back: %+v", plans[1])
	}
	if _, ok, err := store.ExecutionPlanOutcome(planID2); err != nil || ok {
		t.Fatalf("a plan whose steps never ran reported an outcome: %v, %v", ok, err)
	}

	// The Completion Contract is pinned once per (task, criterion) and cannot be
	// rewritten; verdicts are appended and read in order.
	if err := store.AppendCompletionContract(ContractCriterion{
		TaskID: "task-1", Idx: 1, PlanID: planID, Name: "service is up", Criterion: `{"requirement":"the service answers"}`,
	}); err != nil {
		t.Fatalf("AppendCompletionContract: %v", err)
	}
	if err := store.AppendCompletionContract(ContractCriterion{
		TaskID: "task-1", Idx: 1, PlanID: planID, Name: "service is up", Criterion: `{"requirement":"something weaker"}`,
	}); err != nil {
		t.Fatalf("AppendCompletionContract (restated): %v", err)
	}
	contract, err := store.ListCompletionContract("task-1")
	if err != nil || len(contract) != 1 || !strings.Contains(contract[0].Criterion, "the service answers") {
		t.Fatalf("contract = %+v, %v (the first criterion must win)", contract, err)
	}
	for _, verdict := range []Verification{
		{TaskID: "task-1", PlanID: planID, Cycle: 1, Criterion: "service is up", Requirement: "the service answers", Method: "world_model", Evidence: `{"slot":"service"}`, Result: "fail", Reason: "nothing listens"},
		{TaskID: "task-1", PlanID: planID, Cycle: 2, Criterion: "service is up", Requirement: "the service answers", Method: "registry:http", Evidence: `{"reference":"http://127.0.0.1:8080/health"}`, Observed: "200", Result: "pass"},
	} {
		if _, err := store.AppendVerification(verdict); err != nil {
			t.Fatalf("AppendVerification: %v", err)
		}
	}
	verdicts, err := store.ListVerifications("task-1")
	if err != nil || len(verdicts) != 2 {
		t.Fatalf("ListVerifications = %+v, %v", verdicts, err)
	}
	if verdicts[0].Result != "fail" || verdicts[1].Result != "pass" || verdicts[1].Observed != "200" {
		t.Fatalf("verdicts out of order or missing fields: %+v", verdicts)
	}
	// An empty JSON column is still JSON a reader can parse.
	if verdicts[0].Evidence != `{"slot":"service"}` {
		t.Fatalf("evidence was reformatted: %q", verdicts[0].Evidence)
	}
}

// TestPostgresStoreTurnQueries: the data API's read half — the list page's filter,
// order and page, the filter bar's facets, the task selector's candidates, one task's
// execution series, and the count the API describes itself with.
func TestPostgresStoreTurnQueries(t *testing.T) {
	store := newPostgresStore(t)

	planner := &Agent{LLMProvider: LLMProvider("cline"), Model: "m"}
	if err := store.UpsertAgent(planner); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	worker := &Agent{LLMProvider: LLMProvider("cursor"), Model: "composer"}
	if err := store.UpsertAgent(worker); err != nil {
		t.Fatalf("UpsertAgent (worker): %v", err)
	}

	base := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	seed := func(taskID string, agent *Agent, cycle int, at time.Time, mode ReasonMode, status, input, output string) int64 {
		t.Helper()
		if err := store.InsertReasonTurn(ReasonTurn{
			TaskID: taskID, AgentID: agent.ID, Cycle: cycle, Mode: mode,
			LLMProvider: agent.LLMProvider, Model: agent.Model, Input: input, RawOutput: output,
			Status: status, CreatedAt: at, StartedAt: at, EndedAt: at,
		}); err != nil {
			t.Fatalf("InsertReasonTurn: %v", err)
		}
		turns, _, err := store.QueryTurns(TurnQuery{TaskID: taskID, Limit: 1})
		if err != nil || len(turns) == 0 {
			t.Fatalf("QueryTurns after seed: %v", err)
		}
		return turns[0].ID
	}

	if err := store.UpsertTask(&Task{ID: "task-a", Description: "first", Status: "completed"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if err := store.UpsertTask(&Task{ID: "task-b", Description: "second", Status: "error"}); err != nil {
		t.Fatalf("UpsertTask (b): %v", err)
	}
	if err := store.UpsertTask(&Task{ID: "task-c", Description: "accepted, never run"}); err != nil {
		t.Fatalf("UpsertTask (c): %v", err)
	}

	first := seed("task-a", planner, 1, base, ReasonModePlan, string(LLMStatusFinished), "wire up the deploy 100%", "planned")
	seed("task-a", planner, 2, base.Add(time.Minute), ReasonModePlan, string(LLMStatusError), "it failed", "Insufficient Balance")
	seed("task-a", worker, 1, base.Add(2*time.Minute), ReasonModeAgent, string(LLMStatusFinished), "delegated the deploy", "done")
	seed("task-b", worker, 1, base.Add(3*time.Minute), ReasonModeAgent, string(LLMStatusFinished), "another task", "ok")
	// A task nobody ever wrote a row for: it only exists in the log.
	seed("task-ghost", planner, 1, base.Add(4*time.Minute), ReasonModePlan, string(LLMStatusFinished), "ghost", "gone")

	if n, err := store.CountTurns(); err != nil || n != 5 {
		t.Fatalf("CountTurns = %d, %v", n, err)
	}

	// Filters, and the count that matches them (not the table's).
	page, total, err := store.QueryTurns(TurnQuery{TaskID: "task-a"})
	if err != nil || total != 3 || len(page) != 3 {
		t.Fatalf("QueryTurns(task-a) = %d rows, %d total, %v", len(page), total, err)
	}
	if page[0].TaskID != "task-a" {
		t.Fatalf("QueryTurns(task-a) returned %+v", page[0])
	}
	// The reading order is newest first.
	if !(page[0].CreatedAt > page[2].CreatedAt) {
		t.Fatalf("default order is not newest first: %+v", page)
	}
	if _, total, err := store.QueryTurns(TurnQuery{Status: string(LLMStatusError)}); err != nil || total != 1 {
		t.Fatalf("QueryTurns(status=error) total = %d, %v", total, err)
	}
	if _, total, err := store.QueryTurns(TurnQuery{Model: "composer"}); err != nil || total != 2 {
		t.Fatalf("QueryTurns(model=composer) total = %d, %v", total, err)
	}
	if _, total, err := store.QueryTurns(TurnQuery{Mode: ReasonModePlan}); err != nil || total != 3 {
		t.Fatalf("QueryTurns(mode=plan) total = %d, %v", total, err)
	}
	if _, total, err := store.QueryTurns(TurnQuery{Agent: planner.Name}); err != nil || total != 3 {
		t.Fatalf("QueryTurns(agent=%s) total = %d, %v", planner.Name, total, err)
	}

	// The search is a literal substring: a term with a wildcard in it matches itself, the
	// case does not have to match, and a term nobody wrote matches nothing.
	if hits, total, err := store.QueryTurns(TurnQuery{Search: "100%"}); err != nil || total != 1 || hits[0].ID != first {
		t.Fatalf("QueryTurns(search=100%%) = %d rows, %d total, %v", len(hits), total, err)
	}
	if _, total, err := store.QueryTurns(TurnQuery{Search: "100_"}); err != nil || total != 0 {
		t.Fatalf("QueryTurns(search=100_) total = %d, %v (the _ must be literal)", total, err)
	}
	if _, total, err := store.QueryTurns(TurnQuery{Search: "INSUFFICIENT"}); err != nil || total != 1 {
		t.Fatalf("QueryTurns(search=INSUFFICIENT) total = %d, %v (the search is not case sensitive)", total, err)
	}
	if _, total, err := store.QueryTurns(TurnQuery{Search: "nothing wrote this"}); err != nil || total != 0 {
		t.Fatalf("QueryTurns(search=missing) total = %d, %v", total, err)
	}

	// Paging: a page is bounded, the count is the filter's, and a second page continues
	// the first without repeating a row.
	one, total, err := store.QueryTurns(TurnQuery{Limit: 2})
	if err != nil || total != 5 || len(one) != 2 {
		t.Fatalf("QueryTurns(limit 2) = %d rows, %d total, %v", len(one), total, err)
	}
	two, _, err := store.QueryTurns(TurnQuery{Limit: 2, Offset: 2})
	if err != nil || len(two) != 2 || two[0].ID == one[0].ID || two[0].ID == one[1].ID {
		t.Fatalf("second page repeats the first: %+v vs %+v", two, one)
	}
	if clamped, _, err := store.QueryTurns(TurnQuery{Limit: MaxTurnPageLimit + 100}); err != nil || len(clamped) != 5 {
		t.Fatalf("QueryTurns(limit over the cap) = %d rows, %v", len(clamped), err)
	}
	// Ordering by a named column, oldest first when the caller says asc; anything else
	// reads as the default (id descending).
	asc, _, err := store.QueryTurns(TurnQuery{Order: "created_at", Dir: "asc"})
	if err != nil || asc[0].ID != first {
		t.Fatalf("QueryTurns(order=created_at asc) starts at %+v, %v", asc[0], err)
	}
	bogus, _, err := store.QueryTurns(TurnQuery{Order: "id; drop table tasks"})
	if err != nil || len(bogus) != 5 {
		t.Fatalf("an unknown order must read as the default: %d rows, %v", len(bogus), err)
	}

	// One turn, in full, and the empty answer for one that is not there.
	rec, err := store.GetTurn(first)
	if err != nil || rec == nil || rec.Input != "wire up the deploy 100%" || rec.Agent != planner.Name {
		t.Fatalf("GetTurn = %+v, %v", rec, err)
	}
	if rec.Status != string(LLMStatusFinished) || rec.Provider != LLMProvider("cline") || rec.Mode != ReasonModePlan {
		t.Fatalf("GetTurn lost fields: %+v", rec)
	}
	if rec.StartedAt != "2026-09-20T08:00:00Z" || rec.CreatedAt != "2026-09-20T08:00:00Z" {
		t.Fatalf("GetTurn timestamps = %q / %q", rec.StartedAt, rec.CreatedAt)
	}
	if missing, err := store.GetTurn(999999); err != nil || missing != nil {
		t.Fatalf("GetTurn(missing) = %+v, %v", missing, err)
	}

	// The filter bar: every facet, in one call, most-used first, empty values left out.
	facets, err := store.TurnFacets()
	if err != nil {
		t.Fatalf("TurnFacets: %v", err)
	}
	// The tasks with turns (a task accepted but never run has no turn to facet on).
	if len(facets.Tasks) != 3 {
		t.Fatalf("task facet = %+v, want the three tasks that have turns", facets.Tasks)
	}
	if facets.Tasks[0].Value != "task-a" || facets.Tasks[0].Count != 3 {
		t.Fatalf("task facet is not most-used first: %+v", facets.Tasks)
	}
	if len(facets.Agents) != 2 || facets.Agents[0].Value != planner.Name || facets.Agents[0].Count != 3 {
		t.Fatalf("agent facet = %+v", facets.Agents)
	}
	if len(facets.Statuses) != 2 || facets.Statuses[0].Value != string(LLMStatusFinished) || facets.Statuses[0].Count != 4 {
		t.Fatalf("status facet = %+v", facets.Statuses)
	}
	if len(facets.Models) != 2 || len(facets.Modes) != 2 || len(facets.Providers) != 2 {
		t.Fatalf("facets = %+v", facets)
	}

	// The task selector: the tasks that have a row, the ones that only exist in the log,
	// and a task accepted but never run.
	options, err := store.ListTaskOptions()
	if err != nil || len(options) != 4 {
		t.Fatalf("ListTaskOptions = %+v, %v", options, err)
	}
	byID := map[string]TaskOption{}
	for _, option := range options {
		byID[option.ID] = option
	}
	if got := byID["task-a"]; got.Turns != 3 || got.Description != "first" || got.Status != "completed" || got.LastAt == "" {
		t.Fatalf("task-a option = %+v", got)
	}
	if got := byID["task-ghost"]; got.Turns != 1 || got.Description != "" {
		t.Fatalf("log-only task option = %+v", got)
	}
	if got := byID["task-c"]; got.Turns != 0 || got.LastAt != "" {
		t.Fatalf("never-run task option = %+v", got)
	}
	if options[0].ID != "task-ghost" {
		t.Fatalf("the selector is not ordered by the newest turn: %+v", options)
	}

	// One task's series, in execution order, with the total that says whether the page is
	// the whole series.
	series, total, err := store.ListTurnsByTask("task-a", 0)
	if err != nil || total != 3 || len(series) != 3 {
		t.Fatalf("ListTurnsByTask = %d rows, %d total, %v", len(series), total, err)
	}
	if !(series[0].CreatedAt < series[2].CreatedAt) {
		t.Fatalf("the series is not in execution order: %+v", series)
	}
	if capped, total, err := store.ListTurnsByTask("task-a", 1); err != nil || len(capped) != 1 || total != 3 {
		t.Fatalf("ListTurnsByTask(limit 1) = %d rows, %d total, %v", len(capped), total, err)
	}
	if empty, total, err := store.ListTurnsByTask("", 0); err != nil || len(empty) != 0 || total != 0 {
		t.Fatalf("ListTurnsByTask(no task) = %+v, %d, %v", empty, total, err)
	}
}

// TestPostgresStoreServesTheRuntimesWriters is the whole point of the engine in one
// test: the runtime's own record-keeping (src/store.go's persist*/save* helpers, which
// know only the ports) lands in a real PostgreSQL database and reads back through the
// read ports. Nothing in that path names postgres — that is what makes the engine
// pluggable, and this is that claim exercised against a real server instead of a fake.
func TestPostgresStoreServesTheRuntimesWriters(t *testing.T) {
	store := newPostgresStore(t)
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	persistTask(&Task{ID: "task-1", Description: "ship it", GoalType: GoalType("merge")})
	persistAgent(&Agent{ID: 10050, Name: "agent-10050", State: "running"})
	recordReasonTurn(&Agent{ID: 10050, LLMProvider: LLMProvider("cline"), Model: "m"}, "task-1", 1, ReasonModePlan, "ask", `{"type":"plan"}`)

	planID, saved, err := saveExecutionPlan(
		ExecutionPlan{TaskID: "task-1", AgentID: 10050, Cycle: 1, DecisionType: "plan", StepCount: 2},
		[]ExecutionStepPlan{{Idx: 1, Name: "build", Capability: "shell"}, {Idx: 2, Name: "verify", Capability: "http"}},
	)
	if err != nil {
		t.Fatalf("saveExecutionPlan: %v", err)
	}
	if planID == 0 || len(saved) != 2 || saved[0].ID == 0 || saved[0].PlanID != planID {
		t.Fatalf("the plan did not come back with row ids: plan=%d steps=%+v", planID, saved)
	}
	stepID := saveExecutionStep(ExecutionStep{
		PlanID: planID, PlanStepID: saved[0].ID, TaskID: "task-1", AgentID: 10050, Cycle: 1, Idx: 1,
		Name: "build", Capability: "shell", Status: "ok", Input: `{"cmd":"make"}`, Output: `{"exit":0}`,
	})
	saveExecutionStepInteraction(ExecutionStepInteraction{StepID: stepID, Seq: 1, Kind: "local", Provider: "local"})
	saveVerification(Verification{TaskID: "task-1", PlanID: planID, Cycle: 1, Criterion: "it builds", Result: "pass"})
	pinCompletionContract(
		Decision{Ctx: DecisionContext{Task: &Task{ID: "task-1"}}, Contract: []Criterion{{Name: "it builds", Raw: `{"requirement":"the artifact exists"}`}}},
		planID,
	)

	// Read it all back through the ports the upper layer reads with.
	task, err := store.GetTask("task-1")
	if err != nil || task == nil || task.GoalType != GoalType("merge") {
		t.Fatalf("GetTask = %+v, %v", task, err)
	}
	agent, err := store.GetAgent(10050)
	if err != nil || agent == nil || agent.Name != "agent-10050" || agent.State != "running" {
		t.Fatalf("GetAgent = %+v, %v", agent, err)
	}
	plans, err := store.ListExecutionPlans("task-1")
	if err != nil || len(plans) != 1 || plans[0].ID != planID {
		t.Fatalf("ListExecutionPlans = %+v, %v", plans, err)
	}
	steps, err := store.ListExecutionSteps(planID)
	if err != nil || len(steps) != 1 || steps[0].ID != stepID || steps[0].PlanStepID != saved[0].ID {
		t.Fatalf("ListExecutionSteps = %+v, %v", steps, err)
	}
	if outcome, ok, err := store.ExecutionPlanOutcome(planID); err != nil || !ok || outcome.Planned != 2 || outcome.Executed != 1 {
		t.Fatalf("ExecutionPlanOutcome = %+v, %v, %v", outcome, ok, err)
	}
	verdicts, err := store.ListVerifications("task-1")
	if err != nil || len(verdicts) != 1 || verdicts[0].Result != "pass" || verdicts[0].PlanID != planID {
		t.Fatalf("ListVerifications = %+v, %v", verdicts, err)
	}
	pinned, err := store.ListCompletionContract("task-1")
	if err != nil || len(pinned) != 1 || !strings.Contains(pinned[0].Criterion, "the artifact exists") {
		t.Fatalf("ListCompletionContract = %+v, %v", pinned, err)
	}
	interactions, err := store.ListExecutionStepInteractions(stepID)
	if err != nil || len(interactions) != 1 || interactions[0].StepID != stepID {
		t.Fatalf("ListExecutionStepInteractions = %+v, %v", interactions, err)
	}
	if n, err := store.CountTurns(); err != nil || n != 1 {
		t.Fatalf("CountTurns = %d, %v", n, err)
	}
}

// TestPostgresStoreReopenKeepsItsDataAndItsIds: opening an existing database is
// idempotent (the schema statements are CREATE IF NOT EXISTS), the rows are still
// there, and the agent id sequence is not rewound onto ids that are in use.
func TestPostgresStoreReopenKeepsItsDataAndItsIds(t *testing.T) {
	admin, base := pgAdminConn(t)
	dsn := pgWithDatabase(base, pgTestDatabase(t, admin))

	store, err := OpenPostgresStore(dsn)
	if err != nil {
		t.Fatalf("OpenPostgresStore (first): %v", err)
	}
	first := &Agent{}
	if err := store.UpsertAgent(first); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	if err := store.UpsertTask(&Task{ID: "task-1", Description: "ship it"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenPostgresStore(dsn)
	if err != nil {
		t.Fatalf("OpenPostgresStore (reopen): %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.GetTask("task-1"); err != nil {
		t.Fatalf("GetTask after reopen: %v", err)
	}
	second := &Agent{}
	if err := reopened.UpsertAgent(second); err != nil {
		t.Fatalf("UpsertAgent after reopen: %v", err)
	}
	if second.ID <= first.ID {
		t.Fatalf("agent ids went backwards on reopen: %d then %d", first.ID, second.ID)
	}
}
