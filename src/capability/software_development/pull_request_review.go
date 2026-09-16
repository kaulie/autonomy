package software_development

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// ReviewName is the capability's stable semantic name: land a pull request.
	ReviewName = "pull_request.review"
	// ReviewDomain is the semantic domain it belongs to — landing code is part
	// of software development, the domain code_edit and service.deploy live in.
	ReviewDomain = "software_development"
	// ReviewProvider is who really does it: the git host (GitHub), not autonomy.
	// Like service.deploy naming the deployment control plane, the provider says
	// where the effect happens.
	ReviewProvider = "github"

	// DefaultGitHubAPIURL is where GitHub's REST API lives. GITHUB_API_URL
	// overrides it (GitHub Enterprise, or a test server), so pointing the
	// capability somewhere else needs no code change.
	DefaultGitHubAPIURL = "https://api.github.com"

	// EnvGitHubToken / EnvGitHubTokenAlt carry the credential the merge is made
	// with. GH_TOKEN is the gh CLI's own variable, accepted so a host that
	// already exports it needs no extra setting.
	EnvGitHubToken    = "GITHUB_TOKEN"
	EnvGitHubTokenAlt = "GH_TOKEN"
	// EnvGitHubAPIURL overrides the API base URL.
	EnvGitHubAPIURL = "GITHUB_API_URL"
	// EnvRepository names the repository (owner/name) when the caller does not.
	EnvRepository = "GITHUB_REPOSITORY"
	// EnvGitRepoURL is the project's git remote — the URL a task workspace was
	// cloned from; the repository is read out of it.
	EnvGitRepoURL = "GIT_REPO_URL"
	// EnvBaseBranch overrides which branch counts as the trunk.
	EnvBaseBranch = "PR_BASE_BRANCH"
	// DefaultBaseBranch is the last resort for the trunk, when neither the input
	// nor PR_BASE_BRANCH nor the repository itself says what it is.
	DefaultBaseBranch = "main"

	// The three ways GitHub can land a pull request. merge is the default: it
	// keeps the branch's commits, the way the pull requests of this repository
	// have been landed so far.
	mergeMethodMerge  = "merge"
	mergeMethodSquash = "squash"
	mergeMethodRebase = "rebase"

	// mergeStateDirty / mergeStateBlocked are the two `mergeable_state` values
	// that are a refusal rather than a retry: a conflict, and branch protection
	// (required reviews or checks) that is not satisfied.
	mergeStateDirty   = "dirty"
	mergeStateBlocked = "blocked"

	// reviewHTTPTimeout bounds one GitHub call. A merge is a single small
	// request; anything slower than this is a network problem, not patience.
	reviewHTTPTimeout = 30 * time.Second
	// reviewMaxBody bounds how much of a GitHub response is read into memory.
	reviewMaxBody = 1 << 20
	// reviewAPIVersion pins the REST API version these payloads are written
	// against.
	reviewAPIVersion = "2022-11-28"
	// reviewSnippetChars bounds a non-JSON error body kept in a message.
	reviewSnippetChars = 200
)

// PullRequestReview merges one pull request, identified by its branches, into
// the base branch.
//
// It is deliberately not an agent. Merging is a deterministic action against the
// git host with a verifiable answer, so it is code over the GitHub REST API and
// acquires no worker. code_edit may merge the pull request it opened itself (see
// src/agent_policy/CODE_EDIT.md); this capability is the general one — it merges
// the pull request identified by `from` → `to`, whoever opened it.
//
// What it refuses is as much of its contract as what it does. A draft, a
// conflict, red or unfinished checks, or unsatisfied branch protection stop the
// merge and come back as the reason (conflict / checks_failed / checks_pending /
// draft / blocked / not_found) instead of a merge that "sort of" happened.
type PullRequestReview struct {
	// APIURL overrides GITHUB_API_URL / the public API (tests, or an explicit
	// per-call host).
	APIURL string
	// Token overrides GITHUB_TOKEN / GH_TOKEN.
	Token string
	// Repo is the repository (owner/name) when the input does not name one.
	Repo string
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
}

func (PullRequestReview) Name() string { return ReviewName }

func (PullRequestReview) Domain() string { return ReviewDomain }

func (PullRequestReview) Provider() string { return ReviewProvider }

