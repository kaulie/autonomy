package db

import . "github.com/kaulie/autonomy/src"

import "strings"

// The rules a TurnQuery page is read by: how a search term becomes a pattern, which
// column an `order` may name, which direction a page runs in, and how a page and a
// task's series are bounded. They are the *contract's* rules (TurnQuery in
// src/store.go, the data API in docs/http-api.md) rather than one dialect's, so
// they live here — outside every engine file — and each engine renders them into
// its own SQL. The one thing that stays per-engine is the statement itself.

// turnSearchPattern turns a search term into the LIKE pattern that matches it
// literally: the wildcards are characters the caller searched for, and so is the
// escape character itself.
func turnSearchPattern(term string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term)
	return "%" + escaped + "%"
}

// turnOrderColumn is the whitelist behind the list's `order`: sorting is by one of
// four columns, and anything else reads as id. That is also why no caller string
// reaches the SQL text — an unknown order cannot become an injected one.
func turnOrderColumn(order string) string {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "created_at":
		return "t.created_at"
	case "duration_ms":
		return "t.duration_ms"
	case "total_tokens":
		return "t.total_tokens"
	default:
		return "t.id"
	}
}

// turnSortDirection is the direction the list is sorted in: ascending only when the
// caller says "asc", and descending otherwise — the reading order, newest first, is
// what an unqualified request means. Like turnOrderColumn, it answers with SQL this
// file names rather than with caller text.
func turnSortDirection(dir string) string {
	if strings.EqualFold(strings.TrimSpace(dir), "asc") {
		return "ASC"
	}
	return "DESC"
}

// turnPageLimit / turnPageOffset clamp a page request to a page that may exist: the
// bounds are the contract's (src/store.go), and clamping here as well as in the HTTP
// layer means a caller that clamped — and one that did not — get the same page.
func turnPageLimit(limit int) int {
	if limit <= 0 {
		return DefaultTurnPageLimit
	}
	if limit > MaxTurnPageLimit {
		return MaxTurnPageLimit
	}
	return limit
}

func turnPageOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

// taskTurnLimit clamps one task's execution series the same way.
func taskTurnLimit(limit int) int {
	if limit <= 0 {
		return DefaultTaskTurnLimit
	}
	if limit > MaxTaskTurnLimit {
		return MaxTaskTurnLimit
	}
	return limit
}
