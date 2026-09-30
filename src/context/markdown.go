package context

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// markdownHeading matches an ATX heading: one to six '#', whitespace, text.
var markdownHeading = regexp.MustCompile(`^(#{1,6})[ \t]+(.*)$`)

// ParseMarkdown splits a Markdown document into Sections by heading (spec 8):
// each heading opens a Section whose Heading is its text, whose Path is the
// " / "-joined heading ancestry and whose Content is the body up to the next
// heading. Headings that introduce no body (a document title directly above a
// child heading, as in the spec's example) are not emitted as sections.
//
// Sections are numbered from 1 in document order and carry inclusive 1-based
// line ranges into the original document.
func ParseMarkdown(resourceID string, content []byte) ([]Section, error) {
	if !utf8.Valid(content) {
		return nil, Errorf(ErrParseFailed, "markdown source for %q is not valid UTF-8", resourceID)
	}
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")

	type scope struct {
		level   int
		heading string
	}
	var stack []scope
	type draft struct {
		heading   string
		path      string
		body      []string
		startLine int
	}
	var drafts []draft
	cur := -1

	for i, line := range lines {
		if m := markdownHeading.FindStringSubmatch(line); m != nil {
			text := strings.TrimRight(strings.TrimSpace(m[2]), "#")
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			level := len(m[1])
			for len(stack) > 0 && stack[len(stack)-1].level >= level {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, scope{level: level, heading: text})
			names := make([]string, len(stack))
			for j, s := range stack {
				names[j] = s.heading
			}
			drafts = append(drafts, draft{heading: text, path: strings.Join(names, " / "), startLine: i + 1})
			cur = len(drafts) - 1
			continue
		}
		if cur >= 0 {
			drafts[cur].body = append(drafts[cur].body, line)
		}
	}

	sections := make([]Section, 0, len(drafts))
	for _, d := range drafts {
		body := trimTrailingBlank(d.body)
		if len(body) == 0 {
			continue
		}
		order := len(sections) + 1
		sections = append(sections, Section{
			ID:         fmt.Sprintf("%s#s%d", resourceID, order),
			ResourceID: resourceID,
			Heading:    d.heading,
			Path:       d.path,
			Content:    strings.Join(body, "\n"),
			Order:      order,
			StartLine:  d.startLine,
			EndLine:    d.startLine + len(body),
		})
	}
	return sections, nil
}

func trimTrailingBlank(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[:end]
}