func (PullRequestReview) Description() string {
	return `merge one pull request, identified by its branches, into the base branch. input: {"from":"<head/topic branch>","to":"<base branch>"} ("from_branch"/"head" and "to_branch"/"base" work too; an empty "to" means the trunk: PR_BASE_BRANCH, else the repository's default branch, else main). optional "method": merge (default) / squash / rebase. The merge happens only when the pull request is open, not a draft, free of conflict and its checks are green; otherwise it fails with the reason (not_found / draft / conflict / checks_failed / checks_pending / blocked) instead of merging. output: {"from","to","number","pr","merged":"true","method","sha","checks"} — sha is the merge commit on the base branch, checks is "passed"/"none"`
}

// Run resolves what to merge, checks the gates, then merges. Every gate is
// evaluated before the merge call, and each failure carries its own reason, so a
// refusal reaches the planner as evidence rather than as a generic error.
func (c PullRequestReview) Run(in map[string]string) (map[string]string, error) {
	from := strings.TrimSpace(firstNonEmpty(in["from"], in["from_branch"], in["head"], in["source"]))
	if from == "" {
		return nil, fmt.Errorf("%s: missing from (the head branch whose pull request should be merged)", ReviewName)
	}
	repo := normalizeRepo(firstNonEmpty(in["repo"], in["repository"], c.Repo, os.Getenv(EnvRepository), os.Getenv(EnvGitRepoURL)))
	if repo == "" {
		return nil, fmt.Errorf("%s: missing repository (pass \"repo\":\"owner/name\", or set %s / %s)", ReviewName, EnvRepository, EnvGitRepoURL)
	}
	token := strings.TrimSpace(firstNonEmpty(c.Token, os.Getenv(EnvGitHubToken), os.Getenv(EnvGitHubTokenAlt)))
	if token == "" {
		return nil, fmt.Errorf("%s: no credential (set %s)", ReviewName, EnvGitHubToken)
	}
	method := strings.ToLower(strings.TrimSpace(firstNonEmpty(in["method"], in["merge_method"])))
	if method == "" {
		method = mergeMethodMerge
	}
	switch method {
	case mergeMethodMerge, mergeMethodSquash, mergeMethodRebase:
	default:
		return nil, fmt.Errorf("%s: unknown method %q (want merge, squash or rebase)", ReviewName, method)
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: reviewHTTPTimeout}
	}
	apiURL := strings.TrimRight(strings.TrimSpace(firstNonEmpty(c.APIURL, os.Getenv(EnvGitHubAPIURL), DefaultGitHubAPIURL)), "/")

	// The base branch is resolved first: everything below (which pull request,
	// which gates) is about the from → to pair, not about a branch name alone.
	to, err := c.resolveBase(client, apiURL, repo, token, firstNonEmpty(in["to"], in["to_branch"], in["base"], in["target"]))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ReviewName, err)
	}

	pr, err := c.findPullRequest(client, apiURL, repo, token, from, to)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ReviewName, err)
	}
	if pr.Draft {
		return nil, fmt.Errorf("%s: draft: pull request #%d (%s) is a draft and is not merged", ReviewName, pr.Number, pr.HTMLURL)
	}
	if pr.conflicted() {
		return nil, fmt.Errorf("%s: conflict: pull request #%d (%s) cannot be merged into %s (mergeable_state=%s): resolve it, then ask again", ReviewName, pr.Number, pr.HTMLURL, to, firstNonEmpty(pr.MergeableState, "unknown"))
	}
	if pr.MergeableState == mergeStateBlocked {
		return nil, fmt.Errorf("%s: blocked: pull request #%d (%s) is held by branch protection on %s (required reviews or checks are not satisfied)", ReviewName, pr.Number, pr.HTMLURL, to)
	}
	checks, err := c.gateChecks(client, apiURL, repo, token, pr.Head.SHA)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ReviewName, err)
	}

	merge, err := c.mergePullRequest(client, apiURL, repo, token, pr, method)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ReviewName, err)
	}
	// The base/environment GitHub reports is what is echoed back, not the request:
	// the merge really happened against the branch GitHub names.
	return map[string]string{
		"from":   pr.Head.Ref,
		"to":     pr.Base.Ref,
		"number": strconv.Itoa(pr.Number),
		"pr":     pr.HTMLURL,
		"merged": "true",
		"method": method,
		"sha":    merge.SHA,
		"checks": checks,
	}, nil
}

