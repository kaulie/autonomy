package eventcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	typePullRequestMerged  = "github.pull_request.merged"
	typePullRequestClosed  = "github.pull_request.closed"
	typePullRequestUpdated = "github.pull_request.updated"
)

// PullRef is the identity of a pull request as event-center subjects it:
// repo:owner/name plus the number in the GitHub payload.
type PullRef struct {
	Repo   string
	Number string
}

// ParsePullTarget reads a watch target: a PR URL, owner/name#N, or owner/name/N.
func ParsePullTarget(raw string) (PullRef, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return PullRef{}, fmt.Errorf("missing pull request target")
	}
	if i := strings.Index(strings.ToLower(s), "/pull"); i >= 0 {
		repo := repoFromPrefix(s[:i])
		tail := strings.TrimLeft(strings.TrimPrefix(s[i+len("/pull"):], "s"), "/")
		if j := strings.IndexAny(tail, "?#/"); j >= 0 {
			tail = tail[:j]
		}
		if repo == "" || !digits(tail) {
			return PullRef{}, fmt.Errorf("cannot read a pull request from %q", raw)
		}
		return PullRef{Repo: repo, Number: tail}, nil
	}
	if j := strings.LastIndexByte(s, '#'); j > 0 {
		num := strings.TrimSpace(s[j+1:])
		repo := repoFromPrefix(s[:j])
		if repo != "" && digits(num) {
			return PullRef{Repo: repo, Number: num}, nil
		}
	}
	return PullRef{}, fmt.Errorf("cannot read a pull request from %q", raw)
}

// ObservePullRequest is one snapshot of a PR as event-center last reported it.
// No matching event means the object is named but not yet observed — still
// watchable; GitHub 404 is not something this client invents.
func (c *Client) ObservePullRequest(ctx context.Context, target string) (map[string]string, error) {
	events, err := c.Recent(ctx, c.stream(), DefaultLookback)
	if err != nil {
		return nil, err
	}
	return PullRequestObservation(target, events)
}

// PullRequestObservation walks events oldest→newest and keeps the last one
// that names target.
func PullRequestObservation(target string, events []Event) (map[string]string, error) {
	ref, err := ParsePullTarget(target)
	if err != nil {
		return nil, err
	}
	out := map[string]string{
		"exists":   "true",
		"observed": "false",
		"merged":   "false",
		"pr":       strings.TrimSpace(target),
		"repo":     ref.Repo,
		"number":   ref.Number,
		"target":   strings.TrimSpace(target),
		"state":    "",
	}
	for _, ev := range events {
		pr, ok := decodeGitHubPR(ev)
		if !ok || !pr.matches(ref) {
			continue
		}
		out["observed"] = "true"
		out["repo"] = pr.Repo
		out["number"] = pr.Number
		out["state"] = pr.State
		out["title"] = pr.Title
		out["merged"] = strconv.FormatBool(pr.Merged)
		out["draft"] = strconv.FormatBool(pr.Draft)
		if pr.URL != "" {
			out["pr"] = pr.URL
			out["target"] = pr.URL
		}
	}
	return out, nil
}

// EventMatchesPullRequest reports whether ev is about the PR named by target.
func EventMatchesPullRequest(ev Event, target string) bool {
	ref, err := ParsePullTarget(target)
	if err != nil {
		return false
	}
	pr, ok := decodeGitHubPR(ev)
	return ok && pr.matches(ref)
}

type githubPR struct {
	Repo   string
	Number string
	URL    string
	Title  string
	State  string
	Draft  bool
	Merged bool
}

func (p githubPR) matches(ref PullRef) bool {
	if !strings.EqualFold(p.Repo, ref.Repo) {
		return false
	}
	return p.Number == ref.Number
}

type githubPRPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		State   string `json:"state"`
		Draft   bool   `json:"draft"`
		Merged  bool   `json:"merged"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func decodeGitHubPR(ev Event) (githubPR, bool) {
	typ := strings.ToLower(strings.TrimSpace(ev.Type))
	if typ != "" && !strings.Contains(typ, "pull_request") {
		return githubPR{}, false
	}
	var p githubPRPayload
	if len(ev.Data) == 0 || json.Unmarshal(ev.Data, &p) != nil {
		return githubPR{}, false
	}
	number := p.PullRequest.Number
	if number == 0 {
		number = p.Number
	}
	repo := strings.TrimSpace(p.Repository.FullName)
	if repo == "" {
		repo = strings.TrimPrefix(strings.TrimSpace(ev.Subject), "repo:")
	}
	if repo == "" || number == 0 {
		return githubPR{}, false
	}
	state := strings.TrimSpace(p.PullRequest.State)
	if state == "" && strings.Contains(typ, "closed") {
		state = "closed"
	}
	if state == "" && (p.Action == "opened" || p.Action == "synchronize" || p.Action == "reopened") {
		state = "open"
	}
	if p.PullRequest.Merged {
		state = "closed"
	}
	return githubPR{
		Repo:   repo,
		Number: strconv.Itoa(number),
		URL:    strings.TrimSpace(p.PullRequest.HTMLURL),
		Title:  strings.TrimSpace(p.PullRequest.Title),
		State:  state,
		Draft:  p.PullRequest.Draft,
		Merged: p.PullRequest.Merged,
	}, true
}

func repoFromPrefix(prefix string) string {
	s := strings.TrimSpace(prefix)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimPrefix(s, "git@")
	if i := strings.IndexByte(s, ':'); i >= 0 && !strings.Contains(s[:i], "/") {
		s = s[i+1:]
	}
	s = strings.Trim(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return ""
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	name = strings.TrimSuffix(name, ".git")
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != "0"
}
