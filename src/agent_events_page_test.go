package autonomy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The events page renders each stream item in its own terminal section so long
// transcripts stay scannable; the markup must expose that structure.
func TestAgentEventsPageUsesPerEventSections(t *testing.T) {
	server := NewHTTPServer(nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agents/10001/events?task=task-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET events status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"id=\"events\"",
		".event-section",
		"event-header",
		"event-body",
		"function appendEvent",
		"MAX_SECTIONS",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("events page missing %q", want)
		}
	}
	if strings.Contains(body, "id=\"log\"") {
		t.Fatal("events page should not use the old single #log terminal")
	}
}
