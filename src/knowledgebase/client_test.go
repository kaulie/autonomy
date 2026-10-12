package knowledgebase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

func TestWritesStayOnKnowledgeBase(t *testing.T) {
	c := New()
	if _, err := c.Register(context.Background(), ctxsvc.Resource{Name: "x"}); ctxsvc.CodeOf(err) != ctxsvc.ErrIndexFailed {
		t.Fatalf("Register: %v", err)
	}
	if _, err := c.Sync(context.Background(), "kn-1"); ctxsvc.CodeOf(err) != ctxsvc.ErrIndexFailed {
		t.Fatalf("Sync: %v", err)
	}
	if err := c.DeleteResource(context.Background(), "kn-1"); ctxsvc.CodeOf(err) != ctxsvc.ErrIndexFailed {
		t.Fatalf("Delete: %v", err)
	}
	if !strings.Contains(c.RegisterResource(context.Background(), ctxsvc.Resource{}).Error(), "knowledge-base") {
		t.Fatal("write error should name knowledge-base")
	}
}

func TestListSearchGetMapProjectOntoEntity(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.URL.Path == "/api/contexts/project/project-749a0238/knowledge":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"entity": map[string]string{"type": "project", "id": "project-749a0238"},
				"knowledge": []map[string]any{{
					"id":       "kn-1",
					"name":     "runtime.md",
					"kind":     "filesystem",
					"provider": "local",
					"source":   map[string]string{"location": "/srv/docs/runtime.md"},
					"revision": "rev-1",
				}},
			})
		case r.URL.Path == "/api/contexts/project/project-749a0238/search":
			if r.URL.Query().Get("q") != "heartbeat" {
				t.Errorf("q=%q", r.URL.Query().Get("q"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{{
					"knowledge_id":  "kn-1",
					"section_id":    "sec-1",
					"document_name": "runtime.md",
					"heading":       "Heartbeat",
					"snippet":       "interval 30s",
					"score":         1.5,
					"revision":      "rev-1",
				}},
			})
		case r.URL.Path == "/api/sections/sec-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":           "sec-1",
				"knowledge_id": "kn-1",
				"heading":      "Heartbeat",
				"content":      "interval 30s",
				"revision":     "rev-1",
				"source":       map[string]string{"location": "/srv/docs/runtime.md"},
			})
		case r.URL.Path == "/api/knowledge/kn-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       "kn-1",
				"name":     "runtime.md",
				"kind":     "source_code_repository",
				"provider": "git",
				"source":   map[string]string{"location": "https://example.invalid/repo.git", "path": "docs"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{URL: srv.URL}

	listed, err := c.ListResources(context.Background(), "project-749a0238")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "kn-1" || listed[0].ProjectID != "project-749a0238" ||
		listed[0].Type != ctxsvc.ResourceDocument || listed[0].Source.Kind != "filesystem" {
		t.Fatalf("list = %+v", listed)
	}

	hits, err := c.Search(context.Background(), ctxsvc.SearchRequest{ProjectID: "project-749a0238", Query: "heartbeat", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ResourceID != "kn-1" || hits[0].SectionID != "sec-1" || hits[0].ResourceName != "runtime.md" {
		t.Fatalf("search = %+v", hits)
	}

	sec, err := c.GetSection(context.Background(), "sec-1")
	if err != nil {
		t.Fatal(err)
	}
	if sec.ResourceID != "kn-1" || sec.Content != "interval 30s" {
		t.Fatalf("section = %+v", sec)
	}

	res, err := c.GetResource(context.Background(), "kn-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != ctxsvc.ResourceRepository || res.Source.Provider != "git" {
		t.Fatalf("resource = %+v", res)
	}

	if _, err := c.ListResources(context.Background(), ""); ctxsvc.CodeOf(err) != ctxsvc.ErrProjectNotFound {
		t.Fatalf("empty project: %v", err)
	}
	if _, err := c.Search(context.Background(), ctxsvc.SearchRequest{ProjectID: "p", Query: "  "}); ctxsvc.CodeOf(err) != ctxsvc.ErrInvalidQuery {
		t.Fatalf("empty query: %v", err)
	}
	if _, err := c.GetSection(context.Background(), "missing"); ctxsvc.CodeOf(err) != ctxsvc.ErrResourceNotFound {
		t.Fatalf("missing section: %v", err)
	}

	joined := strings.Join(seen, "\n")
	for _, want := range []string{
		"GET /api/contexts/project/project-749a0238/knowledge?",
		"GET /api/contexts/project/project-749a0238/search?limit=5&q=heartbeat",
		"GET /api/sections/sec-1?",
		"GET /api/knowledge/kn-1?",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing call %q in\n%s", want, joined)
		}
	}
}

func TestMapHTTPErrorCodes(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   ctxsvc.ErrorCode
	}{
		{404, `{"code":"NOT_FOUND","message":"gone"}`, ctxsvc.ErrResourceNotFound},
		{400, `{"code":"INVALID_QUERY","message":"bad"}`, ctxsvc.ErrInvalidQuery},
		{500, `{"code":"INDEX_FAILED","message":"down"}`, ctxsvc.ErrIndexFailed},
	}
	for _, tc := range cases {
		err := mapHTTPError(tc.status, []byte(tc.body))
		if ctxsvc.CodeOf(err) != tc.want {
			t.Errorf("%d %s: got %s", tc.status, tc.body, ctxsvc.CodeOf(err))
		}
	}
}

func TestBaseURL(t *testing.T) {
	if New().BaseURL() != DefaultURL {
		t.Fatalf("default = %s", New().BaseURL())
	}
	c := &Client{URL: "http://kb.example:4360/"}
	if c.BaseURL() != "http://kb.example:4360" {
		t.Fatalf("explicit = %s", c.BaseURL())
	}
}
