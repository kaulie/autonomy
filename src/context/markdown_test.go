package context

import (
	"strings"
	"testing"
)

const sampleDoc = `# Runtime Architecture
## Runtime Responsibility
Runtime executes actions.

## Heartbeat Protocol
Runtime periodically sends heartbeat to the control plane.

### Interval
The interval is configurable.

## Recovery
Runtime recovers after a crash.
`

func TestParseMarkdownSections(t *testing.T) {
	sections, err := ParseMarkdown("doc-001", []byte(sampleDoc))
	if err != nil {
		t.Fatalf("ParseMarkdown: %v", err)
	}
	// The level-1 title introduces the document and has no body of its own, so
	// it is not emitted as a section; the four remaining headings are.
	if len(sections) != 4 {
		t.Fatalf("want 4 sections, got %d: %+v", len(sections), sections)
	}
	if sections[0].Heading != "Runtime Responsibility" || sections[0].Order != 1 {
		t.Errorf("section 0 = %q order %d", sections[0].Heading, sections[0].Order)
	}
	if sections[1].Heading != "Heartbeat Protocol" {
		t.Fatalf("section 1 = %q", sections[1].Heading)
	}
	if sections[1].Path != "Runtime Architecture / Heartbeat Protocol" {
		t.Errorf("heartbeat path = %q", sections[1].Path)
	}
	if sections[2].Heading != "Interval" || sections[2].Path != "Runtime Architecture / Heartbeat Protocol / Interval" {
		t.Errorf("nested section = %q path %q", sections[2].Heading, sections[2].Path)
	}
	if !strings.Contains(sections[1].Content, "heartbeat to the control plane") {
		t.Errorf("heartbeat content = %q", sections[1].Content)
	}
	if sections[1].StartLine != 5 || sections[1].EndLine != 6 {
		t.Errorf("heartbeat line range = %d..%d", sections[1].StartLine, sections[1].EndLine)
	}
	if sections[0].ID != "doc-001#s1" {
		t.Errorf("section id = %q", sections[0].ID)
	}
}

func TestParseMarkdownRejectsNonUTF8(t *testing.T) {
	_, err := ParseMarkdown("doc-bad", []byte{0xff, 0xfe, 0x00})
	if !IsCode(err, ErrParseFailed) {
		t.Fatalf("want PARSE_FAILED, got %v", err)
	}
}

func TestParseMarkdownEmpty(t *testing.T) {
	sections, err := ParseMarkdown("doc-empty", []byte("   \n\n# \n"))
	if err != nil {
		t.Fatalf("ParseMarkdown: %v", err)
	}
	if len(sections) != 0 {
		t.Fatalf("want no sections, got %d", len(sections))
	}
}
