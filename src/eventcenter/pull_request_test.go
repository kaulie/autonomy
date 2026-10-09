package eventcenter

import (
	"encoding/json"
	"testing"
)

func TestParsePullTarget(t *testing.T) {
	ref, err := ParsePullTarget("https://github.com/kaulie/agent-watchdog/pull/9")
	if err != nil || ref.Repo != "kaulie/agent-watchdog" || ref.Number != "9" {
		t.Fatalf("ref=%+v err=%v", ref, err)
	}
	ref, err = ParsePullTarget("kaulie/agent-watchdog#9")
	if err != nil || ref.Repo != "kaulie/agent-watchdog" || ref.Number != "9" {
		t.Fatalf("hash ref=%+v err=%v", ref, err)
	}
}

func TestPullRequestObservationUsesLatestMatchingEvent(t *testing.T) {
	events := []Event{
		{Type: "github.pull_request.opened", Subject: "repo:kaulie/agent-watchdog", Data: json.RawMessage(openPRBody())},
		{Type: "github.push", Subject: "repo:kaulie/agent-watchdog", Data: json.RawMessage(`{"ref":"refs/heads/main"}`)},
		{Type: "github.pull_request.closed", Subject: "repo:kaulie/agent-watchdog", Data: json.RawMessage(mergedPRBody())},
	}
	obs, err := PullRequestObservation("https://github.com/kaulie/agent-watchdog/pull/9", events)
	if err != nil {
		t.Fatal(err)
	}
	if obs["merged"] != "true" || obs["observed"] != "true" || obs["number"] != "9" {
		t.Fatalf("obs=%v", obs)
	}
}

func TestPullRequestObservationWithoutEventsIsStillWatchable(t *testing.T) {
	obs, err := PullRequestObservation("https://github.com/kaulie/agent-watchdog/pull/9", nil)
	if err != nil {
		t.Fatal(err)
	}
	if obs["exists"] != "true" || obs["observed"] != "false" || obs["merged"] != "false" {
		t.Fatalf("obs=%v, want a named PR that event-center has not seen yet", obs)
	}
}

func TestFactFromMergedPullRequest(t *testing.T) {
	fact := FactFrom(Event{
		Provider:  "github",
		Type:      "github.pull_request.closed",
		Subject:   "repo:kaulie/agent-watchdog",
		DedupeKey: "delivery-1",
		Data:      json.RawMessage(mergedPRBody()),
	})
	if fact.Type != typePullRequestMerged || fact.Source != "github" {
		t.Fatalf("fact=%+v", fact)
	}
	if fact.IdempotencyKey != "github:kaulie/agent-watchdog#9:merged" {
		t.Fatalf("key=%q", fact.IdempotencyKey)
	}
	if fact.AssetID != "pull_request:kaulie/agent-watchdog#9" {
		t.Fatalf("asset=%q", fact.AssetID)
	}
}

func TestEventMatchesPullRequest(t *testing.T) {
	ev := Event{Type: "github.pull_request.closed", Data: json.RawMessage(mergedPRBody())}
	if !EventMatchesPullRequest(ev, "https://github.com/kaulie/agent-watchdog/pull/9") {
		t.Fatal("should match the same PR")
	}
	if EventMatchesPullRequest(ev, "https://github.com/kaulie/agent-watchdog/pull/8") {
		t.Fatal("must not match another number")
	}
}
