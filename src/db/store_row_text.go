package db

import . "github.com/kaulie/autonomy/src"

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Text values a row keeps, and how a Go value becomes one: a JSON column is kept
// valid even when the caller left it empty, a zero id becomes SQL NULL, and a task's
// context references become the JSON object its column holds.
//
// These are the same in either engine — a JSON column is JSON whichever database
// stores it, and "no reference" is the same empty object — so they live outside the
// engine files, next to the contract they spell out (TaskStore in src/store.go), and
// each engine calls them instead of keeping a second copy in step.
//
// What stays per-engine is the *storage* type and the time format:
// sqlite_encoding.go keeps timestamps as RFC3339 text, postgres_encoding.go keeps
// them as timestamptz.

// orJSONArray / orJSONObject / orJSONObjectText keep a JSON column valid even when
// the caller left it empty, so a reader never has to guess what an empty string
// means. Non-empty text is stored verbatim: the caller's JSON is the record (a
// criterion's own words, the provider's raw event), so the writer does not reformat
// it. orJSONObjectText is the stricter of the two objects: a column whose contract
// says "an object" reads as one.
func orJSONArray(text string) string {
	if strings.TrimSpace(text) == "" {
		return "[]"
	}
	return text
}

func orJSONObject(text string) string {
	if strings.TrimSpace(text) == "" {
		return "{}"
	}
	return text
}

func orJSONObjectText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return "{}"
	}
	return trimmed
}

// nullID writes 0 as SQL NULL: "no such message" and "message 0" are not the same
// thing, and the traceability columns are empty when there was nothing to point at.
func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// taskContextRefJSON is a task's context references as its row keeps them: the
// container type -> container id map as a JSON object. No references is "{}", so
// "this write gave none" and "there are none" are the same row value (UpsertTask
// reads it back to decide whether a write may overwrite what is there).
func taskContextRefJSON(ref map[ContextContainerType]string) string {
	out := map[string]string{}
	for ctype, id := range ref {
		if id == "" {
			continue
		}
		out[string(ctype)] = id
	}
	if len(out) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// parseTaskContextRef is the read side of taskContextRefJSON. A row that says
// nothing — no column value, or "{}" — carries no references.
func parseTaskContextRef(text string) (map[ContextContainerType]string, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "{}" || text == "null" {
		return nil, nil
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("context_ref: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[ContextContainerType]string, len(raw))
	for k, v := range raw {
		out[ContextContainerType(k)] = v
	}
	return out, nil
}
