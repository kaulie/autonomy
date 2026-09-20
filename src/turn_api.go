package autonomy

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// The data API: the reads the evaluation side (the benchmark tool) makes over HTTP
// instead of opening this runtime's database file (docs/http-api.md「数据 API」).
//
// The split is deliberate. The handlers (src/http_server.go) parse a request and
// render a response; these methods decide what the data *is*, taking the narrower
// store port they need (TurnQueryStore) rather than the whole Store. Moving the
// reader across this boundary is the whole point of the change: which columns,
// which tables and which join answer a question is this side's business now, so a
// schema that moves no longer breaks a consumer that only ever asked for JSON.

// turnQueryStore is the port the data API reads through. A runtime with no store
// refuses — "store not ready", what TaskProgress says — rather than answering with
// an empty log: "nothing was ever logged" and "this process cannot see the log"
// must not look the same to a page that is comparing two runs.
func (r *Autonomy) turnQueryStore() (TurnQueryStore, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	return r.Store, nil
}

// ListReasonTurns reads one page of reason turns: the rows q selects, and how many
// rows q selects in total (the pager's count).
func (r *Autonomy) ListReasonTurns(q TurnQuery) ([]TurnRecord, int, error) {
	store, err := r.turnQueryStore()
	if err != nil {
		return nil, 0, err
	}
	return store.QueryTurns(q)
}

// ReasonTurn reads one turn in full. A missing turn returns (nil, nil).
func (r *Autonomy) ReasonTurn(id int64) (*TurnRecord, error) {
	store, err := r.turnQueryStore()
	if err != nil {
		return nil, err
	}
	return store.GetTurn(id)
}

// ReasonTurnFacets reads every filter's values with their counts.
func (r *Autonomy) ReasonTurnFacets() (*TurnFacets, error) {
	store, err := r.turnQueryStore()
	if err != nil {
		return nil, err
	}
	facets, err := store.TurnFacets()
	if err != nil {
		return nil, err
	}
	return &facets, nil
}

// TaskOptions reads the task selector's candidates.
func (r *Autonomy) TaskOptions() ([]TaskOption, error) {
	store, err := r.turnQueryStore()
	if err != nil {
		return nil, err
	}
	return store.ListTaskOptions()
}

// TaskTurns reads one task's turns in execution order, and how many the task has.
func (r *Autonomy) TaskTurns(taskID string, limit int) ([]TurnRecord, int, error) {
	store, err := r.turnQueryStore()
	if err != nil {
		return nil, 0, err
	}
	return store.ListTurnsByTask(taskID, limit)
}

// TurnCount is how many reason turns the log holds.
func (r *Autonomy) TurnCount() (int, error) {
	store, err := r.turnQueryStore()
	if err != nil {
		return 0, err
	}
	return store.CountTurns()
}

// ReasonTurnListResponse is one page of the turn list: the rows, the total the same
// filter matches (the pager's number, which is not the length of `turns`), and the
// window they were read with — echoed so a client never has to guess what the
// runtime clamped its limit to.
type ReasonTurnListResponse struct {
	Turns  []TurnRecord `json:"turns"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// TaskTurnListResponse is one task's execution series, in execution order. Capped
// means the series is longer than the requested limit: what is here is its
// beginning, not all of it.
type TaskTurnListResponse struct {
	TaskID string       `json:"task_id"`
	Turns  []TurnRecord `json:"turns"`
	Total  int          `json:"total"`
	Capped bool         `json:"capped"`
}

// TaskOptionListResponse is the task selector: every task that has a row or a turn.
type TaskOptionListResponse struct {
	Tasks []TaskOption `json:"tasks"`
}

// MetaResponse is the data API describing itself: which service and which build
// answered, and the two facts a consumer would otherwise have to probe the database
// for (how the turn columns are named, and whether task definitions exist at all).
type MetaResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
	// ReasonTurns names the turn columns as this runtime reads them out.
	ReasonTurns   ReasonTurnColumns `json:"reason_turns"`
	HasTasksTable bool              `json:"has_tasks_table"`
	Turns         int               `json:"turns"`
}

// ReasonTurnColumns is the answer to "what are the columns called this time": the
// names this runtime normalizes to, so a consumer that remembers older ones (a
// database migrated from `step` / `output`) still recognizes what it gets.
type ReasonTurnColumns struct {
	CycleColumn     string `json:"cycle_column"`
	RawOutputColumn string `json:"raw_output_column"`
}

// serviceVersion is which build this runtime is: the release hash the deployment
// stamped into it (build.sh stamps it into cmd/autonomyd, scripts/start.sh exports it
// as APP_VERSION), or "dev" for a binary that was never released.
func serviceVersion() string {
	if version := strings.TrimSpace(os.Getenv("APP_VERSION")); version != "" {
		return version
	}
	return "dev"
}

// queryInt reads an integer query parameter, clamped to [min, max] — a max of 0
// meaning no upper bound. A value that is not an integer is the default, and an
// out-of-range one is the nearest end of the range.
//
// Being forgiving is the contract, not sloppiness: the caller asked for a page and
// the parameter is only how, so an unreadable `limit` must not become a failed
// request (docs/http-api.md「数据 API」: 未知参数忽略，不报错).
func queryInt(raw string, def, min, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	if value < min {
		return min
	}
	if max > 0 && value > max {
		return max
	}
	return value
}

// turnListLimit reads the list endpoint's limit: the page size the request will
// actually read. A value that is not a positive integer is the default — the same
// thing the store does with a limit it cannot use — and one above the cap is the
// cap. The response echoes the result, so a client never has to guess what it got.
func turnListLimit(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return DefaultTurnPageLimit
	}
	if value > MaxTurnPageLimit {
		return MaxTurnPageLimit
	}
	return value
}

// previewRequested reads the list endpoint's preview pair: whether the caller wants
// previews at all (preview=1 / true), and how many characters one may keep
// (truncate). `truncate` only ever makes a page smaller, so anything that is not a
// positive integer is the default — never a page of empty text.
func previewRequested(query url.Values) (bool, int) {
	preview, err := strconv.ParseBool(strings.TrimSpace(query.Get("preview")))
	if err != nil || !preview {
		return false, 0
	}
	chars := DefaultTurnPreviewChars
	if raw := strings.TrimSpace(query.Get("truncate")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			chars = value
		}
	}
	return true, chars
}

// previewTurns is a list page's rows with the bulky text cut down: input and output
// keep their first `chars` runes. It is what `preview=1` asks for, and the reason a
// 50-row page does not have to carry megabytes of prompts.
//
// The truncation is a prefix and nothing more: no ellipsis is appended, because what
// a caller compares against the log (lengths, prefixes) has to stay comparable with
// the row it came from.
func previewTurns(turns []TurnRecord, chars int) []TurnRecord {
	if chars <= 0 {
		return turns
	}
	for i := range turns {
		turns[i].Input = truncateRunes(turns[i].Input, chars)
		turns[i].Output = truncateRunes(turns[i].Output, chars)
	}
	return turns
}

// truncateRunes keeps the first n runes of text. Runes and not bytes: a preview that
// ends halfway through a character is not a preview, and the same limit then means
// the same thing whatever the text is written in.
func truncateRunes(text string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n])
}