// resolveBase decides which branch the pull request must target: what the caller
// asked for, then PR_BASE_BRANCH, then the repository's own default branch, then
// main. A repository that cannot be read is not an error here — the pull request
// lookup below reports that far more precisely — so the fallback stands.
func (c PullRequestReview) resolveBase(client *http.Client, apiURL, repo, token, requested string) (string, error) {
	if to := strings.TrimSpace(requested); to != "" {
		return to, nil
	}
	if to := strings.TrimSpace(os.Getenv(EnvBaseBranch)); to != "" {
		return to, nil
	}
	status, body, err := c.get(client, fmt.Sprintf("%s/repos/%s", apiURL, repo), token)
	if err == nil && status >= 200 && status <= 299 {
		var r gitHubRepo
		if json.Unmarshal(body, &r) == nil {
			if def := strings.TrimSpace(r.DefaultBranch); def != "" {
				return def, nil
			}
		}
	}
	return DefaultBaseBranch, nil
}

// findPullRequest looks the pull request up by its branches: the open one whose
// head is `from` and whose base is `to`. GitHub allows at most one such pair, so
// an empty result is a real "there is no such pull request" and not a paging
// artefact.
func (c PullRequestReview) findPullRequest(client *http.Client, apiURL, repo, token, from, to string) (gitHubPull, error) {
	q := url.Values{}
	q.Set("state", "open")
	q.Set("head", headParam(repo, from))
	q.Set("base", to)
	q.Set("sort", "created")
	q.Set("direction", "desc")
	q.Set("per_page", "10")
	endpoint := fmt.Sprintf("%s/repos/%s/pulls?%s", apiURL, repo, q.Encode())

	status, body, err := c.get(client, endpoint, token)
	if err != nil {
		return gitHubPull{}, err
	}
	if err := gitHubExpect(status, body, fmt.Sprintf("list pull requests %s → %s", from, to)); err != nil {
		return gitHubPull{}, err
	}
	var pulls []gitHubPull
	if err := json.Unmarshal(body, &pulls); err != nil {
		return gitHubPull{}, fmt.Errorf("decode pull request list: %w", err)
	}
	if len(pulls) == 0 {
		return gitHubPull{}, fmt.Errorf("not_found: %s has no open pull request from %s into %s", repo, from, to)
	}
	return pulls[0], nil
}

// gateChecks refuses the merge unless the head commit's checks are green, and
// reports what it found: "passed", or "none" when the repository has no checks
// at all. A repository without CI is not a failing repository — nothing is
// waiting to be satisfied, and inventing a failure there would make the
// capability unusable on it.
//
// Both vocabularies are read, because both are in use: GitHub's check runs
// (Actions, and anything posting through the Checks API) and the legacy commit
// statuses. A check that is still running is not green either: it comes back as
// checks_pending so the planner can ask again on a later cycle rather than
// blocking this call.
func (c PullRequestReview) gateChecks(client *http.Client, apiURL, repo, token, sha string) (string, error) {
	if strings.TrimSpace(sha) == "" {
		return "", fmt.Errorf("cannot check %s: the pull request reports no head commit", repo)
	}
	runs, err := c.checkRuns(client, apiURL, repo, token, sha)
	if err != nil {
		return "", err
	}
	if failed := runs.failing(); len(failed) > 0 {
		return "", fmt.Errorf("checks_failed: %s on %s", strings.Join(failed, ", "), shortSHA(sha))
	}
	if pending := runs.pending(); len(pending) > 0 {
		return "", fmt.Errorf("checks_pending: %s on %s — ask again once they finish", strings.Join(pending, ", "), shortSHA(sha))
	}

	legacy, err := c.commitStatus(client, apiURL, repo, token, sha)
	if err != nil {
		return "", err
	}
	// The combined status endpoint reports "pending" when there are no statuses
	// at all, so the count decides whether there is anything to judge.
	if legacy.TotalCount > 0 {
		switch strings.ToLower(strings.TrimSpace(legacy.State)) {
		case "success":
		case "pending":
			return "", fmt.Errorf("checks_pending: commit status on %s is pending — ask again once it finishes", shortSHA(sha))
		case "":
			return "", fmt.Errorf("checks_failed: commit status on %s has no state to judge", shortSHA(sha))
		default:
			return "", fmt.Errorf("checks_failed: commit status on %s is %s", shortSHA(sha), legacy.State)
		}
	}

	if runs.TotalCount > 0 || legacy.TotalCount > 0 {
		return "passed", nil
	}
	return "none", nil
}

