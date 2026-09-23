package db

import . "github.com/kaulie/autonomy/src"

import (
	"fmt"
	"github.com/kaulie/autonomy/src/llmbackend"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The two engines are two implementations of one contract, and this is that sentence as
// a test: the same writes through the same ports answer the same way, whichever engine
// holds the data. It is what makes AUTONOMY_STORE_ENGINE a configuration choice rather
// than a behaviour change — a difference the engine's own tests could not see, because
// each of them only knows its own world.
//
// The snapshot is the *contract's* shape: ids of the rows a caller can address, the
// order of what it reads, the text of its timestamps, and which of its values are NULL.
// Where an engine is free to differ is left out (the migration-era columns, the
// wall-clock a claim stamps, row ids nothing outside can name).
func TestBothEnginesAnswerTheSameWay(t *testing.T) {
	sqliteStore, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "parity.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	defer sqliteStore.Close()
	postgresStore := newPostgresStore(t)

	fromSQLite := contractSnapshot(t, sqliteStore)
	fromPostgres := contractSnapshot(t, postgresStore)

	if len(fromSQLite) < 30 {
		t.Fatalf("the snapshot is too thin to prove anything: %d lines", len(fromSQLite))
	}
	if diff := firstDifference(fromSQLite, fromPostgres); diff != "" {
		t.Fatalf("the engines answered differently:\n%s", diff)
	}
}

// firstDifference reports the first line the two snapshots disagree on, with both
// answers, or "" when they agree.
func firstDifference(left, right []string) string {
	for i := range left {
		if i >= len(right) {
			return fmt.Sprintf("line %d: sqlite has %q, postgres ran out\n", i, left[i])
		}
		if left[i] != right[i] {
			return fmt.Sprintf("line %d:\n  sqlite:   %s\n  postgres: %s\n", i, left[i], right[i])
		}
	}
	if len(right) > len(left) {
		return fmt.Sprintf("postgres answered %d more lines, starting at %q\n", len(right)-len(left), right[len(left)])
	}
	return ""
}

// contractSnapshot drives one engine through every port with the same inputs and
// returns what the contract answers, as comparable lines.
func contractSnapshot(t *testing.T, store Store) []string {
	t.Helper()
	const (
		taskID  = "task-1"
		ghostID = "task-ghost"
	)
	at := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	var lines []string
	add := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	// Tasks: the row, its world, and the empty-value semantics of a later write.
	must("UpsertTask", store.UpsertTask(&Task{
		ID: taskID, Description: "ship it", Domain: TaskDomain("code"), GoalType: GoalType("merge"),
		ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-1"}, Status: "running",
	}))
	must("UpsertTask (empty write)", store.UpsertTask(&Task{ID: taskID, Description: "ship it", Status: "completed"}))
	task, err := store.GetTask(taskID)
	must("GetTask", err)
	add("task %s|%s|%s|%s|%s|%s", task.ID, task.Description, task.Domain, task.GoalType,
		task.ContextRef[ContextContainerTypeProject], task.Status)
	missing, err := store.GetTask("nothing-here")
	must("GetTask (missing)", err)
	add("missing task is nil: %v", missing == nil)

	// Agents: the id/name pairing and a soft delete.
	planner := &Agent{LLMProvider: llmbackend.Provider("cline"), Model: "m"}
	must("UpsertAgent", store.UpsertAgent(planner))
	worker := &Agent{}
	must("UpsertAgent (worker)", store.UpsertAgent(worker))
	add("agents %d|%s|%d|%s", planner.ID, planner.Name, worker.ID, worker.Name)
	read, err := store.GetAgent(planner.ID)
	must("GetAgent", err)
	add("agent %d|%s|%s|%s", read.ID, read.Name, read.State, read.Model)
	must("SoftDeleteAgent", store.SoftDeleteAgent(worker.ID))
	deleted, err := store.GetAgent(worker.ID)
	must("GetAgent (deleted)", err)
	add("deleted agent %s|%t", deleted.State, !deleted.DeletedAt.IsZero())

	// A run in one shot: header, input, return, and how the log reads them back.
	must("InsertReasonTurn", store.InsertReasonTurn(ReasonTurn{
		TaskID: taskID, AgentID: planner.ID, Cycle: 1, Mode: ReasonModePlan,
		LLMProvider: planner.LLMProvider, Model: planner.Model, Input: "wire up the deploy 100%",
		RawOutput: `{"type":"plan"}`, Status: string(llmbackend.StatusFinished),
		CreatedAt: at, StartedAt: at, EndedAt: at.Add(time.Second),
	}))
	turns, total, err := store.QueryTurns(TurnQuery{TaskID: taskID})
	must("QueryTurns", err)
	add("task turns: %d", total)
	rec := turns[0]
	add("turn %s|%d|%s|%s|%s|%s|%s|%s|%s", rec.Mode, rec.Cycle, rec.Model, rec.Provider, rec.Status,
		rec.Output, rec.Agent, rec.CreatedAt, rec.StartedAt)
	messages, err := store.ListLLMMessages(rec.ID)
	must("ListLLMMessages", err)
	for _, m := range messages {
		add("message %d|%s|%s|answers input: %t|%s|%s", m.Seq, m.Role, m.Content, m.ParentID == messages[0].ID,
			m.NormalizedContent, m.Status)
	}
	assistantID, found, err := store.AssistantMessageID(rec.ID)
	must("AssistantMessageID", err)
	add("assistant is the last message: %t (%t)", found, assistantID == messages[len(messages)-1].ID)

	// A streamed run: append events twice (idempotent), a growing aggregated message,
	// then finish with usage — so the header, the stream and the cost are all read back.
	handle, err := store.BeginReasonTurn(ReasonTurn{
		TaskID: taskID, AgentID: worker.ID, Cycle: 1, Mode: ReasonModeAgent,
		LLMProvider: llmbackend.Provider("cursor"), Model: "composer", Input: "ask the worker", CreatedAt: at,
	})
	must("BeginReasonTurn", err)
	events := []llmbackend.Event{
		{Seq: 1, Channel: llmbackend.ChannelThought, Kind: llmbackend.KindThoughtDelta, EventType: "thought_delta", TextDelta: "hmm", CreatedAt: at},
		{Seq: 2, Channel: llmbackend.ChannelAssistant, Kind: llmbackend.KindAssistantDelta, EventType: "assistant_delta", TextDelta: "hi", CreatedAt: at},
	}
	for pass := 0; pass < 2; pass++ {
		must("AppendLLMEvents", store.AppendLLMEvents(handle.TurnID, "run-9", events))
	}
	stored, err := store.ListLLMEvents(handle.TurnID)
	must("ListLLMEvents", err)
	add("events: %d|%s|%s", len(stored), stored[0].Kind, stored[1].TextDelta)
	must("AppendLLMMessages", store.AppendLLMMessages(handle.TurnID, []LLMMessage{
		{Seq: 1, Role: LLMMessageRoleThinking, ParentID: handle.InputMessageID, Content: "hmm", NormalizedContent: "hmm", CreatedAt: at},
	}))
	must("AppendLLMMessages (grown)", store.AppendLLMMessages(handle.TurnID, []LLMMessage{
		{Seq: 1, Role: LLMMessageRoleThinking, ParentID: handle.InputMessageID, Content: "hmmm", NormalizedContent: "hmmm", CreatedAt: at},
	}))
	must("FinishReasonTurn", store.FinishReasonTurn(handle, llmbackend.RunResult{
		RawOutput: "hi there", Status: llmbackend.StatusFinished, ProviderRunID: "run-9", LLMAgentID: "bc-9",
		DurationMS: 1500, EventCount: 2, EndedAt: at.Add(3 * time.Second),
		Usage: llmbackend.Usage{InputTokens: 11, OutputTokens: 4, TotalTokens: 15, CostCents: 0.25, CostKnown: true},
	}))
	streamed, err := store.ListLLMMessages(handle.TurnID)
	must("ListLLMMessages (streamed)", err)
	for _, m := range streamed {
		add("streamed message %d|%s|%s|%s", m.Seq, m.Role, m.Content, m.RunID)
	}
	turns, _, err = store.QueryTurns(TurnQuery{TaskID: taskID, Order: "id", Dir: "asc"})
	must("QueryTurns (both runs)", err)
	for _, r := range turns {
		cost := "none"
		if r.CostCents != nil {
			cost = fmt.Sprintf("%.2f", *r.CostCents)
		}
		add("log %s|%d|%s|%t|%s|%s", r.Mode, r.Cycle, r.Status, r.RunID != "", r.EndedAt, cost)
	}
	add("log count: %d", countOrZero(t, store))

	// The turn log's own reads: facets, the selector, one task's series, a search.
	facets, err := store.TurnFacets()
	must("TurnFacets", err)
	add("facets tasks=%s agents=%s modes=%s", facetLine(facets.Tasks), facetLine(facets.Agents), facetLine(facets.Modes))
	options, err := store.ListTaskOptions()
	must("ListTaskOptions", err)
	for _, option := range options {
		// updated_at is compared by presence, not by value: the two engines keep the same
		// instant in their own column type, and a nanosecond and a microsecond rendering
		// of one clock are not the same string (src/store.md, "两个引擎，两份数据").
		add("option %s|%d|%s|%s|%s|agent=%d|updated=%t", option.ID, option.Turns, option.Description,
			option.Status, option.ProjectID, option.AgentID, option.UpdatedAt != "")
	}
	series, seriesTotal, err := store.ListTurnsByTask(taskID, 0)
	must("ListTurnsByTask", err)
	add("series: %d of %d, first %s", len(series), seriesTotal, series[0].Mode)
	hits, hitTotal, err := store.QueryTurns(TurnQuery{Search: "100%"})
	must("QueryTurns (search)", err)
	add("search 100%%: %d (%d)", len(hits), hitTotal)
	if rec, err := store.GetTurn(hits[0].ID); err != nil {
		t.Fatalf("GetTurn: %v", err)
	} else {
		add("turn in full: %s|%s", rec.Input, rec.NormalizedOutput)
	}

	// A task that only exists in the log, so the selector has both sources.
	must("InsertReasonTurn (orphan)", store.InsertReasonTurn(ReasonTurn{
		TaskID: ghostID, AgentID: planner.ID, Cycle: 1, Mode: ReasonModePlan, Input: "ghost",
		RawOutput: "gone", Status: string(llmbackend.StatusFinished), CreatedAt: at.Add(time.Minute),
	}))
	options, err = store.ListTaskOptions()
	must("ListTaskOptions (with orphan)", err)
	for _, option := range options {
		add("option now %s|%d", option.ID, option.Turns)
	}

	// A task accepted but never run: the selector still offers it.
	must("UpsertTask (never run)", store.UpsertTask(&Task{ID: "task-c", Description: "accepted only"}))
	options, err = store.ListTaskOptions()
	must("ListTaskOptions (never run)", err)
	add("selector: %s", strings.Join(optionIDs(options), ","))

	// The plan, its steps, what ran, and how the outcome is derived.
	planID, err := store.CreateExecutionPlan(ExecutionPlan{
		TaskID: taskID, AgentID: planner.ID, Cycle: 1, DecisionType: "plan", Reason: "because",
		Evidence: `[]`, Need: `{}`, StepCount: 2, PlanHash: "hash",
		ReplyMessageID: assistantID, InputMessageID: messages[0].ID, ReasonTurnID: rec.ID,
	})
	must("CreateExecutionPlan", err)
	add("plan links: %d|%d|%d", planID, assistantID, messages[0].ID)
	must("AppendExecutionStepPlans", store.AppendExecutionStepPlans([]ExecutionStepPlan{
		{PlanID: planID, Idx: 1, Name: "build", Capability: "shell", Input: `{"cmd":"make"}`},
		{PlanID: planID, Idx: 2, Name: "verify", Capability: "http", Input: `{}`},
	}))
	planned, err := store.ListExecutionStepPlan(planID)
	must("ListExecutionStepPlan", err)
	for _, step := range planned {
		add("planned %d|%s|%s|%s|%s", step.Idx, step.Name, step.Capability, step.Input, step.EvidenceRefs)
	}
	stepID, err := store.AppendExecutionStep(ExecutionStep{
		PlanID: planID, PlanStepID: planned[0].ID, TaskID: taskID, AgentID: planner.ID, Cycle: 1, Idx: 1,
		Name: "build", Capability: "shell", Provider: "local", Status: "ok",
		Input: `{"cmd":"make"}`, Output: `{"exit":0}`, StartedAt: at, EndedAt: at.Add(2 * time.Second), DurationMS: 2000,
	})
	must("AppendExecutionStep", err)
	must("AppendExecutionStepInteraction", func() error {
		_, err := store.AppendExecutionStepInteraction(ExecutionStepInteraction{
			StepID: stepID, Seq: 1, Kind: "llm", Provider: "cline", ReasonTurnID: rec.ID})
		return err
	}())
	run, err := store.ListExecutionSteps(planID)
	must("ListExecutionSteps", err)
	for _, step := range run {
		add("ran %d|%s|%s|%s|%s|%s", step.Idx, step.Name, step.Status, step.Input, step.Output, step.EndedAt)
	}
	interactions, err := store.ListExecutionStepInteractions(stepID)
	must("ListExecutionStepInteractions", err)
	add("interactions: %d|%s|%s|%t", len(interactions), interactions[0].Kind, interactions[0].Provider,
		interactions[0].ReasonTurnID == rec.ID)
	outcome, ok, err := store.ExecutionPlanOutcome(planID)
	must("ExecutionPlanOutcome", err)
	add("outcome %t|%d|%d|%s", ok, outcome.Planned, outcome.Executed, outcome.Status)
	plans, err := store.ListExecutionPlans(taskID)
	must("ListExecutionPlans", err)
	add("plans: %d", len(plans))
	inputID, found, err := store.TaskInputMessageID(taskID)
	must("TaskInputMessageID", err)
	add("task input: %t|%t", found, inputID == messages[0].ID)

	// The Completion Contract is pinned once, verdicts are appended.
	must("AppendCompletionContract", store.AppendCompletionContract(ContractCriterion{
		TaskID: taskID, Idx: 1, PlanID: planID, Name: "it builds", Criterion: `{"requirement":"the artifact exists"}`,
	}))
	must("AppendCompletionContract (restated)", store.AppendCompletionContract(ContractCriterion{
		TaskID: taskID, Idx: 1, PlanID: planID, Name: "it builds", Criterion: `{"requirement":"something weaker"}`,
	}))
	contract, err := store.ListCompletionContract(taskID)
	must("ListCompletionContract", err)
	for _, criterion := range contract {
		add("contract %d|%s|%s", criterion.Idx, criterion.Name, criterion.Criterion)
	}
	for _, verdict := range []Verification{
		{TaskID: taskID, PlanID: planID, Cycle: 1, Criterion: "it builds", Requirement: "the artifact exists", Method: "world_model", Evidence: `{"slot":"artifact"}`, Result: "fail", Reason: "nothing there", CreatedAt: at},
		{TaskID: taskID, PlanID: planID, Cycle: 2, Criterion: "it builds", Requirement: "the artifact exists", Method: "registry:http", Evidence: `{}`, Expected: "200", Observed: "200", Result: "pass", CreatedAt: at},
	} {
		if _, err := store.AppendVerification(verdict); err != nil {
			t.Fatalf("AppendVerification: %v", err)
		}
	}
	verdicts, err := store.ListVerifications(taskID)
	must("ListVerifications", err)
	for _, verdict := range verdicts {
		add("verdict %d|%s|%s|%s|%s|%s", verdict.Cycle, verdict.Result, verdict.Method, verdict.Evidence,
			verdict.Expected, verdict.Observed)
	}

	// The inbox: arrival order, one claim at a time, the counters, and the requeue.
	var queued []int64
	for i, content := range []string{"first", "second", "third"} {
		id, err := store.EnqueueMessage(AgentMessage{
			AgentID: planner.ID, TaskID: taskID, Sender: MessageSender("user"), SenderID: "u-1",
			Kind: MessageKindInstruction, Content: content, CreatedAt: at.Add(time.Duration(i) * time.Second),
		})
		must("EnqueueMessage", err)
		queued = append(queued, id)
	}
	count, err := store.CountQueuedMessages(planner.ID)
	must("CountQueuedMessages", err)
	ahead, err := store.CountMessagesAhead(planner.ID, queued[2])
	must("CountMessagesAhead", err)
	add("inbox queued=%d ahead=%d", count, ahead)
	claimed, found, err := store.ClaimNextMessage(planner.ID)
	must("ClaimNextMessage", err)
	add("claimed %t|%s|%s|%t", found, claimed.Content, claimed.Status, claimed.ID == queued[0])
	must("FinishAgentMessage", store.FinishAgentMessage(claimed.ID, MessageStatusDone, ""))
	must("RequeueRunningMessages", store.RequeueRunningMessages(planner.ID))
	again, found, err := store.ClaimNextMessage(planner.ID)
	must("ClaimNextMessage (second)", err)
	// One claimed message can be put back where it was (RequeueMessage): that is how a
	// restart's drain leaves a run for the consumer that starts next — claimed again,
	// in the same place in the queue.
	must("RequeueMessage", store.RequeueMessage(again.ID))
	back, refound, err := store.ClaimNextMessage(planner.ID)
	must("ClaimNextMessage (requeued)", err)
	add("requeued %t|%t", refound, back.ID == queued[1])
	again = back
	must("FinishAgentMessage (failed)", store.FinishAgentMessage(again.ID, MessageStatusFailed, "boom"))
	add("second claim %t|%s|%t", found, again.Content, again.ID == queued[1])
	inbox, err := store.ListAgentMessages(planner.ID, 0)
	must("ListAgentMessages", err)
	for _, msg := range inbox {
		add("inbox message %s|%s|%s|%s", msg.Content, msg.Status, msg.Error, msg.Kind)
	}
	summary, found, err := store.ClaimNextMessage(planner.ID)
	must("ClaimNextMessage (third)", err)
	add("third claim %t|%s", found, summary.Content)
	_, found, err = store.ClaimNextMessage(planner.ID)
	must("ClaimNextMessage (empty)", err)
	add("empty inbox: %t", found)

	return lines
}

// countOrZero is CountTurns with a test failure instead of an error return.
func countOrZero(t *testing.T, store Store) int {
	t.Helper()
	count, err := store.CountTurns()
	if err != nil {
		t.Fatalf("CountTurns: %v", err)
	}
	return count
}

// facetLine renders one facet's values with their counts.
func facetLine(values []TurnFacetValue) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%s:%d", value.Value, value.Count))
	}
	return strings.Join(parts, ",")
}

// optionIDs is the selector's order, as ids.
func optionIDs(options []TaskOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.ID)
	}
	return ids
}

func TestSQLiteEngineDefaultDSNUsesDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDataDir, dir)
	dsn, err := sqliteEngine{}.DefaultDSN()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "autonomy.db"); dsn != want {
		t.Fatalf("default DSN=%q, want %q", dsn, want)
	}
}

// Unset, the default is the one database this machine keeps:
// ~/database/autonomy/autonomy.db. Every consumer — the deployed runtime, a dev
// run, the benchmark tool — reads that file, not one per checkout.
func TestSQLiteEngineDefaultDSNFallsBackToHomeDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvDataDir, "")
	t.Setenv("HOME", home)
	dsn, err := sqliteEngine{}.DefaultDSN()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "database", "autonomy", "autonomy.db")
	if dsn != want {
		t.Fatalf("default DSN=%q, want %q", dsn, want)
	}
}
