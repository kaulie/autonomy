package software_development

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/kaulie/autonomy/src/capability/spec"
)

const (
	// CheckName is PR_Check: the system verification tool for one pull request.
	// It answers whether that object is real on the git host and what state it
	// is in. It is not a review — that is pull_request.review.
	CheckName     = "pr.check"
	CheckDomain   = ReviewDomain
	CheckProvider = ReviewProvider
)

// PRCheck is the system-level authenticity check for a pull request. Given a
// URL it asks the git host whether that object exists and what GitHub currently
// says about it (open / closed, draft, merged).
//
// The verifier is the only caller that should use it as truth. It never lists
// reviews, never comments, and never merges.
type PRCheck struct {
	APIURL      string
	Token       string
	GhAuthToken GhAuthTokenFn
	Repo        string
	HTTPClient  *http.Client
}

func (PRCheck) Name() string { return CheckName }

func (PRCheck) Domain() string { return CheckDomain }

func (PRCheck) Provider() string { return CheckProvider }

func (PRCheck) ChecksKind() string { return spec.KindPullRequest }

func (PRCheck) Description() string {
	return `PR_Check: the system verification tool for one pull request — does it exist, and what state is it in. Not a review. Name it by URL: "pr":"https://<host>/owner/name/pull/43" ("pr_url"/"pull_request" work too; "owner/name#43" and a bare "43" with "repo" are accepted). output: {"exists","valid","state","draft","merged","number","pr","title","repo"} — exists/valid are "true" when the host has that pull request; state is "open" / "closed"; merged is what the host reports. This capability does not read review opinions.`
}

func (PRCheck) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "pr", Aliases: []string{"pr_url", "pull_request"}, Required: true, Kind: spec.KindPullRequest, Description: "the pull request to check: https://<host>/owner/name/pull/<number>, owner/name#<number>, or <number> when repo names the repository"},
		{Name: "repo", Aliases: []string{"repository"}, Description: "owner/name, when pr does not name the repository; it must not contradict pr"},
	}
}

func (PRCheck) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "exists", Description: `"true" when the git host has this pull request, "false" when it does not`},
		{Name: "valid", Description: `"true" when the pull request is a real object on the host (same as exists)`},
		{Name: "state", Description: `the pull request's state: "open" / "closed"; empty when it does not exist`},
		{Name: "draft", Description: `"true" when the pull request is a draft; empty when it does not exist`},
		{Name: "merged", Description: `"true" when the host has merged it; empty when it does not exist`},
		{Name: "number", Description: "the pull request number; empty when it does not exist"},
		{Name: "pr", Description: "the pull request's url; empty when it does not exist"},
		{Name: "title", Description: "the pull request's title; empty when it does not exist"},
		{Name: "repo", Description: "owner/name the pull request lives in"},
	}
}

func (c PRCheck) review() PullRequestReview {
	return PullRequestReview{
		APIURL:      c.APIURL,
		Token:       c.Token,
		GhAuthToken: c.GhAuthToken,
		Repo:        c.Repo,
		HTTPClient:  c.HTTPClient,
	}
}

func (c PRCheck) Run(in map[string]string) (map[string]string, error) {
	named, err := parsePullReference(firstNonEmpty(in[inputPull], in[inputPullURL], in[inputPullAlt]))
	if err != nil {
		return nil, err
	}
	if named.number == 0 {
		return nil, fmt.Errorf("%s: missing pr (pass \"pr\":\"https://<host>/owner/name/pull/<number>\")", CheckName)
	}
	repo := normalizeRepo(firstNonEmpty(in["repo"], in["repository"], c.Repo))
	if named.repo != "" {
		if repo != "" && !strings.EqualFold(repo, named.repo) {
			return nil, fmt.Errorf("%s: the pull request url names %s but \"repo\" says %s", CheckName, named.repo, repo)
		}
		repo = named.repo
	}
	if repo == "" {
		repo = normalizeRepo(firstNonEmpty(os.Getenv(EnvRepository), os.Getenv(EnvGitRepoURL)))
	}
	if repo == "" {
		return nil, fmt.Errorf("%s: missing repository (pass \"repo\":\"owner/name\", or set %s / %s)", CheckName, EnvRepository, EnvGitRepoURL)
	}

	reader := c.review()
	token, err := reader.tokenForRepo(repo)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, noCredential()
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: reviewHTTPTimeout}
	}
	apiURL := strings.TrimRight(strings.TrimSpace(firstNonEmpty(c.APIURL, os.Getenv(EnvGitHubAPIURL), DefaultGitHubAPIURL)), "/")

	pr, err := reader.getPullRequest(client, apiURL, repo, token, named.number)
	if err != nil {
		if isGitHubNotFound(err) {
			return map[string]string{
				"exists": "false",
				"valid":  "false",
				"repo":   repo,
			}, nil
		}
		return nil, fmt.Errorf("%s: %w", CheckName, err)
	}
	return map[string]string{
		"exists": "true",
		"valid":  "true",
		"state":  pr.State,
		"draft":  strconv.FormatBool(pr.Draft),
		"merged": strconv.FormatBool(pr.Merged),
		"number": strconv.Itoa(pr.Number),
		"pr":     pr.HTMLURL,
		"title":  pr.Title,
		"repo":   repo,
	}, nil
}

func isGitHubNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not_found:") || strings.Contains(msg, "HTTP 404")
}