// checkRuns reads GitHub's check runs for one commit.
func (c PullRequestReview) checkRuns(client *http.Client, apiURL, repo, token, sha string) (gitHubCheckRuns, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?per_page=100", apiURL, repo, url.PathEscape(sha))
	status, body, err := c.get(client, endpoint, token)
	if err != nil {
		return gitHubCheckRuns{}, err
	}
	if err := gitHubExpect(status, body, "list check runs"); err != nil {
		return gitHubCheckRuns{}, err
	}
	var runs gitHubCheckRuns
	if err := json.Unmarshal(body, &runs); err != nil {
		return gitHubCheckRuns{}, fmt.Errorf("decode check runs: %w", err)
	}
	return runs, nil
}

// commitStatus reads the legacy combined status for one commit.
func (c PullRequestReview) commitStatus(client *http.Client, apiURL, repo, token, sha string) (gitHubStatus, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/commits/%s/status", apiURL, repo, url.PathEscape(sha))
	status, body, err := c.get(client, endpoint, token)
	if err != nil {
		return gitHubStatus{}, err
	}
	if err := gitHubExpect(status, body, "read commit status"); err != nil {
		return gitHubStatus{}, err
	}
	var combined gitHubStatus
	if err := json.Unmarshal(body, &combined); err != nil {
		return gitHubStatus{}, fmt.Errorf("decode commit status: %w", err)
	}
	return combined, nil
}

// mergePullRequest lands the pull request. The head commit that was just checked
// is sent along as `sha`: if the branch moved between the gate and this call,
// GitHub refuses (409) rather than merging a commit nobody looked at.
func (c PullRequestReview) mergePullRequest(client *http.Client, apiURL, repo, token string, pr gitHubPull, method string) (gitHubMerge, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d/merge", apiURL, repo, pr.Number)
	body, err := json.Marshal(map[string]string{"merge_method": method, "sha": pr.Head.SHA})
	if err != nil {
		return gitHubMerge{}, err
	}
	status, payload, err := c.request(client, http.MethodPut, endpoint, token, body)
	if err != nil {
		return gitHubMerge{}, err
	}
	if status < 200 || status > 299 {
		return gitHubMerge{}, gitHubError(status, payload, fmt.Sprintf("merge pull request #%d", pr.Number))
	}
	var merged gitHubMerge
	if err := json.Unmarshal(payload, &merged); err != nil {
		return gitHubMerge{}, fmt.Errorf("decode merge result: %w", err)
	}
	// A 2xx that does not claim a merge is not a merge: reporting success here
	// would hand the planner a merge that never happened.
	if !merged.Merged {
		return gitHubMerge{}, fmt.Errorf("merge pull request #%d: GitHub accepted the request without merging (%s)", pr.Number, firstNonEmpty(merged.Message, "no message"))
	}
	if strings.TrimSpace(merged.SHA) == "" {
		return gitHubMerge{}, fmt.Errorf("merge pull request #%d: merged without reporting the merge commit", pr.Number)
	}
	return merged, nil
}

func (c PullRequestReview) get(client *http.Client, endpoint, token string) (int, []byte, error) {
	return c.request(client, http.MethodGet, endpoint, token, nil)
}

// request is the one place a GitHub call happens: the version-pinned headers
// (including the credential) are set here and the body is read bounded, so no
// caller can pull an unbounded response into memory.
func (c PullRequestReview) request(client *http.Client, method, endpoint, token string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", reviewAPIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, reviewMaxBody))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, payload, nil
}

// gitHubExpect turns a non-2xx into an error, so every caller can share the same
// "call, then judge" shape.
func gitHubExpect(status int, body []byte, what string) error {
	if status >= 200 && status <= 299 {
		return nil
	}
	return gitHubError(status, body, what)
}

