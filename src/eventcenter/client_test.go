package eventcenter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientPullsRecentGitHubEvents(t *testing.T) {
	var pulls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/streams":
			_ = json.NewEncoder(w).Encode(StreamsResult{Streams: map[string]int64{"github": 2}, GlobalSeq: 2})
		case "/v1/streams/github/events":
			pulls++
			if r.URL.Query().Get("after") != "0" {
				t.Errorf("after=%s", r.URL.Query().Get("after"))
			}
			_ = json.NewEncoder(w).Encode(ListResult{
				Events: []Event{
					{ID: "evt-1", StreamSeq: 1, Provider: "github", Type: "github.pull_request.opened", Subject: "repo:kaulie/agent-watchdog", Data: json.RawMessage(openPRBody())},
					{ID: "evt-2", StreamSeq: 2, Provider: "github", Type: "github.pull_request.closed", Subject: "repo:kaulie/agent-watchdog", Data: json.RawMessage(mergedPRBody())},
				},
				NextCursor: 2,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := &Client{URL: srv.URL, Token: "tok"}
	events, err := c.Recent(context.Background(), StreamGitHub, 200)
	if err != nil {
		t.Fatal(err)
	}
	if pulls != 1 || len(events) != 2 {
		t.Fatalf("pulls=%d events=%d", pulls, len(events))
	}

	obs, err := c.ObservePullRequest(context.Background(), "https://github.com/kaulie/agent-watchdog/pull/9")
	if err != nil {
		t.Fatal(err)
	}
	if obs["merged"] != "true" || obs["state"] != "closed" || obs["observed"] != "true" {
		t.Fatalf("obs=%v, want the merge event-center already has", obs)
	}
}

func TestClientRecentUnknownStreamIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(StreamsResult{Streams: map[string]int64{}, GlobalSeq: 0})
	}))
	t.Cleanup(srv.Close)
	events, err := (&Client{URL: srv.URL}).Recent(context.Background(), StreamGitHub, 50)
	if err != nil || len(events) != 0 {
		t.Fatalf("events=%v err=%v", events, err)
	}
}

func openPRBody() string {
	return `{"action":"opened","number":9,"pull_request":{"number":9,"html_url":"https://github.com/kaulie/agent-watchdog/pull/9","title":"watch","state":"open","draft":false,"merged":false},"repository":{"full_name":"kaulie/agent-watchdog"}}`
}

func mergedPRBody() string {
	return `{"action":"closed","number":9,"pull_request":{"number":9,"html_url":"https://github.com/kaulie/agent-watchdog/pull/9","title":"watch","state":"closed","draft":false,"merged":true},"repository":{"full_name":"kaulie/agent-watchdog"}}`
}
