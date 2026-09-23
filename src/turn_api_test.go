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

// The data API's tests, from both ends: the store reads it is built on (the port's
// contract: filtering, ordering, paging, facets, task options) and the endpoints
// themselves (docs/http-api.md「数据 API」). The log they read is one small fixture
// with every state the contract names — an agent that has a row and one that does
// not, a task whose row is gone, a cost nobody reported, a Chinese prompt
// (truncation is by rune), and a row written the way the pre-integer-id database
// wrote agent_id.

// turnLogAt is the clock the fixture's rows are stamped with, so ordering assertions
// are about the data and not about how fast the test runs.
var turnLogAt = time.Date(2026, 9, 20, 7, 0, 0, 0, time.UTC)

// seedTurnLog opens a store and writes the fixture log. Row ids come from SQLite in
// insert order, so A < B < … < F (F is inserted last and holds the highest id).
func seedTurnLog(t *testing.T) (rawStore, map[string]int64) {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "turns.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.UpsertTask(&Task{
		ID: "task-29", Description: "主界面增加显示当前agent已经执行的轮次", Domain: TaskDomainSoftwareDevelopment,
		Status: TaskStatusBlocked, AgentID: 10001,
		ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-749a0238"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTask(&Task{
		ID: "task-28", Description: "开放服务契约的前端入口", Domain: TaskDomainSoftwareDevelopment,
		Status: TaskStatusError,
	}); err != nil {
		t.Fatal(err)
	}
	// task-27 is deliberately absent from tasks: a task the log knows and the rows do
	// not (deleted, or never written) is still a candidate.

	for _, agent := range []*Agent{
		{ID: 10001, Name: "agent-10001", State: "running", Lifecycle: AgentLifecycleEphemeral},
		{ID: 10002, Name: "agent-10002", State: "idle", Lifecycle: AgentLifecycleEphemeral},
	} {
		if err := store.UpsertAgent(agent); err != nil {
			t.Fatal(err)
		}
	}

	cost := 2.1322308
	half := 0.5
	zero := 0.0
	turns := []ReasonTurn{
		{ // A: a plan turn with cost, and the literal % and _ the search must respect
			TaskID: "task-29", AgentID: 10001, Cycle: 1, Mode: ReasonModePlan,
			LLMProvider: llmbackend.ProviderCline, Model: "deepseek-v4-flash",
			Input: "plan this: 100% done, 50_50 path", RawOutput: "```json\n{\"type\":\"plan\"}\n```",
			Status: string(llmbackend.StatusFinished), DurationMS: 445086, CostCents: &cost,
			CreatedAt: turnLogAt.Add(time.Minute),
		},
		{ // B: the same agent one cycle later, one of the two "agent" turns
			TaskID: "task-29", AgentID: 10001, Cycle: 2, Mode: ReasonModeAgent,
			LLMProvider: llmbackend.ProviderCline, Model: "deepseek-v4-flash",
			Input: "do the thing", RawOutput: "done", Status: string(llmbackend.StatusFinished),
			DurationMS: 1200, TotalTokens: 3000, CreatedAt: turnLogAt.Add(2 * time.Minute),
		},
		{ // C: another agent, a failure, and a provider that is not the default
			TaskID: "task-29", AgentID: 10002, Cycle: 1, Mode: ReasonModePlan,
			LLMProvider: llmbackend.ProviderCursor, Model: "composer-2",
			Input: "re-plan", RawOutput: "", Status: string(llmbackend.StatusError),
			ErrorMessage: "upstream said no", DurationMS: 300, InputTokens: 10, CostCents: &half,
			CreatedAt: turnLogAt.Add(3 * time.Minute),
		},
		{ // D: a task with no row of its own; a cost of zero is zero, not unknown
			TaskID: "task-27", AgentID: 10001, Cycle: 1, Mode: ReasonModePlan,
			LLMProvider: llmbackend.ProviderCline, Model: "deepseek-v4-flash",
			Input: "这是中文提示词：unrelated prompt", RawOutput: "ok",
			Status: string(llmbackend.StatusFinished), DurationMS: 60, TotalTokens: 5, CostCents: &zero,
			CreatedAt: turnLogAt.Add(4 * time.Minute),
		},
		{ // E: an agent id no agent row answers, and no cost at all
			TaskID: "task-28", AgentID: 10099, Cycle: 1, Mode: ReasonModeAgent,
			LLMProvider: llmbackend.ProviderCursor, Model: "composer-2",
			Input: "orphan agent", RawOutput: "ok", Status: string(llmbackend.StatusFinished),
			DurationMS: 90, TotalTokens: 7, CreatedAt: turnLogAt.Add(5 * time.Minute),
		},
	}
	ids := map[string]int64{}
	for i, turn := range turns {
		if err := store.InsertReasonTurn(turn); err != nil {
			t.Fatal(err)
		}
		// The fixture is keyed by letter, not by "the ids are 1, 2, 3": what a row's
		// id actually is belongs to the database.
		var id int64
		if err := store.RawDB().QueryRow(`
SELECT id FROM reason_turns WHERE task_id = ? AND agent_id = ? AND cycle = ? AND mode = ?`,
			turn.TaskID, turn.AgentID, turn.Cycle, string(turn.Mode)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[string(rune('A'+i))] = id
	}

	// F: agent_id as the pre-integer-id schema wrote it (a uuid in a TEXT column), and
	// a created_at older than every turn above.
	res, err := store.RawDB().Exec(`
INSERT INTO reason_turns (task_id, agent_id, cycle, mode, input, raw_output, normalized_output, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"task-28", "a-old", 1, "plan", "legacy row", "legacy", "legacy", string(llmbackend.StatusFinished),
		turnLogAt.Add(-time.Hour).UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ids["F"] = legacy
	return store, ids
}

func TestTurnQueryStoreFiltersOrdersAndPages(t *testing.T) {
	store, ids := seedTurnLog(t)

	all, total, err := store.QueryTurns(TurnQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 || len(all) != 6 {
		t.Fatalf("total=%d rows=%d, want 6 and 6", total, len(all))
	}
	if all[0].ID != ids["F"] {
		t.Fatalf("default order puts %d first, want the newest id %d", all[0].ID, ids["F"])
	}

	cases := []struct {
		name  string
		query TurnQuery
		want  int
	}{
		{"task_id", TurnQuery{TaskID: "task-29"}, 3},
		{"mode", TurnQuery{Mode: ReasonModeAgent}, 2},
		{"model", TurnQuery{Model: "composer-2"}, 2},
		{"status", TurnQuery{Status: string(llmbackend.StatusError)}, 1},
		{"agent name", TurnQuery{Agent: "agent-10001"}, 3},
		{"agent nobody answers", TurnQuery{Agent: "agent-10099"}, 0},
		{"search, literal percent", TurnQuery{Search: "100%"}, 1},
		{"search, literal underscore", TurnQuery{Search: "50_50"}, 1},
		{"search, no wildcards", TurnQuery{Search: "%100"}, 0},
		{"search, plain substring", TurnQuery{Search: "unrelated"}, 1},
		{"filter and search together", TurnQuery{TaskID: "task-29", Search: "re-plan"}, 1},
		{"nothing matches", TurnQuery{TaskID: "task-none"}, 0},
	}
	for _, tc := range cases {
		turns, total, err := store.QueryTurns(tc.query)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(turns) != tc.want || total != tc.want {
			t.Errorf("%s: rows=%d total=%d, want %d", tc.name, len(turns), total, tc.want)
		}
	}

	// A filter that matches nothing is an empty slice, not nil: the endpoint answers
	// with [] and a nil Go slice would marshal as null.
	none, _, err := store.QueryTurns(TurnQuery{TaskID: "task-none"})
	if err != nil {
		t.Fatal(err)
	}
	if none == nil {
		t.Fatal("a filter matching nothing returned a nil slice")
	}

	// Ordering: a whitelisted column, the direction, and the id tiebreak.
	shortest, _, err := store.QueryTurns(TurnQuery{Order: "duration_ms", Dir: "asc"})
	if err != nil {
		t.Fatal(err)
	}
	// F is the raw-inserted row: it never went through InsertReasonTurn, so its
	// duration is the column's own default (0) and it opens the ascending order.
	for i, want := range []int64{ids["F"], ids["D"], ids["E"], ids["C"], ids["B"], ids["A"]} {
		if shortest[i].ID != want {
			t.Fatalf("duration_ms asc[%d]=%d, want %d", i, shortest[i].ID, want)
		}
	}
	byTime, _, err := store.QueryTurns(TurnQuery{Order: "created_at", Dir: "asc"})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []int64{ids["F"], ids["A"], ids["B"], ids["C"], ids["D"], ids["E"]}
	for i, id := range wantOrder {
		if byTime[i].ID != id {
			t.Fatalf("created_at asc[%d]=%d, want %d", i, byTime[i].ID, id)
		}
	}
	// An order nobody knows falls back to id instead of reaching the SQL text.
	unknown, _, err := store.QueryTurns(TurnQuery{Order: "id; DROP TABLE reason_turns"})
	if err != nil {
		t.Fatal(err)
	}
	if unknown[0].ID != ids["F"] {
		t.Fatalf("unknown order puts %d first, want the id fallback %d", unknown[0].ID, ids["F"])
	}

	// Paging: the window is the page, the total is the filter.
	page, total, err := store.QueryTurns(TurnQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 || len(page) != 2 || page[0].ID != ids["D"] || page[1].ID != ids["C"] {
		t.Fatalf("limit=2 offset=2 → total=%d rows=%v, want 6 and [D C]", total, turnIDsOf(page))
	}

	// A limit above the cap is clamped, not obeyed (and not refused).
	clamped, _, err := store.QueryTurns(TurnQuery{Limit: MaxTurnPageLimit + 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(clamped) != 6 {
		t.Fatalf("a limit above the cap returned %d rows", len(clamped))
	}
}

func turnIDsOf(turns []TurnRecord) []int64 {
	ids := make([]int64, 0, len(turns))
	for _, turn := range turns {
		ids = append(ids, turn.ID)
	}
	return ids
}

// TestTurnQueryStoreReadsTheContractShape pins the fields the contract names: the
// renames (llm_provider → provider, raw_output → output), the agent-name join, the
// NULL-vs-zero cost, the verbatim timestamps, and a task's series by execution order.
func TestTurnQueryStoreReadsTheContractShape(t *testing.T) {
	store, ids := seedTurnLog(t)

	first, err := store.GetTurn(ids["A"])
	if err != nil || first == nil {
		t.Fatalf("GetTurn=%+v err=%v", first, err)
	}
	if first.TaskID != "task-29" || first.Cycle != 1 || first.Mode != ReasonModePlan {
		t.Fatalf("turn A reads as %+v", first)
	}
	if first.AgentID != 10001 || first.Agent != "agent-10001" {
		t.Fatalf("agent = %d / %q, want 10001 / agent-10001", first.AgentID, first.Agent)
	}
	if first.Provider != llmbackend.ProviderCline || first.Model != "deepseek-v4-flash" {
		t.Fatalf("provider/model = %q / %q", first.Provider, first.Model)
	}
	if !strings.Contains(first.Output, "```json") {
		t.Fatalf("output is not the verbatim raw_output: %q", first.Output)
	}
	if first.CostCents == nil || *first.CostCents != 2.1322308 {
		t.Fatalf("cost_cents=%v, want 2.1322308", first.CostCents)
	}
	if first.CreatedAt != "2026-09-20T07:01:00Z" {
		t.Fatalf("created_at=%q, want the stored text", first.CreatedAt)
	}

	// No agent row: the turn still reads, with an empty name, and a cost nobody
	// reported stays unknown instead of becoming zero.
	orphan, err := store.GetTurn(ids["E"])
	if err != nil || orphan == nil {
		t.Fatalf("GetTurn(E)=%+v err=%v", orphan, err)
	}
	if orphan.Agent != "" || orphan.AgentID != 10099 {
		t.Fatalf("orphan agent = %q / %d, want \"\" / 10099", orphan.Agent, orphan.AgentID)
	}
	if orphan.CostCents != nil {
		t.Fatalf("cost_cents=%v, want nil (the provider reported none)", *orphan.CostCents)
	}

	// The last row: a TEXT agent_id from the old schema reads, joined to nothing.
	legacy, err := store.GetTurn(ids["F"])
	if err != nil || legacy == nil {
		t.Fatalf("GetTurn(F)=%+v err=%v", legacy, err)
	}
	if legacy.AgentID != 0 || legacy.Agent != "" {
		t.Fatalf("legacy row agent = %d / %q, want 0 / \"\"", legacy.AgentID, legacy.Agent)
	}

	// A missing turn is (nil, nil): the endpoint's 404, not a read failure.
	if missing, err := store.GetTurn(ids["F"] + 1000); err != nil || missing != nil {
		t.Fatalf("GetTurn(missing)=%+v err=%v, want nil, nil", missing, err)
	}

	// A task's series is in execution order, whole, with its total.
	series, total, err := store.ListTurnsByTask("task-29", 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(series) != 3 {
		t.Fatalf("task-29 series total=%d rows=%d, want 3", total, len(series))
	}
	for i, want := range []int64{ids["A"], ids["B"], ids["C"]} {
		if series[i].ID != want {
			t.Fatalf("series[%d]=%d, want %d (created_at ascending)", i, series[i].ID, want)
		}
	}
	capped, total, err := store.ListTurnsByTask("task-28", 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(capped) != 1 || capped[0].ID != ids["F"] {
		t.Fatalf("limit=1 → total=%d rows=%v, want 2 and [F]", total, turnIDsOf(capped))
	}
	empty, total, err := store.ListTurnsByTask("task-none", 0)
	if err != nil || total != 0 || empty == nil || len(empty) != 0 {
		t.Fatalf("task-none → rows=%v total=%d err=%v, want an empty slice", empty, total, err)
	}

	if count, err := store.CountTurns(); err != nil || count != 6 {
		t.Fatalf("CountTurns=%d err=%v, want 6", count, err)
	}
}

// TestTurnQueryStoreFacets is the filter bar's data: distinct values with counts,
// empty values left out, most-used first — including the agents facet, which is the
// agent *name* (a turn whose agent row does not exist counts nowhere).
func TestTurnQueryStoreFacets(t *testing.T) {
	store, _ := seedTurnLog(t)

	facets, err := store.TurnFacets()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]TurnFacetValue{
		"tasks":     {{Value: "task-29", Count: 3}, {Value: "task-28", Count: 2}, {Value: "task-27", Count: 1}},
		"agents":    {{Value: "agent-10001", Count: 3}, {Value: "agent-10002", Count: 1}},
		"modes":     {{Value: "plan", Count: 4}, {Value: "agent", Count: 2}},
		"models":    {{Value: "deepseek-v4-flash", Count: 3}, {Value: "composer-2", Count: 2}},
		"providers": {{Value: "cline", Count: 3}, {Value: "cursor", Count: 2}},
		"statuses":  {{Value: "finished", Count: 5}, {Value: "error", Count: 1}},
	}
	got := map[string][]TurnFacetValue{
		"tasks": facets.Tasks, "agents": facets.Agents, "modes": facets.Modes,
		"models": facets.Models, "providers": facets.Providers, "statuses": facets.Statuses,
	}
	for name, wantValues := range want {
		values := got[name]
		if len(values) != len(wantValues) {
			t.Errorf("%s facet = %v, want %v", name, values, wantValues)
			continue
		}
		for i := range wantValues {
			if values[i] != wantValues[i] {
				t.Errorf("%s facet[%d] = %v, want %v", name, i, values[i], wantValues[i])
			}
		}
	}
}

// dataAPIHandler is the data API wired to the fixture log: the endpoints are served
// by the same HTTP server the deployment runs, so a test here is a test of what the
// benchmark tool will actually call.
func dataAPIHandler(t *testing.T) (http.Handler, map[string]int64) {
	t.Helper()
	store, ids := seedTurnLog(t)
	return NewHTTPServer(&Autonomy{Store: store}).Handler(), ids
}

// getJSON drives one GET and decodes a 200 into into (nil to only look at the
// status and the raw body).
func getJSON(t *testing.T, handler http.Handler, path string, into any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if into != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("GET %s: %v (body %s)", path, err, rec.Body.String())
		}
	}
	return rec
}

