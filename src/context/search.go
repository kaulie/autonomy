package context

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// queryTerms normalises a query into distinct lowercase terms. It is the V1
// lexical tokeniser: word characters by Unicode (so CJK stays grouped) split on
// punctuation and whitespace.
func queryTerms(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]bool, len(fields))
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		t := strings.ToLower(f)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		terms = append(terms, t)
	}
	return terms
}

// matchesFilters reports whether a resource passes the request's type and
// metadata filters.
func matchesFilters(r Resource, req SearchRequest) bool {
	if len(req.ResourceTypes) > 0 {
		ok := false
		for _, t := range req.ResourceTypes {
			if r.Type == t {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for k, v := range req.Metadata {
		if r.Metadata == nil || r.Metadata[k] != v {
			return false
		}
	}
	return true
}

// sectionScore is the V1 relevance heuristic: term frequency with a small boost
// for hits in the heading. A section with no matching term scores 0.
func sectionScore(s Section, terms []string) float64 {
	if len(terms) == 0 {
		return 0
	}
	hay := strings.ToLower(s.Heading + "\n" + s.Path + "\n" + s.Content)
	heading := strings.ToLower(s.Heading)
	var score float64
	for _, t := range terms {
		n := strings.Count(hay, t)
		if n == 0 {
			continue
		}
		score += 1 + math.Log(float64(n))
		if strings.Contains(heading, t) {
			score += 1
		}
	}
	return score
}

// snippet returns a one-line excerpt of content around the first matched term,
// so a SearchResult is a candidate to judge, not the whole document.
func snippet(content, heading string, terms []string, width int) string {
	flat := strings.Join(strings.Fields(strings.ReplaceAll(content, "\n", " ")), " ")
	if flat == "" {
		return heading
	}
	runes := []rune(flat)
	lower := strings.ToLower(flat)
	byteIdx := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 && (byteIdx < 0 || i < byteIdx) {
			byteIdx = i
		}
	}
	idx := 0
	if byteIdx > 0 {
		idx = utf8.RuneCountInString(flat[:byteIdx])
	}
	if width < 40 {
		width = 160
	}
	start := idx - width/3
	if start < 0 {
		start = 0
	}
	end := start + width
	if end > len(runes) {
		end = len(runes)
		if end-width > 0 {
			start = end - width
		} else {
			start = 0
		}
	}
	out := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out = out + "…"
	}
	return out
}

// rankResults keeps the strongest hits, then orders deterministically by
// resource name and section id so equal scores are stable.
func rankResults(results []SearchResult, limit int) []SearchResult {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].ResourceName != results[j].ResourceName {
			return results[i].ResourceName < results[j].ResourceName
		}
		return results[i].SectionID < results[j].SectionID
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}
