package watcher

import (
	"fmt"
	"strings"
)

const (
	UntilMerged   = "merged"
	UntilClosed   = "closed"
	UntilTerminal = "terminal"

	sourceGitHub           = "github"
	typePullRequestMerged  = "github.pull_request.merged"
	typePullRequestClosed  = "github.pull_request.closed"
	typePullRequestUpdated = "github.pull_request.updated"
)

// PullRequestProbe watches one pull request. Snapshot is how the host reads
// it (typically event-center, not GitHub). The probe knows the object's
// vocabulary — open / closed / merged — not the event-center HTTP client.
type PullRequestProbe struct {
	Snapshot func(target string) (Observation, error)
}

func (PullRequestProbe) Kind() string { return KindPullRequest }

func (PullRequestProbe) DefaultUntil() string { return UntilMerged }

func (p PullRequestProbe) Observe(spec Spec) (Observation, error) {
	if p.Snapshot == nil {
		return nil, fmt.Errorf("watcher: pull_request probe has no snapshot")
	}
	return p.Snapshot(spec.Target)
}

func (PullRequestProbe) ID(spec Spec, obs Observation) string {
	repo := firstNonEmpty(obs["repo"], spec.Fields["repo"])
	number := firstNonEmpty(obs["number"], spec.Fields["number"])
	if repo != "" && number != "" {
		return KindPullRequest + ":" + strings.ToLower(repo) + "#" + number
	}
	return KindPullRequest + ":" + strings.ToLower(firstNonEmpty(obs["pr"], spec.Target))
}

func (PullRequestProbe) Fingerprint(obs Observation) string {
	return strings.ToLower(strings.TrimSpace(obs["state"])) + "|" + strings.TrimSpace(obs["merged"])
}

func (PullRequestProbe) Done(obs Observation, until string) bool {
	merged := obs["merged"] == "true"
	closed := strings.EqualFold(obs["state"], "closed")
	switch strings.ToLower(strings.TrimSpace(until)) {
	case UntilClosed:
		return closed
	case UntilTerminal:
		return merged || closed
	default:
		// until=merged: a closed-unmerged PR will not merge; stop watching.
		return merged || closed
	}
}

func (PullRequestProbe) Change(spec Spec, obs Observation) Change {
	pr := firstNonEmpty(obs["pr"], spec.Target)
	repo := firstNonEmpty(obs["repo"], spec.Fields["repo"])
	number := firstNonEmpty(obs["number"], spec.Fields["number"])
	typ := typePullRequestUpdated
	key := "github:" + repo + "#" + number + ":updated"
	if obs["merged"] == "true" {
		typ = typePullRequestMerged
		key = "github:" + repo + "#" + number + ":merged"
	} else if strings.EqualFold(obs["state"], "closed") {
		typ = typePullRequestClosed
		key = "github:" + repo + "#" + number + ":closed"
	}
	until := spec.Until
	if until == "" {
		until = UntilMerged
	}
	payload := observationPayload(obs, until)
	if pr != "" {
		payload["pr"] = pr
	}
	return Change{
		Source:         sourceGitHub,
		Type:           typ,
		TaskID:         spec.TaskID,
		AssetID:        "pull_request:" + repo + "#" + number,
		Payload:        payload,
		IdempotencyKey: key,
	}
}