// TestDataAPIReasonTurnList is GET /api/reason-turns end to end: the filters reach
// the store, the page echoes the window it used, and preview cuts input / output by
// rune (a Chinese prompt is five characters, not five bytes).
func TestDataAPIReasonTurnList(t *testing.T) {
	handler, ids := dataAPIHandler(t)

	var page ReasonTurnListResponse
	rec := getJSON(t, handler, "/api/reason-turns?task_id=task-29&preview=1&truncate=5", &page)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if page.Total != 3 || len(page.Turns) != 3 {
		t.Fatalf("total=%d rows=%d, want 3", page.Total, len(page.Turns))
	}
	if page.Limit != DefaultTurnPageLimit || page.Offset != 0 {
		t.Fatalf("window = limit %d offset %d, want the defaults", page.Limit, page.Offset)
	}
	byID := map[int64]TurnRecord{}
	for _, turn := range page.Turns {
		byID[turn.ID] = turn
	}
	a := byID[ids["A"]]
	if a.Input != "plan " || a.Output != "```js" {
		t.Fatalf("preview of A = %q / %q, want the first 5 runes", a.Input, a.Output)
	}

	// A preview is a prefix and the limit is in runes: five Chinese characters are
	// fifteen bytes, and both facts have to hold at once.
	var chinese ReasonTurnListResponse
	getJSON(t, handler, "/api/reason-turns?task_id=task-27&preview=1&truncate=5", &chinese)
	if len(chinese.Turns) != 1 {
		t.Fatalf("task-27 page=%+v", chinese.Turns)
	}
	if got := chinese.Turns[0].Input; got != "这是中文提" {
		t.Fatalf("truncated Chinese input = %q, want the first 5 runes", got)
	}
	if !strings.HasPrefix("这是中文提示词：unrelated prompt", chinese.Turns[0].Input) {
		t.Fatalf("the preview is not a prefix of the stored input: %q", chinese.Turns[0].Input)
	}

	// preview=0 keeps the whole text, whatever truncate says.
	var full ReasonTurnListResponse
	getJSON(t, handler, "/api/reason-turns?task_id=task-27&truncate=5", &full)
	if full.Turns[0].Input != "这是中文提示词：unrelated prompt" {
		t.Fatalf("without preview the input should be whole, got %q", full.Turns[0].Input)
	}

	// Parameters nobody can read are the default, and a limit above the cap comes
	// back clamped — the response says which window it really read.
	var clamped ReasonTurnListResponse
	getJSON(t, handler, "/api/reason-turns?limit=10000&offset=-3", &clamped)
	if clamped.Limit != MaxTurnPageLimit || clamped.Offset != 0 {
		t.Fatalf("window = limit %d offset %d, want %d / 0", clamped.Limit, clamped.Offset, MaxTurnPageLimit)
	}
	var unreadable ReasonTurnListResponse
	getJSON(t, handler, "/api/reason-turns?limit=0&offset=abc", &unreadable)
	if unreadable.Limit != DefaultTurnPageLimit || unreadable.Offset != 0 {
		t.Fatalf("window = limit %d offset %d, want the defaults", unreadable.Limit, unreadable.Offset)
	}

	// An empty result is an empty array in the JSON, not null: a page that draws
	// "nothing matched" must not have to handle a missing list.
	rec = getJSON(t, handler, "/api/reason-turns?task_id=task-none", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"turns":[]`) {
		t.Fatalf("empty page = %d %s, want 200 with \"turns\":[]", rec.Code, rec.Body.String())
	}

	// dir=asc is the reading order the other way round.
	var ascending ReasonTurnListResponse
	getJSON(t, handler, "/api/reason-turns?mode=agent&order=id&dir=asc", &ascending)
	if len(ascending.Turns) != 2 || ascending.Turns[0].ID != ids["B"] {
		t.Fatalf("ascending agent turns = %v, want B first", turnIDsOf(ascending.Turns))
	}
}

// TestDataAPIReasonTurnDetail is GET /api/reason-turns/{id}: the whole text (a detail
// page compares prompts), a 400 for an id that is not one, and a 404 for a turn that
// does not exist.
func TestDataAPIReasonTurnDetail(t *testing.T) {
	handler, ids := dataAPIHandler(t)

	var turn TurnRecord
	rec := getJSON(t, handler, "/api/reason-turns/"+strconv.FormatInt(ids["A"], 10), &turn)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if turn.Input != "plan this: 100% done, 50_50 path" || !strings.Contains(turn.Output, "```json") {
		t.Fatalf("detail is not the whole turn: %+v", turn)
	}

	for _, path := range []string{"/api/reason-turns/abc", "/api/reason-turns/0"} {
		if rec := getJSON(t, handler, path, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
	missing := ids["F"] + 1000
	if rec := getJSON(t, handler, "/api/reason-turns/"+strconv.FormatInt(missing, 10), nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET missing turn = %d, want 404", rec.Code)
	}
}

// TestTurnQueryStoreTaskOptions is the selector's data: the tasks that have a row
// unioned with the ones that only appear in the log, most recently active first — and,
// with them, the project and agent a list page groups and links by.
func TestTurnQueryStoreTaskOptions(t *testing.T) {
	store, _ := seedTurnLog(t)

	options, err := store.ListTaskOptions()
	if err != nil {
		t.Fatal(err)
	}
	// updated_at is the clock the write stamped (UpsertTask sets it to now), so the rows
	// with a definition are compared without it and it is checked by presence below.
	want := []TaskOption{
		{ID: "task-28", Description: "开放服务契约的前端入口", Status: TaskStatusError, Turns: 2, LastAt: "2026-09-20T07:05:00Z"},
		{ID: "task-27", Turns: 1, LastAt: "2026-09-20T07:04:00Z"},
		{ID: "task-29", Description: "主界面增加显示当前agent已经执行的轮次", Status: TaskStatusBlocked, Turns: 3, LastAt: "2026-09-20T07:03:00Z",
			ProjectID: "project-749a0238", AgentID: 10001},
	}
	if len(options) != len(want) {
		t.Fatalf("options=%+v, want %d", options, len(want))
	}
	for i := range want {
		got, expected := options[i], want[i]
		got.UpdatedAt, expected.UpdatedAt = "", ""
		if got != expected {
			t.Errorf("options[%d]=%+v, want %+v", i, got, expected)
		}
	}
	// The row's own updated_at travels with the definition; a task that only appears in
	// the log has no row, so it has none (nor a project, nor an agent).
	for _, option := range options {
		hasRow := option.ID != "task-27"
		if hasRow != (option.UpdatedAt != "") {
			t.Errorf("option %s: updated_at=%q, want a row's time exactly when it has a row", option.ID, option.UpdatedAt)
		}
		if option.ID == "task-27" && (option.ProjectID != "" || option.AgentID != 0 || option.Turns != 1) {
			t.Errorf("log-only option = %+v, want no project and no agent", option)
		}
	}
}

// TestDataAPIFacetsTasksAndSeries is the rest of the data API over HTTP: the filter
// bar, the task selector, and one task's execution series.
func TestDataAPIFacetsTasksAndSeries(t *testing.T) {
	handler, ids := dataAPIHandler(t)

	var facets TurnFacets
	if rec := getJSON(t, handler, "/api/reason-turns/facets", &facets); rec.Code != http.StatusOK {
		t.Fatalf("facets = %d %s", rec.Code, rec.Body.String())
	}
	if len(facets.Tasks) != 3 || len(facets.Agents) != 2 || len(facets.Providers) != 2 {
		t.Fatalf("facets = %+v", facets)
	}
	if facets.Agents[0] != (TurnFacetValue{Value: "agent-10001", Count: 3}) {
		t.Fatalf("agents facet = %+v, want the busiest agent first", facets.Agents)
	}

	var tasks TaskOptionListResponse
	if rec := getJSON(t, handler, "/api/tasks", &tasks); rec.Code != http.StatusOK {
		t.Fatalf("tasks = %d %s", rec.Code, rec.Body.String())
	}
	if len(tasks.Tasks) != 3 || tasks.Tasks[0].ID != "task-28" {
		t.Fatalf("tasks = %+v, want the most recent three with task-28 first", tasks.Tasks)
	}
	if tasks.Tasks[1].ID != "task-27" || tasks.Tasks[1].Description != "" || tasks.Tasks[1].Turns != 1 {
		t.Fatalf("log-only task = %+v, want task-27 with no row and one turn", tasks.Tasks[1])
	}

	// The same list, filtered by project: only the task whose own world names it, and an
	// id nobody accepted under is an empty list rather than a 404.
	var byProject TaskOptionListResponse
	if rec := getJSON(t, handler, "/api/tasks?project_id=project-749a0238", &byProject); rec.Code != http.StatusOK {
		t.Fatalf("tasks?project_id = %d %s", rec.Code, rec.Body.String())
	}
	if len(byProject.Tasks) != 1 || byProject.Tasks[0].ID != "task-29" {
		t.Fatalf("tasks?project_id=project-749a0238 = %+v, want task-29 alone", byProject.Tasks)
	}
	if byProject.Tasks[0].ProjectID != "project-749a0238" || byProject.Tasks[0].AgentID != 10001 || byProject.Tasks[0].UpdatedAt == "" {
		t.Fatalf("filtered task = %+v, want its project, its agent and its updated_at", byProject.Tasks[0])
	}
	var unknownProject TaskOptionListResponse
	if rec := getJSON(t, handler, "/api/tasks?project_id=project-nope", &unknownProject); rec.Code != http.StatusOK {
		t.Fatalf("tasks?project_id=project-nope = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if len(unknownProject.Tasks) != 0 {
		t.Fatalf("tasks?project_id=project-nope = %+v, want an empty list", unknownProject.Tasks)
	}

	var series TaskTurnListResponse
	if rec := getJSON(t, handler, "/api/tasks/task-29/turns", &series); rec.Code != http.StatusOK {
		t.Fatalf("series = %d %s", rec.Code, rec.Body.String())
	}
	if series.TaskID != "task-29" || series.Total != 3 || series.Capped {
		t.Fatalf("series = %+v, want 3 uncapped turns", series)
	}
	for i, want := range []int64{ids["A"], ids["B"], ids["C"]} {
		if series.Turns[i].ID != want {
			t.Fatalf("series[%d]=%d, want %d (execution order)", i, series.Turns[i].ID, want)
		}
	}
	// The series carries whole text: comparing two runs needs prompts, not previews.
	if series.Turns[0].Input != "plan this: 100% done, 50_50 path" {
		t.Fatalf("series input = %q, want the whole prompt", series.Turns[0].Input)
	}

	// A capped series says so instead of pretending it is the whole task.
	var capped TaskTurnListResponse
	getJSON(t, handler, "/api/tasks/task-29/turns?limit=2", &capped)
	if !capped.Capped || capped.Total != 3 || len(capped.Turns) != 2 {
		t.Fatalf("limit=2 → %+v, want capped with total 3", capped)
	}
	// A task nobody ever ran is an empty series, not a 404: the page shows it with
	// nothing to compare.
	rec := getJSON(t, handler, "/api/tasks/task-none/turns", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"turns":[]`) {
		t.Fatalf("empty series = %d %s", rec.Code, rec.Body.String())
	}
}

// TestDataAPIMetaAndHealth is the self-description the consumer uses instead of
// probing the schema, and the probe the deployment platform uses — both of which
// answer from the same store.
func TestDataAPIMetaAndHealth(t *testing.T) {
	t.Setenv("APP_VERSION", "cabb1e98")
	handler, _ := dataAPIHandler(t)

	var meta MetaResponse
	if rec := getJSON(t, handler, "/api/meta", &meta); rec.Code != http.StatusOK {
		t.Fatalf("meta = %d %s", rec.Code, rec.Body.String())
	}
	if meta.Service != "autonomy" || meta.Version != "cabb1e98" {
		t.Fatalf("meta identity = %q / %q", meta.Service, meta.Version)
	}
	if meta.ReasonTurns.CycleColumn != "cycle" || meta.ReasonTurns.RawOutputColumn != "raw_output" {
		t.Fatalf("reason_turns columns = %+v", meta.ReasonTurns)
	}
	if !meta.HasTasksTable || meta.Turns != 6 {
		t.Fatalf("meta = %+v, want a tasks table and 6 turns", meta)
	}

	var health healthResponse
	if rec := getJSON(t, handler, "/health", &health); rec.Code != http.StatusOK {
		t.Fatalf("health = %d %s", rec.Code, rec.Body.String())
	}
	if health.Status != "ok" || health.Turns == nil || *health.Turns != 6 {
		t.Fatalf("health = %+v, want ok with 6 turns", health)
	}
}

// TestDataAPIRefusesWhenNoStoreIsBound pins the distinction the whole API rests on:
// "nothing matched" is a 200 with an empty list, "I cannot read the log" is a 500 —
// an evaluation page must never render the second one as the first.
func TestDataAPIRefusesWhenNoStoreIsBound(t *testing.T) {
	handler := NewHTTPServer(&Autonomy{}).Handler()

	for _, path := range []string{
		"/api/reason-turns", "/api/reason-turns/facets", "/api/reason-turns/1",
		"/api/tasks", "/api/tasks/task-29/turns", "/api/meta",
	} {
		rec := getJSON(t, handler, path, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("GET %s = %d, want 500 without a store", path, rec.Code)
			continue
		}
		var refused errResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &refused); err != nil || refused.Error == "" {
			t.Errorf("GET %s refused with %s", path, rec.Body.String())
		}
	}

	// Liveness is not the store's question: /health still answers, without a count.
	rec := getJSON(t, handler, "/health", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"turns"`) {
		t.Fatalf("health without a store = %d %s, want 200 and no turns", rec.Code, rec.Body.String())
	}
}
