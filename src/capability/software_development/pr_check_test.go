package software_development_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

func TestPRCheckObservesExistenceAndState(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodGet {
			t.Errorf("method=%s, want GET only", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/reviews") {
			t.Error("PR_Check must not list reviews")
		}
		if !strings.HasSuffix(r.URL.Path, "/pulls/70") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"number":70,"html_url":"https://github.com/kaulie/autonomy/pull/70","title":"a change","state":"open","draft":false,"merged":false}`))
	}))
	t.Cleanup(srv.Close)

	c := sd.PRCheck{APIURL: srv.URL, Token: "test-token"}
	out, err := c.Run(map[string]string{"pr": "https://github.com/kaulie/autonomy/pull/70"})
	if err != nil {
		t.Fatal(err)
	}
	if out["exists"] != "true" || out["valid"] != "true" {
		t.Fatalf("exists/valid=%q/%q, want true", out["exists"], out["valid"])
	}
	if out["state"] != "open" || out["merged"] != "false" || out["number"] != "70" {
		t.Fatalf("out=%v", out)
	}
	if _, ok := out["reviews"]; ok {
		t.Fatalf("PR_Check must not report reviews: %v", out)
	}
	for _, p := range paths {
		if strings.Contains(p, "/reviews") {
			t.Fatalf("fetched reviews: %v", paths)
		}
	}
}

func TestPRCheckMissingPullRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	c := sd.PRCheck{APIURL: srv.URL, Token: "test-token"}
	out, err := c.Run(map[string]string{"pr": "https://github.com/kaulie/autonomy/pull/404"})
	if err != nil {
		t.Fatalf("err=%v, want exists=false rather than an error", err)
	}
	if out["exists"] != "false" || out["valid"] != "false" {
		t.Fatalf("out=%v, want exists=false", out)
	}
	if out["state"] != "" {
		t.Fatalf("state=%q, want empty when the pull request is not there", out["state"])
	}
}

func TestPRCheckRequiresAPullRequest(t *testing.T) {
	c := sd.PRCheck{Token: "test-token"}
	if _, err := c.Run(map[string]string{}); err == nil {
		t.Fatal("empty input must be refused")
	}
}