// gitHubError carries the git host's own reason — "Not Found", "Pull Request is
// not mergeable" — instead of a bare status code, so the planner can act on it.
func gitHubError(status int, body []byte, what string) error {
	var e struct {
		Message string `json:"message"`
	}
	msg := ""
	if err := json.Unmarshal(body, &e); err == nil {
		msg = strings.TrimSpace(e.Message)
	}
	if msg == "" {
		msg = bodySnippet(body)
	}
	return fmt.Errorf("%s: HTTP %d: %s", what, status, msg)
}

// bodySnippet is a bounded, single-line rendering of a response body.
func bodySnippet(body []byte) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(string(body))), " ")
	if text == "" {
		return "no response body"
	}
	if len(text) > reviewSnippetChars {
		text = text[:reviewSnippetChars] + "…"
	}
	return text
}

// normalizeRepo accepts what a caller reasonably has — "owner/name", an https
// remote, or an ssh remote — and returns owner/name. Anything it cannot read as
// a repository is empty, which the caller reports as a missing repository rather
// than guessing one.
func normalizeRepo(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, ".git")
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		// scheme://[user@]host[:port]/owner/name
		s = s[i+3:]
		if j := strings.IndexByte(s, '/'); j >= 0 {
			s = s[j+1:]
		} else {
			return ""
		}
		return repoSegments(s)
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		// scp-like: git@host:owner/name (or git@host/owner/name)
		s = strings.TrimPrefix(s[i+1:], "/")
		if j := strings.IndexAny(s, ":/"); j >= 0 {
			s = s[j+1:]
		}
	}
	return repoSegments(s)
}

// repoSegments keeps owner/name and drops anything a git URL carries around it.
func repoSegments(s string) string {
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// headParam is how the pull request list filters by branch: a same-repository
// branch is addressed as owner:branch, and a caller that already passed
// owner:branch is left alone.
func headParam(repo, head string) string {
	if strings.Contains(head, ":") {
		return head
	}
	owner := repo
	if i := strings.IndexByte(repo, '/'); i > 0 {
		owner = repo[:i]
	}
	return owner + ":" + head
}

// shortSHA is the short form used in messages, so a reason names the commit it
// is about without pasting a 40-character hash into every error.
func shortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// gitHubPull is the slice of a pull request this capability decides on.
type gitHubPull struct {
	Number         int    `json:"number"`
	HTMLURL        string `json:"html_url"`
	Title          string `json:"title"`
	Draft          bool   `json:"draft"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// conflicted reports whether the pull request cannot be merged as it stands.
// GitHub computes `mergeable` asynchronously, so a null is "not known yet" and
// is not read as a conflict; mergeable_state says "dirty" when the branch really
// has one.
func (p gitHubPull) conflicted() bool {
	if p.Mergeable != nil && !*p.Mergeable {
		return true
	}
	return p.MergeableState == mergeStateDirty
}

// gitHubRepo is the slice of a repository used to learn the trunk.
type gitHubRepo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

// gitHubCheckRuns is GitHub's check-run list for one commit.
type gitHubCheckRuns struct {
	TotalCount int `json:"total_count"`
	CheckRuns  []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"check_runs"`
}

// failing names the checks that finished badly. Only the passing conclusions are
// named as passing: an unrecognised one is not green, so the gate fails closed
// rather than merging on a vocabulary it does not know.
func (r gitHubCheckRuns) failing() []string {
	var out []string
	for _, run := range r.CheckRuns {
		switch run.Conclusion {
		case "", "success", "neutral", "skipped":
			continue
		default:
			out = append(out, fmt.Sprintf("%s (%s)", run.Name, run.Conclusion))
		}
	}
	return out
}

// pending names the checks that have not finished yet.
func (r gitHubCheckRuns) pending() []string {
	var out []string
	for _, run := range r.CheckRuns {
		if !strings.EqualFold(run.Status, "completed") {
			out = append(out, fmt.Sprintf("%s (%s)", run.Name, firstNonEmpty(run.Status, "queued")))
		}
	}
	return out
}

// gitHubStatus is the legacy combined commit status for one commit.
type gitHubStatus struct {
	State      string `json:"state"`
	TotalCount int    `json:"total_count"`
}

// gitHubMerge is GitHub's answer to a merge request.
type gitHubMerge struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}
